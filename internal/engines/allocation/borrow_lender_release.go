package allocation

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/accelerator"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/kueue"
)

// Handing borrowed capacity back to a lender Kueue does not reclaim for
// (PR-C of the Kueue borrowing proposal, kueue.releaseToNonReclaimingLenders).
//
// With the option on, what a lender on reclaimWithinCohort Never -- or on
// LowerPriority below the borrowing model's priority -- lends is counted
// (reclaimablePools). Kueue never preempts for such a lender, so when it waits
// for what it lent, the scaling manager lowers the borrowing models itself:
// it is the lender's reclaim agent. A lender Kueue reclaims for (Any, or
// LowerPriority above the model) is left to Kueue, as without the option.

// ReleaseLenderWaiting is the release cause of borrowed capacity handed back
// to a waiting lender Kueue does not reclaim for (lenderReleases).
const ReleaseLenderWaiting ReleaseCause = "lender-waiting"

// LenderReleasePinReason is the reason a lender release lowers a decision
// with, and the scaling-blocked reason on the model while it is in flight.
const LenderReleasePinReason = "released-to-lender"

// LenderReleaseStepName names the decision step of a lender release.
const LenderReleaseStepName = "kueue-lender-release"

// BorrowLedgerVersion is the ledger format this build writes and reads. A
// ledger that names no version is of version 1. One of a newer version is
// discarded (ErrBorrowLedgerVersion): its fields may not mean what this
// build reads them as.
const BorrowLedgerVersion = 1

// ErrBorrowLedgerVersion is returned by UnmarshalBorrowLedger for a ledger a
// newer build wrote in a format this one does not know.
var ErrBorrowLedgerVersion = errors.New("borrow release ledger: unknown version")

// maxLedgerUnits bounds the variants one persisted lender release may name.
const maxLedgerUnits = 64

// ReleasedUnit is one variant a lender release lowered, and to what.
type ReleasedUnit struct {
	Namespace string `json:"namespace"`
	// Model is the variant's model, whose cooldown in the lender's cohort
	// starts when the release lands.
	Model   string `json:"model,omitempty"`
	Variant string `json:"variant"`
	To      int    `json:"to"`
}

// LenderRelease is a release in flight to one waiting lender: the variants it
// lowered, the GPUs it hands back, and when. No further release goes to that
// lender while it exists. It lands once every variant holds no more than its
// target (LandedAt), and clears on the first fresh read of the lender after
// that, or once it outlived the release timeout (lenderTimeout).
type LenderRelease struct {
	LenderKey
	Units []ReleasedUnit `json:"units"`
	GPUs  int            `json:"gpus"`
	// BorrowedBefore is Kueue's borrowed figure of the lender's pool when the
	// release was decided: once a fresh read shows it fallen by GPUs, Kueue
	// has taken the release in.
	BorrowedBefore int        `json:"borrowedBefore,omitempty"`
	ReleasedAt     time.Time  `json:"releasedAt"`
	LandedAt       *time.Time `json:"landedAt,omitempty"`
}

// lenderTimeout is how long a lender release may stay in flight before it is
// dropped and the lender may be released to again: the release actuation
// window, or twice the expected release latency when that is longer (a
// ScaledObject with a long scale-down window).
func (g *BorrowGovernor) lenderTimeout() time.Duration {
	return max(g.settings.ReleaseActuation, 2*g.settings.ReleaseLatency)
}

// lenderPinned reports whether a lender release in flight lowers the
// variant "namespace/variant".
func (g *BorrowGovernor) lenderPinned(key string) bool {
	for _, e := range g.lenderRel {
		for _, u := range e.Units {
			if u.Namespace+"/"+u.Variant == key {
				return true
			}
		}
	}
	return false
}

// modelPinned reports whether a release in flight -- a placement-timeout or
// gate pin, or a lender release -- lowers a variant of req.
func (g *BorrowGovernor) modelPinned(req ModelScalingRequest) bool {
	return slices.ContainsFunc(req.VariantStates, func(st domain.VariantReplicaState) bool {
		k := req.Namespace + "/" + st.VariantName
		_, sticky := g.timeoutHeld[k]
		return sticky || g.lenderPinned(k)
	})
}

// observeLenderReleases advances the lender releases in flight on one
// cycle's states and reads: a release lands once every variant holds no
// more than its target; it clears on a fresh read of its lender after that,
// when its lender is gone from a fresh read of its pool, or once it outlived
// lenderTimeout. A stale read changes nothing.
func (g *BorrowGovernor) observeLenderReleases(chunks []*SharedPoolChunk, states map[string]variantRef, now time.Time) {
	if len(g.lenderRel) == 0 {
		return
	}
	type read struct {
		at      time.Time
		fresh   bool
		lenders map[string]bool
	}
	reads := map[string]read{}
	for _, ch := range chunks {
		r := read{at: ch.ReadAt, fresh: !ch.Stale, lenders: map[string]bool{}}
		for _, l := range ch.Lenders {
			r.lenders[l.Queue] = true
		}
		reads[ch.key()] = r
	}
	for k, e := range g.lenderRel {
		if e.ReleasedAt.After(now) {
			e.ReleasedAt = now // a restored stamp from the future
		}
		if now.Sub(e.ReleasedAt) > g.lenderTimeout() {
			delete(g.lenderRel, k)
			continue
		}
		if e.LandedAt == nil && unitsLanded(e.Units, states) {
			at := now
			e.LandedAt = &at
		}
		r, ok := reads[k.flavorKey()]
		if !ok || !r.fresh {
			continue
		}
		if !r.lenders[k.Lender] || (e.LandedAt != nil && r.at.After(*e.LandedAt)) {
			delete(g.lenderRel, k)
		}
	}
}

// unitsLanded reports whether every released variant is read and holds no
// more than its target.
func unitsLanded(units []ReleasedUnit, states map[string]variantRef) bool {
	for _, u := range units {
		s, ok := states[u.Namespace+"/"+u.Variant]
		if !ok || heldOf(s.state) > u.To {
			return false
		}
	}
	return true
}

// kueueSchedulingInterval is how long a Workload may wait before its wait
// is more than Kueue's admission lag: Kueue retries a pending Workload on
// every change in its cohort, and within this a Workload a free pool covers
// has been admitted. A lender's wait triggers a release once its Workloads
// waited this long, or on two fresh reads in a row (releaseDue); a landed
// release is judged by a read at least this long after it landed.
const kueueSchedulingInterval = time.Minute

// Reasons a waiting lender Kueue does not reclaim for is handed nothing
// (LenderReleaseBlock.Reason).
const (
	// LenderUnservable: what this controller can give back on the lender's
	// flavor -- whole units, never below minReplicas or a namespace's
	// guaranteed figure, at most one replica past the need -- does not cover
	// what the lender still lacks once the cohort's free pool, its own kept
	// part, what is already being freed and what Kueue preempts for it are
	// counted. Anything handed back would sit idle.
	LenderUnservable = "unservable"
	// LenderTaken: a release to the lender landed, yet a read since shows it
	// still waiting and Kueue's borrowed figure not fallen by what was handed
	// back: another borrower took it. Nothing more is released to it while it
	// waits.
	LenderTaken = "taken-by-another-borrower"
)

// LenderReleaseBlock is a waiting lender Kueue does not reclaim for that a
// cycle handed nothing, and why.
type LenderReleaseBlock struct {
	Lender LenderKey
	Reason string
	// Need is what the lender still lacked, Givable what this controller
	// could have handed back on its flavor (LenderUnservable).
	Need, Givable int
	// Models are the "namespace/model" keys that borrow in its cohort and
	// were candidates for the release.
	Models []string
}

// SharedBorrow is a pool where a release to a lender was weighed and Kueue's
// borrowed figure exceeds what this controller's namespaces hold past their
// guaranteed figures there: another borrower holds the rest, possibly a
// second scaling manager that hands the same lender the same GPUs.
type SharedBorrow struct {
	Cohort, Flavor, Resource string
	Kueue, Ours              int
}

// LenderReport is what the last Releases saw of the waiting lenders it did
// not serve, for the engine to log, count and record.
type LenderReport struct {
	Blocked []LenderReleaseBlock
	Shared  []SharedBorrow
}

// LenderReport returns what the last Releases reported.
func (g *BorrowGovernor) LenderReport() LenderReport {
	g.mu.Lock()
	defer g.mu.Unlock()
	return LenderReport{Blocked: slices.Clone(g.report.Blocked), Shared: slices.Clone(g.report.Shared)}
}

// lenderReleases hands borrowed capacity back, on every fresh pool whose entry
// opts in (SharedPoolChunk.ReleaseToNonReclaiming), to each lender that waits,
// that Kueue does not reclaim for, and that has no release in flight. A
// release is sized to what unblocks the lender, net of everything that
// already covers it:
//
//	shortfall = PendingGPUs (its smallest Workload Kueue could admit next)
//	          - its kept part, max(0, G - U)
//	          - what Kueue preempts for it from models it outranks
//	          - the pool's share of: Kueue's raw free pool, the GPUs this
//	            cycle already frees on the flavor (current - target, held -
//	            current: terminating pods) and those of landed releases Kueue
//	            has not yet shown free
//
// The pool's share is one budget for its lenders, taken in order, so two
// lenders never count the same free GPU; Kueue's borrowed figure is another,
// decremented by each release, so two lenders are never handed more than is
// borrowed. The models are lowered by whole units until the shortfall is
// covered -- the last unit may pass it by at most one replica -- or, when
// they cannot cover it, not at all: the lender is reported unservable
// (LenderUnservable) and nothing is handed back to sit idle.
//
//   - Models are taken lowest priority first: Kueue's priority of the model
//     (its priority class, as the pool read it; a class not read last), then
//     its scaling priority, then the most expensive.
//   - A unit is one replica of a single-role model on the lender's flavor, or
//     a whole pair at the model's role ratio for a P/D one (yieldUnit, the
//     rule the reclaim yield uses), so a release never leaves a role without
//     its partner. Only replicas a model holds and would keep count as given
//     back: lowering a target above what it holds frees nothing.
//   - No variant goes below minReplicas, and no namespace below its
//     guaranteed figure (borrowedLeft).
//   - A model with a release in flight (any cause) is skipped. Under a
//     LowerPriority lender a model it outranks is left to Kueue, whose
//     preemption counts against the shortfall, and a model whose priority
//     was not read is left alone: Kueue may preempt it too.
//
// A lender's wait triggers a release only once it was read waiting twice in
// a row, or its Workloads waited past Kueue's admission lag (releaseDue).
// Nothing is released before the persisted ledger is read
// (BorrowCycle.LedgerUnread): a release from before a restart may still be
// in flight.
func (g *BorrowGovernor) lenderReleases(c BorrowCycle, states map[string]variantRef, decisions []domain.VariantDecision) []BorrowRelease {
	g.report = LenderReport{}
	if c.LedgerUnread {
		return nil
	}
	chunks, cohorts := mergeSharedPools(c.Constraints)
	var left map[string]map[string]int
	plain := map[string]domain.VariantReplicaState{}
	for k, s := range states {
		plain[k] = s.state
	}
	var out []BorrowRelease
	for _, ch := range chunks {
		if ch.Stale || !ch.ReleaseToNonReclaiming {
			continue
		}
		var b *poolBudget
		for _, l := range ch.Lenders {
			key := LenderKey{Cohort: ch.Cohort, Lender: l.Queue, Flavor: ch.Flavor, Resource: ch.Resource}
			if !l.Waiting || kueue.Reclaims(l.Reclaim, nil, nil) || l.PendingGPUs <= 0 {
				continue
			}
			if e := g.lenderRel[key]; e != nil {
				continue
			}
			if !g.releaseDue(key, l, ch.ReadAt) || kueueReclaimsAll(l, ch) {
				continue
			}
			if left == nil {
				left = borrowedLeft(c, decisions)
			}
			if b == nil {
				b = g.budgetOf(c, ch, cohorts, plain, decisions, left)
			}
			cands, outranked := g.lenderCandidates(c, key, l, ch, cohorts, plain, left)
			need := l.PendingGPUs - l.Kept - outranked
			covered := min(max(0, need), b.free)
			b.free -= covered
			need -= covered
			if need <= 0 {
				continue
			}
			var rel []BorrowRelease
			gave := min(b.ours, b.borrowed)
			if need <= b.borrowed {
				// Never more than what is still borrowed: two lenders of one
				// pool are not both handed the same borrowed GPUs.
				rel, gave = g.releaseTo(c, key, ch, cands, need, plain, decisions, left)
			}
			if rel == nil {
				block := LenderReleaseBlock{Lender: key, Reason: LenderUnservable, Need: need, Givable: gave}
				for _, cand := range cands {
					block.Models = append(block.Models, modelKey(cand.req))
				}
				g.report.Blocked = append(g.report.Blocked, block)
				continue
			}
			b.borrowed -= gave
			out = append(out, rel...)
		}
		if b != nil && ch.Borrowed > b.ours {
			g.report.Shared = append(g.report.Shared, SharedBorrow{Cohort: ch.Cohort, Flavor: ch.Flavor,
				Resource: ch.Resource, Kueue: ch.Borrowed, Ours: b.ours})
		}
	}
	return out
}

// releaseDue reports whether a lender's wait triggers a release now: it was
// read waiting on two fresh reads in a row (its debounce watch), or the
// longest-waiting of its Workloads has waited past Kueue's admission lag. One
// read alone may catch a Workload between its creation and its admission.
func (g *BorrowGovernor) releaseDue(key LenderKey, l kueue.LenderState, readAt time.Time) bool {
	if w := g.watches[key]; w != nil && w.reads >= 2 {
		return true
	}
	return !l.PendingSince.IsZero() && readAt.Sub(l.PendingSince) > kueueSchedulingInterval
}

// kueueReclaimsAll reports whether Kueue itself reclaims everything borrowed
// on the chunk for the lender: a LowerPriority lender whose head outranks
// the highest priority any borrower holds the cohort's GPUs at.
func kueueReclaimsAll(l kueue.LenderState, ch *SharedPoolChunk) bool {
	return l.Reclaim == kueue.ReclaimLowerPriority && ch.BorrowerPriority != nil &&
		kueue.Reclaims(l.Reclaim, l.PendingPriority, ch.BorrowerPriority)
}

// poolBudget is what one pool's waiting lenders share in a pass
// (lenderReleases): free, the GPUs Kueue admits a lender from with nothing
// handed back; borrowed, what is left of Kueue's borrowed figure to hand
// back; ours, what this controller's namespaces in the cohort hold past
// their guaranteed figures on the flavor.
type poolBudget struct {
	free, borrowed, ours int
}

// budgetOf is a pool's budget at the start of the pass: Kueue's raw free
// pool, plus what the cycle's decisions already free on the flavor in the
// cohort's namespaces -- current - target, and held - current (terminating
// pods) -- plus the GPUs of releases to lenders that landed and that Kueue
// has not yet shown free; and Kueue's borrowed figure.
func (g *BorrowGovernor) budgetOf(c BorrowCycle, ch *SharedPoolChunk, cohorts map[string][]string,
	states map[string]domain.VariantReplicaState, decisions []domain.VariantDecision, left map[string]map[string]int) *poolBudget {
	b := &poolBudget{free: max(0, ch.RawFree), borrowed: max(0, ch.Borrowed)}
	accs := map[string]string{}
	for _, req := range c.Requests {
		for _, st := range req.VariantStates {
			accs[req.Namespace+"/"+st.VariantName] = accOf(req, st.VariantName)
		}
	}
	for _, d := range decisions {
		if !slices.Contains(cohorts[d.Namespace], ch.Cohort) || !ch.serves(accs[d.Namespace+"/"+d.VariantName]) {
			continue
		}
		s := states[d.Namespace+"/"+d.VariantName]
		freeing := max(0, d.CurrentReplicas-d.TargetReplicas) + max(0, heldOf(s)-d.CurrentReplicas)
		b.free += freeing * max(1, d.GPUsPerReplica)
	}
	for k, e := range g.lenderRel {
		if k.flavorKey() == ch.key() && e.LandedAt != nil {
			b.free += e.GPUs
		}
	}
	for ns, perType := range left {
		if !slices.Contains(cohorts[ns], ch.Cohort) {
			continue
		}
		for t, n := range perType {
			if ch.serves(t) {
				b.ours += n
			}
		}
	}
	return b
}

// releaseTo lowers, for one waiting lender, the candidate models unit by unit
// until need GPUs on the lender's flavor are handed back (lenderReleases),
// records the release in flight, and returns its records and the GPUs it
// hands back. When the candidates cannot cover need, nothing is lowered: it
// returns nil and what they could have handed back.
func (g *BorrowGovernor) releaseTo(c BorrowCycle, key LenderKey, ch *SharedPoolChunk, cands []lenderCandidate,
	need int, states map[string]domain.VariantReplicaState, decisions []domain.VariantDecision,
	left map[string]map[string]int) ([]BorrowRelease, int) {
	before := slices.Clone(decisions)
	leftBefore := cloneBorrowedLeft(left)
	released := 0
	touched := map[int]bool{}
	for _, cand := range cands {
		if released >= need {
			break
		}
		ns := cand.req.Namespace
		eligible := func(i int) bool { return ch.serves(accOf(cand.req, decisions[i].VariantName)) }
		mine := map[int]bool{}
		nsBefore := maps.Clone(left[ns])
		gave := 0
		for released+gave < need {
			unit := yieldUnitWhere(decisions, ns, cand.req.ModelID, states, cand.req.Disaggregated, eligible)
			freed, largest, ok := unitTakes(unit, decisions, cand.req, ch, left[ns], states)
			if !ok {
				break
			}
			if freed-(need-released-gave) > largest {
				// The model's smallest unit passes what is still lacking by
				// more than one replica (a 2P:3D pair for 8 GPUs): giving it
				// back would drop far more than the lender waits for. The
				// next model, if any.
				break
			}
			if len(touchedWith(touched, mine, unit)) > maxLedgerUnits {
				break // more variants than one ledger entry may name
			}
			for _, i := range slices.Sorted(maps.Keys(unit)) {
				d := &decisions[i]
				gpr := max(1, d.GPUsPerReplica)
				if t, found := accelerator.FindKey(left[ns], accOf(cand.req, d.VariantName)); found {
					left[ns][t] -= heldBelow(*d, states[ns+"/"+d.VariantName], unit[i]) * gpr
				}
				d.TargetReplicas -= unit[i]
				d.GPUsAllocated = d.TargetReplicas * gpr
				mine[i] = true
			}
			gave += freed
		}
		if gave == 0 {
			// Only growth above what it holds was cancelled: nothing of it is
			// handed back, so nothing of it is lowered.
			for i := range mine {
				decisions[i] = before[i]
			}
			left[ns] = nsBefore
			continue
		}
		for i := range mine {
			touched[i] = true
		}
		released += gave
	}
	if released < need {
		copy(decisions, before)
		clear(left)
		for ns, m := range leftBefore {
			left[ns] = m
		}
		return nil, released
	}
	entry := &LenderRelease{LenderKey: key, GPUs: released, BorrowedBefore: ch.Borrowed, ReleasedAt: c.Now}
	out := make([]BorrowRelease, 0, len(touched))
	models := map[string]bool{}
	for _, i := range slices.Sorted(maps.Keys(touched)) {
		d := &decisions[i]
		lenderStep(d, key)
		entry.Units = append(entry.Units, ReleasedUnit{Namespace: d.Namespace, Model: d.ModelID, Variant: d.VariantName, To: d.TargetReplicas})
		out = append(out, BorrowRelease{Namespace: d.Namespace, ModelID: d.ModelID, Variant: d.VariantName,
			From: d.CurrentReplicas, To: d.TargetReplicas, Cause: ReleaseLenderWaiting, Lender: key})
		models[d.Namespace+"/"+d.ModelID] = true
	}
	slices.SortFunc(entry.Units, func(a, b ReleasedUnit) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Variant, b.Variant))
	})
	g.lenderRel[key] = entry
	for _, m := range slices.Sorted(maps.Keys(models)) {
		g.markBackoff(m, []string{key.Cohort}, c.Now, true)
	}
	return out, released
}

// touchedWith is the decisions a release names once unit is added.
func touchedWith(touched, mine map[int]bool, unit map[int]int) map[int]bool {
	out := maps.Clone(touched)
	maps.Copy(out, mine)
	for i := range unit {
		out[i] = true
	}
	return out
}

// cloneBorrowedLeft is a deep copy of borrowedLeft's figures.
func cloneBorrowedLeft(left map[string]map[string]int) map[string]map[string]int {
	out := make(map[string]map[string]int, len(left))
	for ns, m := range left {
		out[ns] = maps.Clone(m)
	}
	return out
}

// heldBelow is how many of the n replicas a unit takes off d's target the
// variant holds and would otherwise keep: those below min(target, held).
// Lowering a target above what the variant holds frees nothing.
func heldBelow(d domain.VariantDecision, s domain.VariantReplicaState, n int) int {
	h := heldOf(s)
	return max(0, min(d.TargetReplicas, h)-min(d.TargetReplicas-n, h))
}

// unitTakes is the GPUs a unit hands back on the chunk's flavor, counting
// only replicas a variant holds and would keep (heldBelow), the largest GPUs
// of one replica in it, and whether it may be released at all: it must take
// something, and every variant's GPUs must lie within what its namespace
// holds past guaranteed of the variant's type (left).
func unitTakes(unit map[int]int, decisions []domain.VariantDecision, req ModelScalingRequest, ch *SharedPoolChunk,
	left map[string]int, states map[string]domain.VariantReplicaState) (freed, largest int, ok bool) {
	if len(unit) == 0 {
		return 0, 0, false
	}
	per := map[string]int{}
	for i, n := range unit {
		d := decisions[i]
		acc := accOf(req, d.VariantName)
		t, found := accelerator.FindKey(left, acc)
		if !found {
			return 0, 0, false
		}
		gpr := max(1, d.GPUsPerReplica)
		largest = max(largest, gpr)
		g := heldBelow(d, states[req.Namespace+"/"+d.VariantName], n) * gpr
		per[t] += g
		if ch.serves(acc) {
			freed += g
		}
	}
	for t, g := range per {
		if g > left[t] {
			return 0, 0, false // never below the guaranteed figure
		}
	}
	return freed, largest, true
}

// borrowedLeft is, per borrowing namespace and type, what it holds past its
// guaranteed figure (gated pods aside), less what the cycle's decisions
// already lower it by: what a lender release may still take.
func borrowedLeft(c BorrowCycle, decisions []domain.VariantDecision) map[string]map[string]int {
	left := namespaceBorrowed(c.Constraints, c.Gated)
	accs := map[string]string{}
	for _, req := range c.Requests {
		for _, st := range req.VariantStates {
			accs[req.Namespace+"/"+st.VariantName] = accOf(req, st.VariantName)
		}
	}
	for _, d := range decisions {
		lowered := d.CurrentReplicas - d.TargetReplicas
		if lowered <= 0 {
			continue
		}
		if t, ok := accelerator.FindKey(left[d.Namespace], accs[d.Namespace+"/"+d.VariantName]); ok {
			left[d.Namespace][t] = max(0, left[d.Namespace][t]-lowered*max(1, d.GPUsPerReplica))
		}
	}
	return left
}

// lenderCandidate is a model a lender release may lower, with its order keys.
type lenderCandidate struct {
	req      ModelScalingRequest
	priority int64 // Kueue's priority of the model; math.MaxInt64 when not read
	cost     float64
}

// lenderCandidates is the models a release to the lender may lower, in
// release order (lenderReleases): in a namespace of the chunk's cohort that
// holds GPUs past its guaranteed figure on a type the chunk serves, with no
// release in flight. Under a LowerPriority lender, a model its head outranks
// is no candidate: Kueue preempts it for the lender, and its GPUs on the
// flavor past guaranteed are returned as outranked; a model whose priority
// was not read is no candidate either, nor counted.
func (g *BorrowGovernor) lenderCandidates(c BorrowCycle, key LenderKey, l kueue.LenderState, ch *SharedPoolChunk,
	cohorts map[string][]string, states map[string]domain.VariantReplicaState,
	left map[string]map[string]int) (out []lenderCandidate, outranked int) {
	lowerPriority := l.Reclaim == kueue.ReclaimLowerPriority
	preempted := map[string]map[string]int{} // per namespace and type, what Kueue preempts
	for _, req := range c.Requests {
		if !slices.Contains(cohorts[req.Namespace], key.Cohort) || len(left[req.Namespace]) == 0 || g.modelPinned(req) {
			continue
		}
		serves, cost := false, 0.0
		onFlavor := map[string]int{}
		for _, st := range req.VariantStates {
			acc := accOf(req, st.VariantName)
			if t, ok := accelerator.FindKey(left[req.Namespace], acc); ok && left[req.Namespace][t] > 0 && ch.serves(acc) {
				serves = true
				onFlavor[t] += heldOf(st) * max(1, st.GPUsPerReplica)
			}
			cost += float64(states[req.Namespace+"/"+st.VariantName].GPUsPerReplica) * variantCost(req, st.VariantName)
		}
		if !serves {
			continue
		}
		prio, known := modelKueuePriority(c.Constraints, ch, req)
		if lowerPriority {
			if !known {
				continue // Kueue may preempt it for the lender: not ours to lower too
			}
			p := prio
			if kueue.Reclaims(l.Reclaim, l.PendingPriority, &p) {
				if preempted[req.Namespace] == nil {
					preempted[req.Namespace] = map[string]int{}
				}
				for t, n := range onFlavor {
					preempted[req.Namespace][t] += n
				}
				continue // Kueue preempts it for the lender
			}
		}
		cand := lenderCandidate{req: req, priority: math.MaxInt64, cost: cost}
		if known {
			cand.priority = int64(prio)
		}
		out = append(out, cand)
	}
	for ns, perType := range preempted {
		for t, n := range perType {
			outranked += min(n, left[ns][t])
		}
	}
	slices.SortStableFunc(out, func(a, b lenderCandidate) int {
		return cmp.Or(cmp.Compare(a.priority, b.priority), cmp.Compare(a.req.Priority, b.req.Priority),
			cmp.Compare(b.cost, a.cost), strings.Compare(modelKey(a.req), modelKey(b.req)))
	})
	return out, outranked
}

// variantCost is a variant's cost as discovery resolved it, 0 when unknown.
func variantCost(req ModelScalingRequest, variant string) float64 {
	for _, v := range req.Variants {
		if v.VariantName == variant {
			return v.Cost
		}
	}
	return 0
}

// modelKueuePriority is the priority Kueue gives the model's Workloads, as
// the chunk's read shows it: the highest over its variants' priority classes
// (SharedPoolChunk.drawable's lookup); a template naming no class takes the
// higher of that class's figure and its namespace's highest priority. Not
// known when some variant's class was not read.
func modelKueuePriority(constraints []*ResourceConstraints, ch *SharedPoolChunk, req ModelScalingRequest) (int32, bool) {
	var fallback *int32
	for _, c := range constraints {
		if c == nil {
			continue
		}
		for _, b := range c.NamespaceBorrow[req.Namespace] {
			if b.HighestPriority != nil && (fallback == nil || *b.HighestPriority > *fallback) {
				p := *b.HighestPriority
				fallback = &p
			}
		}
	}
	var best int32
	for i, st := range req.VariantStates {
		prio, known := ch.ClassPriority[st.PriorityClass]
		if st.PriorityClass == kueue.PriorityClassNone && fallback != nil {
			if !known || *fallback > prio {
				prio = *fallback
			}
			known = true
		}
		if !known {
			return 0, false
		}
		if i == 0 || prio > best {
			best = prio
		}
	}
	return best, len(req.VariantStates) > 0
}

// lenderStep records on d that a release to a waiting lender lowered it.
func lenderStep(d *domain.VariantDecision, key LenderKey) {
	action := domain.ActionNoChange
	if d.TargetReplicas < d.CurrentReplicas {
		action = domain.ActionScaleDown
	}
	d.SetDecisionReason(action, domain.DecisionReasonV2, string(domain.DecisionReasonV2)+" ("+LenderReleasePinReason+")")
	d.DecisionSteps = append(d.DecisionSteps, domain.DecisionStep{
		Name: LenderReleaseStepName, Action: action, TargetReplicas: d.TargetReplicas, WasConstrained: true,
		Reason: fmt.Sprintf("ClusterQueue %s waits for capacity it lent in cohort %s, and Kueue does not reclaim it; "+
			"handed back (releaseToNonReclaimingLenders)", key.Lender, key.Cohort),
	})
}

// restoreLenderReleases adds the persisted lender releases the governor does
// not hold. An entry naming no complete lender, no unit, a unit that names no
// variant or a target out of bounds, no release time, or more GPUs than any
// controller holds is dropped and counted.
func (g *BorrowGovernor) restoreLenderReleases(entries []LenderRelease, dropped *int) {
	if len(entries) > maxLedgerItems {
		*dropped += len(entries) - maxLedgerItems
		entries = entries[:maxLedgerItems]
	}
	for _, e := range entries {
		k := e.LenderKey
		if k.Cohort == "" || k.Lender == "" || k.Flavor == "" || k.Resource == "" || e.ReleasedAt.IsZero() ||
			len(e.Units) == 0 || len(e.Units) > maxLedgerUnits || e.GPUs < 0 || e.GPUs > maxLedgerReplicas ||
			slices.ContainsFunc(e.Units, func(u ReleasedUnit) bool {
				return u.Namespace == "" || u.Variant == "" || u.To < 0 || u.To > maxLedgerReplicas
			}) {
			*dropped++
			continue
		}
		if _, held := g.lenderRel[k]; held {
			continue
		}
		cp := e
		cp.Units = slices.Clone(e.Units)
		if e.LandedAt != nil {
			at := *e.LandedAt
			cp.LandedAt = &at
		}
		g.lenderRel[k] = &cp
	}
}

// persistLenderReleases is the lender releases in flight, in a stable order,
// nil when none.
func (g *BorrowGovernor) persistLenderReleases() []LenderRelease {
	if len(g.lenderRel) == 0 {
		return nil
	}
	out := make([]LenderRelease, 0, len(g.lenderRel))
	for _, e := range g.lenderRel {
		cp := *e
		cp.Units = slices.Clone(e.Units)
		cp.ReleasedAt = e.ReleasedAt.UTC()
		if e.LandedAt != nil {
			at := e.LandedAt.UTC()
			cp.LandedAt = &at
		}
		out = append(out, cp)
	}
	slices.SortFunc(out, func(a, b LenderRelease) int { return compareLenderKeys(a.LenderKey, b.LenderKey) })
	return out
}

// applyLenderPins keeps every lender release in flight in force on decisions.
func (g *BorrowGovernor) applyLenderPins(decisions []domain.VariantDecision, byVariant map[string]int) {
	for _, e := range g.lenderRel {
		for _, u := range e.Units {
			lowerTo(decisions, byVariant, u.Namespace+"/"+u.Variant, u.To, LenderReleasePinReason)
		}
	}
}

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
	Variant   string `json:"variant"`
	To        int    `json:"to"`
}

// LenderRelease is a release in flight to one waiting lender: the variants it
// lowered, the GPUs it hands back, and when. No further release goes to that
// lender while it exists. It lands once every variant holds no more than its
// target (LandedAt), and clears on the first fresh read of the lender after
// that, or once it outlived the release timeout (lenderTimeout).
type LenderRelease struct {
	LenderKey
	Units      []ReleasedUnit `json:"units"`
	GPUs       int            `json:"gpus"`
	ReleasedAt time.Time      `json:"releasedAt"`
	LandedAt   *time.Time     `json:"landedAt,omitempty"`
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

// lenderReleases hands borrowed capacity back, on every fresh pool whose entry
// opts in (SharedPoolChunk.ReleaseToNonReclaiming), to each lender that waits
// in this read (the raw signal) and that Kueue does not reclaim for, with no
// release to it in flight. In one pass it lowers the borrowing models by
// whole units until the GPUs handed back on the lender's flavor cover what it
// waits for, bounded by what it lent out: min(PendingGPUs, Unused, the pool's
// borrowed figure), so ceil(that / unit) units of a uniform fleet. The last
// unit may be larger than what is left of the need: for whole-node models the
// alternative is releasing nothing.
//
//   - Models are taken lowest priority first: Kueue's priority of the model
//     (its priority class, as the pool read it; a class not read last), then
//     its scaling priority, then the most expensive.
//   - A unit is one replica of a single-role model, or a whole pair at the
//     model's role ratio for a P/D one (yieldUnit, the rule the reclaim yield
//     uses), so a release never leaves a role without its partner.
//   - No variant goes below minReplicas, and no namespace below its
//     guaranteed figure: what it holds past it (gated pods aside), less what
//     this cycle's decisions already lower it by -- a demand fall, a
//     placement-timeout release, or a reclaim yield for a wake -- is all a
//     release may take. So the replica a yield gave back is never given again.
//   - A model with a release in flight (any cause) is skipped, as is one a
//     LowerPriority lender outranks: Kueue preempts it for the lender itself.
//
// Each model released starts its cooldown in the lender's cohort, as a
// preemption does. Nothing is released before the persisted ledger is read
// (BorrowCycle.LedgerUnread): a release from before a restart may still be in
// flight.
func (g *BorrowGovernor) lenderReleases(c BorrowCycle, states map[string]variantRef, decisions []domain.VariantDecision) []BorrowRelease {
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
		for _, l := range ch.Lenders {
			key := LenderKey{Cohort: ch.Cohort, Lender: l.Queue, Flavor: ch.Flavor, Resource: ch.Resource}
			if !l.Waiting || kueue.Reclaims(l.Reclaim, nil, nil) || g.lenderRel[key] != nil {
				continue
			}
			need := min(l.PendingGPUs, l.Unused, ch.Borrowed)
			if need <= 0 {
				continue
			}
			if left == nil {
				left = borrowedLeft(c, decisions)
			}
			out = append(out, g.releaseTo(c, key, l, ch, cohorts, need, plain, decisions, left)...)
		}
	}
	return out
}

// releaseTo lowers, for one waiting lender, the candidate models unit by unit
// until need GPUs on the lender's flavor are handed back (lenderReleases),
// records the release in flight, and returns its records.
func (g *BorrowGovernor) releaseTo(c BorrowCycle, key LenderKey, l kueue.LenderState, ch *SharedPoolChunk,
	cohorts map[string][]string, need int, states map[string]domain.VariantReplicaState,
	decisions []domain.VariantDecision, left map[string]map[string]int) []BorrowRelease {
	released := 0
	touched := map[int]bool{}
	var models []string
	for _, cand := range g.lenderCandidates(c, key, l, ch, cohorts, states, left) {
		gave := false
		for released < need {
			unit := yieldUnit(decisions, cand.req.Namespace, cand.req.ModelID, states, cand.req.Disaggregated)
			gpus, ok := unitTakes(unit, decisions, cand.req, ch, left[cand.req.Namespace])
			if !ok || gpus == 0 {
				break
			}
			for _, i := range slices.Sorted(maps.Keys(unit)) {
				d := &decisions[i]
				gpr := max(1, d.GPUsPerReplica)
				if t, found := accelerator.FindKey(left[d.Namespace], accOf(cand.req, d.VariantName)); found {
					left[d.Namespace][t] -= unit[i] * gpr
				}
				d.TargetReplicas -= unit[i]
				d.GPUsAllocated = d.TargetReplicas * gpr
				lenderStep(d, key)
				touched[i] = true
			}
			released += gpus
			gave = true
		}
		if gave {
			models = append(models, cand.req.Namespace+"/"+cand.req.ModelID)
		}
		if released >= need {
			break
		}
	}
	if released == 0 {
		return nil
	}
	entry := &LenderRelease{LenderKey: key, GPUs: released, ReleasedAt: c.Now}
	out := make([]BorrowRelease, 0, len(touched))
	for _, i := range slices.Sorted(maps.Keys(touched)) {
		d := decisions[i]
		entry.Units = append(entry.Units, ReleasedUnit{Namespace: d.Namespace, Variant: d.VariantName, To: d.TargetReplicas})
		out = append(out, BorrowRelease{Namespace: d.Namespace, ModelID: d.ModelID, Variant: d.VariantName,
			From: d.CurrentReplicas, To: d.TargetReplicas, Cause: ReleaseLenderWaiting, Lender: key})
	}
	slices.SortFunc(entry.Units, func(a, b ReleasedUnit) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Variant, b.Variant))
	})
	g.lenderRel[key] = entry
	for _, m := range models {
		g.markBackoff(m, []string{key.Cohort}, c.Now, true)
	}
	return out
}

// unitTakes is the GPUs a unit hands back on the chunk's flavor, and whether
// it may be released at all: every variant's GPUs must lie within what its
// namespace holds past guaranteed of the variant's type (left).
func unitTakes(unit map[int]int, decisions []domain.VariantDecision, req ModelScalingRequest, ch *SharedPoolChunk,
	left map[string]int) (int, bool) {
	if len(unit) == 0 {
		return 0, false
	}
	per := map[string]int{}
	gpus := 0
	for i, n := range unit {
		d := decisions[i]
		acc := accOf(req, d.VariantName)
		t, found := accelerator.FindKey(left, acc)
		if !found {
			return 0, false
		}
		g := n * max(1, d.GPUsPerReplica)
		per[t] += g
		if ch.serves(acc) {
			gpus += g
		}
	}
	for t, g := range per {
		if g > left[t] {
			return 0, false // never below the guaranteed figure
		}
	}
	return gpus, true
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
// release in flight, and not outranked by a LowerPriority lender.
func (g *BorrowGovernor) lenderCandidates(c BorrowCycle, key LenderKey, l kueue.LenderState, ch *SharedPoolChunk,
	cohorts map[string][]string, states map[string]domain.VariantReplicaState, left map[string]map[string]int) []lenderCandidate {
	var out []lenderCandidate
	for _, req := range c.Requests {
		if !slices.Contains(cohorts[req.Namespace], key.Cohort) || len(left[req.Namespace]) == 0 || g.modelPinned(req) {
			continue
		}
		serves, cost := false, 0.0
		for _, st := range req.VariantStates {
			acc := accOf(req, st.VariantName)
			if t, ok := accelerator.FindKey(left[req.Namespace], acc); ok && left[req.Namespace][t] > 0 && ch.serves(acc) {
				serves = true
			}
			cost += float64(states[req.Namespace+"/"+st.VariantName].GPUsPerReplica) * variantCost(req, st.VariantName)
		}
		if !serves {
			continue
		}
		prio, known := modelKueuePriority(c.Constraints, ch, req)
		if known && l.Reclaim == kueue.ReclaimLowerPriority && kueue.Reclaims(l.Reclaim, l.Priority, &prio) {
			continue // Kueue preempts it for the lender
		}
		cand := lenderCandidate{req: req, priority: math.MaxInt64, cost: cost}
		if known {
			cand.priority = int64(prio)
		}
		out = append(out, cand)
	}
	slices.SortStableFunc(out, func(a, b lenderCandidate) int {
		return cmp.Or(cmp.Compare(a.priority, b.priority), cmp.Compare(a.req.Priority, b.req.Priority),
			cmp.Compare(b.cost, a.cost), strings.Compare(modelKey(a.req), modelKey(b.req)))
	})
	return out
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

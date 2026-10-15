package allocation

import (
	"maps"
	"slices"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/accelerator"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/kueue"
)

// BorrowDraw is borrowed capacity the greedy optimizer ordered for one variant
// in one pass: GPUs drawn from one shared-pool chunk, past the namespace's
// guaranteed figure. The engine keeps it as in flight until the pods exist
// (BorrowInFlight), since Kueue reserves nothing for them before.
type BorrowDraw struct {
	Namespace string
	ModelID   string
	Variant   string
	// Cohort, Flavor and Resource name the chunk drawn from.
	Cohort   string
	Flavor   string
	Resource string
	// GPUs is the borrowed part of the replicas ordered, Replicas how many
	// replicas drew on it.
	GPUs     int
	Replicas int
	// Target is the variant's target after the pass.
	Target int
	// Placement is the flavor's node placement the replicas were fitted to:
	// the in-flight ledger holds their holes on those nodes until the pods
	// hold them (ShapeOfDraws).
	Placement *kueue.FlavorPlacement
}

// nsBorrowLeft is what one namespace may still borrow of one accelerator type
// in this pass.
type nsBorrowLeft struct {
	// cap is the GPUs it may still draw past its guaranteed figure; negative
	// is no cap but the pool's.
	cap int
	// lentOut is guaranteed growth still to debit from the pool.
	lentOut int
	// priority is NamespaceBorrow.HighestPriority, the higher of two entries'.
	priority *int32
}

// borrowState is one optimizer pass's borrowing: what each namespace may still
// draw, the shared pools, and the node view. Draws mutate it, so a pool is
// spent once across every namespace in its cohort and a node hole once across
// every model. Nil when no constraint counts borrowing.
//
// The methods the pass calls whether or not it borrows (reachFor,
// newModelPlacer, drawsByVariant) accept a nil receiver and then answer "no
// borrowing"; the others are reached only through a non-nil state.
type borrowState struct {
	borrow  map[string]map[string]*nsBorrowLeft
	reach   map[string]map[string]int
	chunks  []*SharedPoolChunk
	cohorts map[string][]string
	nodes   *NodeView
	draws   []BorrowDraw
	// blocked reports a variant that may not borrow this pass; nil blocks none.
	blocked func(namespace, modelID, variant string) bool
	// blockedIn reports a model that may not draw on one cohort's pools this
	// pass (its backed-off cooldown there); nil blocks none.
	blockedIn func(namespace, modelID, cohort string) bool
}

// newBorrowState builds the pass's borrowing state, or nil when no constraint
// counts borrowing. nodes is the cycle's node view, nil without a physical
// limiter or a fresh node snapshot.
func newBorrowState(constraints []*ResourceConstraints, nodes *NodeView) *borrowState {
	if !HasBorrowing(constraints) {
		return nil
	}
	bs := &borrowState{borrow: map[string]map[string]*nsBorrowLeft{}, nodes: nodes}
	bs.chunks, bs.cohorts = mergeSharedPools(constraints)
	for _, c := range constraints {
		if c == nil {
			continue
		}
		for ns, perType := range c.NamespaceBorrow {
			if bs.borrow[ns] == nil {
				bs.borrow[ns] = map[string]*nsBorrowLeft{}
			}
			for accType, b := range perType {
				drawable := b.Cap
				if b.Frozen {
					drawable = 0 // its ResourceQuota read failed: nothing new
				}
				prev, seen := bs.borrow[ns][accType]
				if !seen {
					bs.borrow[ns][accType] = &nsBorrowLeft{cap: drawable, lentOut: b.LentOut, priority: b.HighestPriority}
					continue
				}
				// Two Kueue-backed entries hold the namespace: the tighter cap.
				prev.cap = tighterBudget(prev.cap, drawable)
				prev.lentOut = max(prev.lentOut, b.LentOut)
				if b.HighestPriority != nil && (prev.priority == nil || *b.HighestPriority > *prev.priority) {
					prev.priority = b.HighestPriority
				}
			}
		}
	}
	reach := mergeNamespaceConstraints(reachConstraints(constraints))
	bs.reach = map[string]map[string]int{}
	for ns := range bs.borrow {
		if r, ok := reach[ns]; ok {
			bs.reach[ns] = r
		}
	}
	return bs
}

// reachFor is the namespace's budget at its reach (guaranteed plus borrow
// cap), the upper bound a borrowing namespace's model is sized against; nil
// for a namespace that borrows nothing.
func (bs *borrowState) reachFor(ns string) map[string]int {
	if bs == nil {
		return nil
	}
	return bs.reach[ns]
}

// cohortChunks is the chunks a namespace may draw a replica of acc from, in a
// stable order.
func (bs *borrowState) cohortChunks(ns, acc string) []*SharedPoolChunk {
	var out []*SharedPoolChunk
	for _, c := range bs.chunks {
		if slices.Contains(bs.cohorts[ns], c.Cohort) && c.serves(acc) {
			out = append(out, c)
		}
	}
	return out
}

// debitPool takes n GPUs of acc out of the namespace's cohort chunks, first
// chunk first, each clamped at 0. Used for growth that needs no gate -- a
// reclaiming queue growing into nominal it lent out -- so that no other queue
// borrows the same GPUs in the same cycle.
func (bs *borrowState) debitPool(ns, acc string, n int) {
	for _, c := range bs.cohortChunks(ns, acc) {
		if n <= 0 {
			return
		}
		take := min(c.Free, n)
		c.Free -= take
		n -= take
	}
}

// borrowSave is a borrowState taken before a tentative placement.
type borrowSave struct {
	borrow     map[string]map[string]nsBorrowLeft
	free       []int
	coFree     []map[string]int64
	nodes      viewDebits
	draws      int
	guaranteed map[string]int
}

func (bs *borrowState) save(guaranteed map[string]int) borrowSave {
	s := borrowSave{borrow: map[string]map[string]nsBorrowLeft{}, nodes: bs.nodes.saveDebits(), draws: len(bs.draws), guaranteed: maps.Clone(guaranteed)}
	for ns, perType := range bs.borrow {
		s.borrow[ns] = map[string]nsBorrowLeft{}
		for t, b := range perType {
			s.borrow[ns][t] = *b
		}
	}
	for _, c := range bs.chunks {
		s.free = append(s.free, c.Free)
		s.coFree = append(s.coFree, maps.Clone(c.CoFree))
	}
	return s
}

// restore puts the state back as save took it; the node view's debits and the
// owners they were charged to with it.
func (bs *borrowState) restore(s borrowSave, guaranteed map[string]int) {
	for ns, perType := range s.borrow {
		for t, b := range perType {
			*bs.borrow[ns][t] = b
		}
	}
	for i, c := range bs.chunks {
		c.Free = s.free[i]
		c.CoFree = s.coFree[i]
	}
	bs.nodes.restoreDebits(s.nodes)
	bs.draws = bs.draws[:s.draws]
	clear(guaranteed)
	maps.Copy(guaranteed, s.guaranteed)
}

// modelPlacer places one model's replicas, one at a time, against its
// namespace's guaranteed budget first and the shared pools after (B4, B5).
type modelPlacer struct {
	bs        *borrowState
	namespace string
	modelID   string
	// guaranteed is this model's working copy of its namespace's guaranteed
	// budget; nil for an open namespace, where everything is guaranteed.
	guaranteed map[string]int
	// borrows is whether the namespace may borrow at all.
	borrows bool
	targets map[string]int
	// limited is the model's limit tracker: a replica the placer refuses is
	// marked there, as node fit's when the pools held its GPUs and no node
	// did, else as the pools'.
	limited gpuLimitTracker
	// cut is the variant of the last replica refused, and cutByNodes whether
	// node fit refused it.
	cut        string
	cutByNodes bool
	// class is the priority class of the variant being placed
	// (domain.VariantReplicaState.PriorityClass).
	class string
}

// newModelPlacer returns the placer for one model, or nil when the pass counts
// no borrowing, which leaves allocation exactly as it was.
func (bs *borrowState) newModelPlacer(namespace, modelID string, nsBudget map[string]int, targets map[string]int,
	limited gpuLimitTracker) *modelPlacer {
	if bs == nil {
		return nil
	}
	_, borrows := bs.borrow[namespace]
	return &modelPlacer{bs: bs, namespace: namespace, modelID: modelID, guaranteed: maps.Clone(nsBudget), borrows: borrows,
		targets: targets, limited: limited}
}

// placeUnit places one iteration's commit -- kByRole replicas of each picked
// role's variant -- as one unit: all of it, or a smaller unit at the same
// shape, never one role without its partner. It trims kByRole as
// fitPairToBudget does (the role holding the most GPUs gives one replica at a
// time, none below one) until the unit places, and reports false when not
// even one replica of each role places, in which case nothing is committed.
// Every trim marks the variant whose replica was refused: by node fit
// (NodeFitLimiterName) when a pool held its GPUs and no node held it, else
// by the pool.
func (p *modelPlacer) placeUnit(picked []string, kByRole map[string]int, variantByRole map[string]string,
	variants []variantRecord, stateMap map[string]domain.VariantReplicaState) bool {
	gpus := func(role string) int { return gpusPerReplicaFromState(stateMap, variantByRole[role]) }
	for {
		saved := p.bs.save(p.guaranteed)
		p.cut, p.cutByNodes = "", false
		if p.tryUnit(picked, kByRole, variantByRole, variants, stateMap) {
			return true
		}
		p.bs.restore(saved, p.guaranteed)
		p.markCut()
		largest := ""
		for _, role := range picked {
			if kByRole[role] > 1 && (largest == "" || kByRole[role]*gpus(role) > kByRole[largest]*gpus(largest)) {
				largest = role
			}
		}
		if largest == "" {
			return false
		}
		kByRole[largest]--
	}
}

// markCut marks the last refused replica's variant in the model's tracker.
func (p *modelPlacer) markCut() {
	if p.cut == "" {
		return
	}
	if p.cutByNodes {
		p.limited.markNodeFit(p.cut)
	} else {
		p.limited.mark(p.cut)
	}
}

// tryUnit places every replica of the unit, one at a time, and reports
// whether all placed.
//
// A unit of several roles -- a P/D pair -- borrows only whole (B4, B9): either
// no replica of it draws on the shared pools, or every one does. A pair with
// its prefill on guaranteed capacity and its decode borrowed would, once
// Kueue reclaims the decode, leave a prefill serving nothing; with minReplicas
// 1 the placement timeout could not release the prefill either, and with
// minReplicas 0 releasing it would give back guaranteed capacity. Such a unit
// does not place; placeUnit then tries a smaller one.
func (p *modelPlacer) tryUnit(picked []string, kByRole map[string]int, variantByRole map[string]string,
	variants []variantRecord, stateMap map[string]domain.VariantReplicaState) bool {
	var borrowed, guaranteed int
	for _, role := range picked {
		v := variantByRole[role]
		r := p.replica(variants, stateMap, v)
		g := gpusPerReplicaFromState(stateMap, v)
		requests := stateMap[v].ReplicaRequests
		p.class = stateMap[v].PriorityClass // K1: whose LowerPriority lending it may draw on
		for i := 0; i < kByRole[role]; i++ {
			placed, drew := p.placeReplica(v, r, g, requests, p.targets[v]+kByRole[role])
			if !placed {
				return false
			}
			if drew {
				borrowed++
			} else {
				guaranteed++
			}
		}
	}
	if len(picked) > 1 && borrowed > 0 && guaranteed > 0 {
		// A P/D unit straddling guaranteed and borrowed capacity: the
		// guaranteed figure ran out for part of it.
		return p.refuse(variantByRole[picked[len(picked)-1]], false)
	}
	return true
}

// replica is one replica of variant v as the placer fits it: the view's
// shape (pods, tolerations, topology key, as node fit reads them) with the
// pod templates' own placement.
func (p *modelPlacer) replica(variants []variantRecord, stateMap map[string]domain.VariantReplicaState, v string) replicaFit {
	st := stateMap[v]
	return p.bs.nodes.fitOf(ReplicaShape{Variant: p.namespace + "/" + v, Accelerator: accFromVCs(variants, v),
		PodGPUs: st.PodGPUs, GPUs: gpusPerReplicaFromState(stateMap, v), Tolerations: st.Tolerations,
		DomainKey: st.TopologyKey, Placements: st.Placements})
}

// refuse records that variant's replica did not place, and why, and reports
// false. An empty variant records a refusal that marks nothing.
func (p *modelPlacer) refuse(variant string, byNodes bool) bool {
	p.cut, p.cutByNodes = variant, byNodes
	return false
}

// placeReplica places one replica r of g GPUs. Within the guaranteed budget
// it must fit a node of the view, as any replica must when a physical limiter
// bounds the cycle (no node holding it is scarcity), with one exception:
// growth into nominal the namespace lent out is Kueue's to reclaim, by
// preempting the borrowers whose pods fill those holes now, so it places
// whether or not a node shows a hole, and debits the one it fits, if any.
// Growth into lent-out nominal also debits the pool, clamped. Past the
// guaranteed budget the borrowed part must fit the namespace's cap, one chunk
// of its cohorts, and -- with a node view -- a node of that chunk's flavor,
// whole replica at once. Every node must also satisfy the replica's pod
// templates, its tolerations and topology key, and a borrowed replica must
// fit the chunk's free pool of every co-resource its flavor's resource group
// covers (requests, per replica in milli-units), which it is debited. It
// reports whether the replica placed, and whether it drew on a shared pool. A
// replica refused for want of a node, when a chunk's pools held it, is
// recorded as node fit's (refuse).
func (p *modelPlacer) placeReplica(variant string, r replicaFit, g int, requests map[string]int64, target int) (placed, drew bool) {
	bs := p.bs
	acc := r.acc
	left := -1 // unbounded: an open namespace, or an unlimited allowance
	key := ""
	if p.guaranteed != nil {
		key = budgetKey(p.guaranteed, acc)
		left = p.guaranteed[key]
	}
	var borrow *nsBorrowLeft
	if p.borrows {
		if k, ok := accelerator.FindKey(bs.borrow[p.namespace], acc); ok {
			borrow = bs.borrow[p.namespace][k]
		}
	}
	if left < 0 || left >= g {
		reclaim := borrow != nil && borrow.lentOut > 0
		if !bs.nodes.place(r) && !reclaim {
			return p.refuse(variant, true), false
		}
		if left >= 0 {
			p.guaranteed[key] = left - g
		}
		if reclaim {
			n := min(g, borrow.lentOut)
			borrow.lentOut -= n
			bs.debitPool(p.namespace, acc, n)
		}
		return true, false
	}
	if borrow == nil {
		return p.refuse(variant, false), false
	}
	if bs.blocked != nil && bs.blocked(p.namespace, p.modelID, variant) {
		// A borrowing rule holds it back (a placement hold, a cooldown, the
		// physical limiter skipped), not scarcity: its own reason says so,
		// and the decision is not marked limited.
		return p.refuse("", false), false
	}
	need := g - left
	if borrow.cap >= 0 && borrow.cap < need {
		return p.refuse(variant, false), false
	}
	byNodes, open := false, false
	chunks := bs.cohortChunks(p.namespace, acc)
	for _, c := range chunks {
		if bs.blockedIn != nil && bs.blockedIn(p.namespace, p.modelID, c.Cohort) {
			continue // backed off in this cohort (B12)
		}
		open = true
		// K1: a LowerPriority lender's GPUs only for a model it outranks.
		if c.drawable(p.class, borrow.priority) < need || !coFits(c.CoFree, requests) {
			continue // the GPUs, or a co-resource of the flavor's group (B1), would hold it back
		}
		// place commits only when every pod fits, so one call both probes
		// and debits.
		flavor := r
		flavor.flavor = &c.FlavorPlacement
		if !bs.nodes.place(flavor) {
			byNodes = true // the pools held it; no node of the flavor did
			continue
		}
		c.Free -= need
		for res := range c.CoFree {
			c.CoFree[res] -= requests[res]
		}
		if borrow.cap >= 0 {
			borrow.cap -= need
		}
		p.guaranteed[key] = 0
		bs.draws = append(bs.draws, BorrowDraw{
			Namespace: p.namespace, ModelID: p.modelID, Variant: variant,
			Cohort: c.Cohort, Flavor: c.Flavor, Resource: c.Resource,
			GPUs: need, Replicas: 1, Target: target, Placement: &c.FlavorPlacement,
		})
		return true, true
	}
	if len(chunks) > 0 && !open {
		// Backed off in every cohort it could draw on: a rule, not
		// scarcity, as for a blocked variant above.
		return p.refuse("", false), false
	}
	return p.refuse(variant, byNodes), false
}

// coFits reports whether one replica's requests fit a chunk's free pool of
// every co-resource it holds. A co-resource the replica does not request is
// taken as 0 here: a variant whose request of one is unknown is withheld
// before the pass (WithholdByVariants).
func coFits(coFree map[string]int64, requests map[string]int64) bool {
	for res, free := range coFree {
		if requests[res] > free {
			return false
		}
	}
	return true
}

// drawsByVariant folds the pass's draws into one per variant and chunk.
func (bs *borrowState) drawsByVariant() []BorrowDraw {
	if bs == nil || len(bs.draws) == 0 {
		return nil
	}
	type key struct{ ns, variant, cohort, flavor, resource string }
	idx := map[key]int{}
	var out []BorrowDraw
	for _, d := range bs.draws {
		k := key{d.Namespace, d.Variant, d.Cohort, d.Flavor, d.Resource}
		i, ok := idx[k]
		if !ok {
			idx[k] = len(out)
			out = append(out, d)
			continue
		}
		out[i].GPUs += d.GPUs
		out[i].Replicas += d.Replicas
		out[i].Target = max(out[i].Target, d.Target)
	}
	return out
}

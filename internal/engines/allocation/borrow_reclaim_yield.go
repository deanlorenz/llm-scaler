package allocation

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/accelerator"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// ReclaimYieldStepName names the decision step that lowers a borrowing
// model's target for a reclaim this controller ordered (yieldReclaimed).
const ReclaimYieldStepName = "kueue-reclaim"

// ReclaimYieldLogMessage is logged when a pass lowers borrowing models'
// targets for a reclaim it ordered; operators search for it.
const ReclaimYieldLogMessage = "Kueue reclaim: a model of this controller grows into nominal its queue lent out; " +
	"the borrowing models here give those replicas back in the same pass"

// yieldReclaimed lowers, in the pass that ordered it, the targets of the
// managed models whose borrowed GPUs a reclaim takes (KU3). A guaranteed
// replica grown into nominal its queue lent out (a reclaim draw's GPUs) is
// admitted by Kueue preempting what peers borrow. When a peer is a namespace
// this controller manages, holding GPUs past its guaranteed figure in the
// same cohort, its replica is the one preempted, the cautious reading: Kueue
// does not say whose borrowing it takes. Left at its target, the preempted
// replica is recreated and waits gated for capacity that is gone, and the
// pass has ordered more than Kueue can admit.
//
// Per cohort and accelerator, the GPUs reclaimed are taken from the own
// borrowers in namespace order, each up to what it holds past guaranteed,
// counting first what the pass already scales it down by; the rest lowers
// its variants' targets one replica at a time, in model and variant order. A
// reclaim from borrowers this controller does not manage changes nothing.
// It returns, per "namespace/variant", the replicas it took off; nil when
// none.
func (bs *borrowState) yieldReclaimed(decisions []domain.VariantDecision) map[string]int {
	if bs == nil || len(bs.reclaims) == 0 {
		return nil
	}
	var lowered map[string]int
	type key struct{ cohort, acc string }
	need := map[key]int{}
	reclaimant := map[key]map[string]bool{}
	for _, d := range bs.reclaims {
		if d.GPUs <= 0 || d.Cohort == "" {
			continue
		}
		k := key{d.Cohort, d.Accelerator}
		need[k] += d.GPUs
		if reclaimant[k] == nil {
			reclaimant[k] = map[string]bool{}
		}
		reclaimant[k][d.Namespace] = true
	}
	keys := slices.SortedFunc(maps.Keys(need), func(a, b key) int {
		return cmp.Or(strings.Compare(a.cohort, b.cohort), strings.Compare(a.acc, b.acc))
	})
	for _, k := range keys {
		left := need[k]
		for _, ns := range slices.Sorted(maps.Keys(bs.heldPast)) {
			if left <= 0 {
				break
			}
			if reclaimant[k][ns] || !slices.Contains(bs.cohorts[ns], k.cohort) {
				continue
			}
			t, ok := accelerator.FindKey(bs.heldPast[ns], k.acc)
			if !ok || bs.heldPast[ns][t] <= 0 {
				continue
			}
			take := min(left, bs.heldPast[ns][t])
			left -= take
			var own []int
			freed := 0
			for i := range decisions {
				d := &decisions[i]
				if d.Namespace != ns || !accelerator.SameName(d.AcceleratorName, k.acc) {
					continue
				}
				own = append(own, i)
				freed += max(0, d.CurrentReplicas-d.TargetReplicas) * max(1, d.GPUsPerReplica)
			}
			slices.SortFunc(own, func(a, b int) int {
				return cmp.Or(strings.Compare(decisions[a].ModelID, decisions[b].ModelID),
					strings.Compare(decisions[a].VariantName, decisions[b].VariantName))
			})
			for _, i := range own {
				d := &decisions[i]
				for freed < take && d.TargetReplicas > 0 {
					d.TargetReplicas--
					d.GPUsAllocated = d.TargetReplicas * max(1, d.GPUsPerReplica)
					freed += max(1, d.GPUsPerReplica)
					yieldStep(d, k.cohort)
					if lowered == nil {
						lowered = map[string]int{}
					}
					lowered[d.Namespace+"/"+d.VariantName]++
				}
			}
		}
	}
	return lowered
}

// yieldStep records on d that a reclaim in cohort lowered its target.
func yieldStep(d *domain.VariantDecision, cohort string) {
	action := domain.ActionNoChange
	switch {
	case d.TargetReplicas < d.CurrentReplicas:
		action = domain.ActionScaleDown
	case d.TargetReplicas > d.CurrentReplicas:
		action = domain.ActionScaleUp
	}
	d.Action = action
	d.DecisionSteps = append(d.DecisionSteps, domain.DecisionStep{
		Name: ReclaimYieldStepName, Action: action, TargetReplicas: d.TargetReplicas, WasConstrained: true,
		Reason: fmt.Sprintf("a model of this controller reclaims nominal this namespace borrows in cohort %s; "+
			"Kueue preempts a borrowed replica to admit it", cohort),
	})
}

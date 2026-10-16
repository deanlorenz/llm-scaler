package allocation

import (
	"context"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// A scale-from-zero wake that grows into nominal its queue lent out lowers
// this controller's own borrower, as a reclaim the steady pass orders does
// (yieldReclaimed), in 8-GPU units. Queue a lent its 8 to b, whose model y
// holds 16 borrowed. A wake grows x, in a, by one 8-GPU replica into that
// nominal: Kueue preempts one of y's replicas to admit it. The wake decides
// alone; the next steady pass lowers y 2 -> 1. The pass after it reads y at
// 1, x's replica still in flight: the same reclaim, already yielded for, so y
// stays at 1. Before, no pass yielded for a wake's reclaim: y kept its target
// 2, and its preempted replica was recreated to wait gated for GPUs gone.
func TestWakeReclaimLowersOwnBorrowerOnce(t *testing.T) {
	record := NewBorrowInFlight()
	now := time.Now()
	record.RecordWake([]BorrowDraw{{Namespace: "a", ModelID: "x", Variant: "x-v", Cohort: "c", Flavor: "h100",
		Resource: "nvidia.com/gpu", Accelerator: "H100", GPUs: 8, Replicas: 1, Target: 1, Reclaim: true}}, now)
	pass := func(yCurrent int) domain.VariantDecision {
		t.Helper()
		physical := physicalH100(24)
		physical.Pools["H100"] = ResourcePool{Limit: 24, Used: 8 * yCurrent}
		quota := borrowQuota(0, nsBorrow{ns: "a", guaranteed: 8, lentOut: 8}, nsBorrow{ns: "b", guaranteed: 0, used: 8 * yCurrent})
		o := NewGreedyByScoreOptimizer()
		o.Nodes = NewNodeView(map[string]decision.NodeGPU{"n1": h100Node(8, nil), "n2": h100Node(8*(yCurrent-1), nil)})
		o.ReclaimCredit = record.Reclaims()
		o.WakeReclaims = record.TakeWakeReclaims(now)
		ds := o.Optimize(context.Background(), []ModelScalingRequest{borrowModel("b", "y", 8, yCurrent, 0, nil)},
			DeductReclaimed([]*ResourceConstraints{physical, quota}, record.Unreserved(now).InFlight))
		return decisionOf(t, ds, "y")
	}
	y := pass(2)
	if y.TargetReplicas != 1 {
		t.Fatalf("pass 1: y's target is %d, want 1: Kueue preempts one borrowed 8-GPU replica for the wake", y.TargetReplicas)
	}
	if steps := y.DecisionSteps; len(steps) == 0 || steps[len(steps)-1].Name != ReclaimYieldStepName {
		t.Fatalf("y's decision does not carry the %s step: %+v", ReclaimYieldStepName, steps)
	}
	if y := pass(1); y.TargetReplicas != 1 {
		t.Fatalf("pass 2: y's target is %d, want 1: the wake's reclaim was yielded for once already", y.TargetReplicas)
	}
}

// The record hands a wake's reclaim to one pass only, and forgets one older
// than InFlightTTL that no pass took. A wake's draw on the cohort's pool is
// no reclaim and is not queued.
func TestTakeWakeReclaims(t *testing.T) {
	record := NewBorrowInFlight()
	now := time.Now()
	record.RecordWake([]BorrowDraw{
		{Namespace: "a", Variant: "x-v", Cohort: "c", GPUs: 8, Replicas: 1, Reclaim: true},
		{Namespace: "a", Variant: "w-v", Cohort: "c", GPUs: 8, Replicas: 1},
	}, now)
	if got := record.TakeWakeReclaims(now); len(got) != 1 || got[0].Variant != "x-v" || got[0].GPUs != 8 {
		t.Fatalf("first take = %+v, want x-v's 8-GPU reclaim alone", got)
	}
	if got := record.TakeWakeReclaims(now); got != nil {
		t.Fatalf("second take = %+v, want nothing: each reclaim is yielded for once", got)
	}
	record.RecordWake([]BorrowDraw{{Namespace: "a", Variant: "z-v", Cohort: "c", GPUs: 8, Replicas: 1, Reclaim: true}}, now)
	if got := record.TakeWakeReclaims(now.Add(InFlightTTL + time.Second)); got != nil {
		t.Fatalf("take past InFlightTTL = %+v, want nothing", got)
	}
}

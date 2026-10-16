package allocation

import (
	"context"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// KU3: a reclaim from a model this controller manages. Queue a (Any, nominal
// 8) lent its 8 to queue b, whose model y -- managed here too -- holds them
// borrowed on the one node. x in a grows into that nominal: Kueue preempts
// y's borrowed replica to admit it. The pass must lower y's target by the
// same replica, so that what it orders, x's 8 and y's, never exceeds the 8
// Kueue can admit. Before, y kept its target 1: its replica was recreated and
// waited gated for capacity that was gone. Control: b's borrower is not
// managed here, and x's reclaim lowers nothing.
func TestReclaimFromOwnBorrowerLowersItsTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed bool
		wantY   int
	}{
		{"own borrower: y yields its replica", true, 0},
		{"control: borrower not managed here", false, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			physical := physicalH100(8)
			physical.Pools["H100"] = ResourcePool{Limit: 8, Used: 8}
			nss := []nsBorrow{{ns: "a", guaranteed: 8, lentOut: 8}}
			reqs := []ModelScalingRequest{borrowModel("a", "x", 8, 0, 1, nil)}
			if tc.managed {
				nss = append(nss, nsBorrow{ns: "b", guaranteed: 0, used: 8})
				reqs = append(reqs, borrowModel("b", "y", 8, 1, 0, nil))
			}
			o := NewGreedyByScoreOptimizer()
			o.Nodes = NewNodeView(map[string]decision.NodeGPU{"n1": h100Node(8, nil)})
			ds := o.Optimize(context.Background(), reqs, []*ResourceConstraints{physical, borrowQuota(0, nss...)})
			if got := addedGPUs(ds)["x"]; got != 8 {
				t.Fatalf("x grew %d GPUs, want 8: Kueue preempts the borrower for it", got)
			}
			if !tc.managed {
				return
			}
			y := decisionOf(t, ds, "y")
			if y.TargetReplicas != tc.wantY {
				t.Fatalf("y's target is %d, want %d: Kueue preempts its borrowed replica for x", y.TargetReplicas, tc.wantY)
			}
			total := 0
			for _, d := range ds {
				total += d.TargetReplicas * d.GPUsPerReplica
			}
			if total > 8 {
				t.Fatalf("the pass orders %d GPUs in all, more than the 8 Kueue can admit", total)
			}
			if steps := y.DecisionSteps; len(steps) == 0 || steps[len(steps)-1].Name != ReclaimYieldStepName ||
				y.Action != domain.ActionScaleDown {
				t.Fatalf("y's decision does not say why: action %q, steps %+v", y.Action, steps)
			}
		})
	}
}

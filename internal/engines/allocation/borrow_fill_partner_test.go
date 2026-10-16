package allocation

import (
	"context"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// pdAt is a P/D model in ns "b", 8 GPUs per replica, at current replicas of
// each role, whose demand asks for two more pairs.
func pdAt(current int) ModelScalingRequest {
	min := 0
	return withSatEntry(&satEntryFixture{
		RequiredCapacity: float64(current+2) * 20000,
		RoleCapacities: map[string]domain.RoleCapacity{
			"prefill": {Role: "prefill", RequiredCapacity: float64(current+2) * 10000, TotalDemand: float64(current+2) * 10000},
			"decode":  {Role: "decode", RequiredCapacity: float64(current+2) * 10000, TotalDemand: float64(current+2) * 10000},
		},
		VariantCapacities: []vcFixture{
			{VariantName: "p", AcceleratorName: "H100", Cost: 5, Role: "prefill", ReplicaCount: current, PerReplicaCapacity: 10000},
			{VariantName: "d", AcceleratorName: "H100", Cost: 5, Role: "decode", ReplicaCount: current, PerReplicaCapacity: 10000},
		},
	}, ModelScalingRequest{ModelID: "pd", Namespace: "b", Disaggregated: true, Priority: 1,
		VariantStates: []domain.VariantReplicaState{
			{VariantName: "p", CurrentReplicas: current, GPUsPerReplica: 8, Role: "prefill", MinReplicas: &min},
			{VariantName: "d", CurrentReplicas: current, GPUsPerReplica: 8, Role: "decode", MinReplicas: &min},
		}})
}

// Review N-m2: fillGuaranteed put one role alone on an odd guaranteed
// remainder even when the model had no replica of the other role that is not
// borrowed. A P/D model at zero, 8 guaranteed GPUs, 16 in the pool: the
// prefill took the guaranteed 8 and a borrowed pair followed. Once Kueue
// reclaims the pair, the prefill on guaranteed capacity serves nothing. A
// role fills the remainder alone only while the other role has a replica
// that is not borrowed. Control: the model already runs a pair on guaranteed
// capacity, and the remainder still fills (W2).
func TestGuaranteedFillNeedsAPartnerOfTheOtherRole(t *testing.T) {
	added := func(current, guaranteed, used int) map[string]int {
		o := NewGreedyByScoreOptimizer()
		ds := o.Optimize(context.Background(), []ModelScalingRequest{pdAt(current)},
			[]*ResourceConstraints{physicalH100(64), borrowQuota(16, nsBorrow{ns: "b", guaranteed: guaranteed, used: used,
				cap: config.QuotaUnlimited})})
		out := map[string]int{}
		for _, d := range ds {
			out[d.VariantName] += (d.TargetReplicas - d.CurrentReplicas) * d.GPUsPerReplica
		}
		return out
	}
	got := added(0, 8, 0)
	if got["p"] != got["d"] {
		t.Fatalf("a model at zero grew prefill %d and decode %d GPUs: one role sits alone on guaranteed capacity, "+
			"its only partner borrowed", got["p"], got["d"])
	}
	got = added(1, 24, 16)
	if got["p"]+got["d"] != 24 {
		t.Fatalf("control: with a guaranteed pair running, the remainder fills and a pair borrows whole: grew %v, want 24 in all", got)
	}
}

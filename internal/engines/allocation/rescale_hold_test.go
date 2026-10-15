package allocation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// pdRescaleRequest is a P/D model with four one-GPU replicas per role on
// A100, prefill demand worth one replica and decode demand worth six, and the
// composite holds holds.
func pdRescaleRequest(holds map[string]RoleHold) ModelScalingRequest {
	return ModelScalingRequest{
		ModelID:   "m",
		Namespace: "ns",
		Priority:  1,
		CompositeSignal: NamedAnalyzerResult{
			Name: CompositeSignalName,
			Live: true,
			Result: &domain.AnalyzerResult{
				ModelID:    "m",
				Namespace:  "ns",
				RoleDemand: map[string]float64{domain.RolePrefill: 100, domain.RoleDecode: 600},
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "p", Role: domain.RolePrefill, ReplicaCount: 4, PerReplicaCapacity: 100},
					{VariantName: "d", Role: domain.RoleDecode, ReplicaCount: 4, PerReplicaCapacity: 100},
				},
			},
			RoleCapacities: map[string]domain.RoleCapacity{
				domain.RolePrefill: {Role: domain.RolePrefill, TotalDemand: 100, TotalSupply: 400},
				domain.RoleDecode:  {Role: domain.RoleDecode, TotalDemand: 600, TotalSupply: 400},
			},
			Holds: holds,
		},
		VariantStates: []domain.VariantReplicaState{
			{VariantName: "p", Role: domain.RolePrefill, CurrentReplicas: 4, GPUsPerReplica: 1},
			{VariantName: "d", Role: domain.RoleDecode, CurrentReplicas: 4, GPUsPerReplica: 1},
		},
		Variants: []domain.VariantMetadata{
			{VariantName: "p", Role: domain.RolePrefill, AcceleratorName: "A100", Cost: 1},
			{VariantName: "d", Role: domain.RoleDecode, AcceleratorName: "A100", Cost: 1},
		},
		Disaggregated: true,
	}
}

func rescaleTargets(req ModelScalingRequest, targetGPUs, free int) map[string]int {
	out := map[string]int{}
	for _, d := range NewGreedyByScoreOptimizer().rescaleModelDecisions(context.Background(), req, nil, "A100", targetGPUs, &free) {
		out[d.VariantName] = d.TargetReplicas
	}
	return out
}

// TestRescaleKeepsAHeldRole: rescale splits a model's GPUs by role demand and
// reclaims or fills each role toward its share. A role the composite holds is
// neither reclaimed nor filled: its demand is a figure the policy does not size
// on, and with it rescale shed prefill to fund decode.
func TestRescaleKeepsAHeldRole(t *testing.T) {
	free := rescaleTargets(pdRescaleRequest(nil), 8, 0)
	assert.Less(t, free["p"], 4, "unheld, prefill is reclaimed toward its share")

	both := map[string]RoleHold{domain.RolePrefill: {NoScaleUp: true, NoScaleDown: true}}
	assert.Equal(t, 4, rescaleTargets(pdRescaleRequest(both), 8, 0)["p"])

	down := map[string]RoleHold{domain.RolePrefill: {NoScaleDown: true}}
	assert.Equal(t, 4, rescaleTargets(pdRescaleRequest(down), 8, 0)["p"])
}

// TestRescaleFreezesAModelWithAPinnedRole: a role held both ways freezes the
// model's rescale, as the share optimizer freezes it. Split around it, decode
// was filled toward 7 of a target of 8 while prefill kept its 4, and a target
// below prefill's 4 cut decode to its floor.
func TestRescaleFreezesAModelWithAPinnedRole(t *testing.T) {
	both := map[string]RoleHold{domain.RolePrefill: {NoScaleUp: true, NoScaleDown: true}}
	for _, tc := range []struct{ target, free int }{{8, 8}, {3, 0}, {16, 8}} {
		got := rescaleTargets(pdRescaleRequest(both), tc.target, tc.free)
		assert.Equal(t, 4, got["p"], "target %d", tc.target)
		assert.Equal(t, 4, got["d"], "target %d", tc.target)
	}
}

// TestRescaleInputHoldsAPinnedModelAtWhatItHas: a model rescale freezes is
// given exactly its GPUs in the water-fill, so the rest of the budget goes to
// the others. Weighed on its demand it was set a share it never took, and the
// GPUs sat idle while another model stayed short.
func TestRescaleInputHoldsAPinnedModelAtWhatItHas(t *testing.T) {
	free, _ := rescaleInputsForGroup([]ModelScalingRequest{pdRescaleRequest(nil)}, "A100", 16)
	assert.Equal(t, 7, free[0].CapGPUs, "unheld: prefill 1 + decode 6 from demand")

	both := map[string]RoleHold{domain.RolePrefill: {NoScaleUp: true, NoScaleDown: true}}
	in, sum := rescaleInputsForGroup([]ModelScalingRequest{pdRescaleRequest(both)}, "A100", 16)
	assert.Equal(t, 8, in[0].FloorGPUs)
	assert.Equal(t, 8, in[0].CapGPUs)
	assert.Equal(t, 8, sum, "it contends for what it holds")
}

// TestRescaleDoesNotFillAHeldRole: a role held against growth is not filled,
// though its share and the free GPUs would allow it.
func TestRescaleDoesNotFillAHeldRole(t *testing.T) {
	req := pdRescaleRequest(nil)
	assert.Greater(t, rescaleTargets(req, 16, 8)["d"], 4, "unheld, decode fills")

	up := map[string]RoleHold{domain.RoleDecode: {NoScaleUp: true}}
	assert.Equal(t, 4, rescaleTargets(pdRescaleRequest(up), 16, 8)["d"])
}

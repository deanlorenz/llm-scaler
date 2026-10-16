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

// TestRescaleWeighsAOneWayHeldModelOnDemand: a role held one way does not pin
// the model. Its water-fill input still follows demand, and its other role
// still moves.
func TestRescaleWeighsAOneWayHeldModelOnDemand(t *testing.T) {
	up := map[string]RoleHold{domain.RolePrefill: {NoScaleUp: true}}
	in, _ := rescaleInputsForGroup([]ModelScalingRequest{pdRescaleRequest(up)}, "A100", 16)
	assert.Equal(t, 7, in[0].CapGPUs, "prefill 1 + decode 6 from demand, not the 8 it holds")
	assert.Greater(t, rescaleTargets(pdRescaleRequest(up), 16, 8)["d"], 4, "decode still fills")
}

// TestRoundFillsToReplicas: on 8-GPU pods a model given 8 more GPUs, split
// +1 and +7 between its roles, adds one decode pod; split as GPUs, neither
// role reached a pod and the 8 GPUs stayed free every cycle.
func TestRoundFillsToReplicas(t *testing.T) {
	req := pdRescaleRequest(nil)
	for i := range req.VariantStates {
		req.VariantStates[i].GPUsPerReplica = 8
	}
	records := buildVariantRecords(req, req.CompositeSignal.Result)
	stateMap := buildStateMap(req.VariantStates)
	roles := []string{domain.RoleDecode, domain.RolePrefill}
	cur := map[string]int{domain.RolePrefill: 16, domain.RoleDecode: 16}

	tgt := map[string]int{domain.RolePrefill: 17, domain.RoleDecode: 23}
	roundFillsToReplicas(records, stateMap, "A100", roles, tgt, cur, initTargets(req.VariantStates))
	assert.Equal(t, map[string]int{domain.RolePrefill: 16, domain.RoleDecode: 24}, tgt)

	// A cut too small for any pod stays unspent rather than overshooting.
	tgt = map[string]int{domain.RolePrefill: 19, domain.RoleDecode: 19}
	roundFillsToReplicas(records, stateMap, "A100", roles, tgt, cur, initTargets(req.VariantStates))
	assert.Equal(t, map[string]int{domain.RolePrefill: 16, domain.RoleDecode: 16}, tgt)

	// One-GPU replicas are left as split.
	one := pdRescaleRequest(nil)
	tgt = map[string]int{domain.RolePrefill: 5, domain.RoleDecode: 7}
	roundFillsToReplicas(buildVariantRecords(one, one.CompositeSignal.Result), buildStateMap(one.VariantStates),
		"A100", roles, tgt, map[string]int{domain.RolePrefill: 4, domain.RoleDecode: 4}, initTargets(one.VariantStates))
	assert.Equal(t, map[string]int{domain.RolePrefill: 5, domain.RoleDecode: 7}, tgt)
}

// TestRoundFillsToReplicasUsesTheVariantThatFills: the share is rounded to
// what fillRole places, not to the cheapest variant's pod. With the 8-GPU
// variant at its maximum and a 4-GPU one free, +4 is one 4-GPU replica;
// rounded on the 8-GPU pod it was nothing.
func TestRoundFillsToReplicasUsesTheVariantThatFills(t *testing.T) {
	one := 1
	req := ModelScalingRequest{
		ModelID: "m", Namespace: "ns",
		CompositeSignal: NamedAnalyzerResult{Result: &domain.AnalyzerResult{VariantCapacities: []domain.VariantCapacity{
			{VariantName: "d8", Role: domain.RoleDecode, ReplicaCount: 1, PerReplicaCapacity: 800},
			{VariantName: "d4", Role: domain.RoleDecode, ReplicaCount: 1, PerReplicaCapacity: 300},
		}}},
		VariantStates: []domain.VariantReplicaState{
			{VariantName: "d8", Role: domain.RoleDecode, CurrentReplicas: 1, GPUsPerReplica: 8, MaxReplicas: &one},
			{VariantName: "d4", Role: domain.RoleDecode, CurrentReplicas: 1, GPUsPerReplica: 4},
		},
		Variants: []domain.VariantMetadata{
			{VariantName: "d8", Role: domain.RoleDecode, AcceleratorName: "A100", Cost: 1},
			{VariantName: "d4", Role: domain.RoleDecode, AcceleratorName: "A100", Cost: 1},
		},
	}
	records := buildVariantRecords(req, req.CompositeSignal.Result)
	tgt := map[string]int{domain.RoleDecode: 16}
	roundFillsToReplicas(records, buildStateMap(req.VariantStates), "A100", []string{domain.RoleDecode},
		tgt, map[string]int{domain.RoleDecode: 12}, initTargets(req.VariantStates))
	assert.Equal(t, 16, tgt[domain.RoleDecode])
}

// TestRescaleReclaimsNoMoreThanTheReleaseBound: a role is reclaimed only as
// far as the composite's release bound allows, and the water-fill reserves
// what it keeps. With a bound worth 150 at 100 per GPU, prefill sheds at most
// one of its four GPUs, not the three its demand share would.
func TestRescaleReclaimsNoMoreThanTheReleaseBound(t *testing.T) {
	req := pdRescaleRequest(nil)
	assert.Equal(t, 1, rescaleTargets(req, 8, 0)["p"], "setup: unbounded, prefill goes to its share")

	req.CompositeSignal.ReleaseBound = map[string]float64{domain.RolePrefill: 150}
	assert.GreaterOrEqual(t, rescaleTargets(req, 8, 0)["p"], 3)

	in, _ := rescaleInputsForGroup([]ModelScalingRequest{req}, "A100", 16)
	assert.Equal(t, 3, in[0].FloorGPUs, "the water-fill keeps the 3 the bound leaves prefill")

	held := pdRescaleRequest(map[string]RoleHold{domain.RolePrefill: {NoScaleDown: true}})
	in, _ = rescaleInputsForGroup([]ModelScalingRequest{held}, "A100", 16)
	assert.Equal(t, 4, in[0].FloorGPUs, "a role that may not shrink keeps all 4")
}

// TestRescaleFillsWholePods: rescale rounds a role's share to whole pods.
// With 8-GPU pods, 2 each, demand weighing the 40-GPU target 17:23, the
// split was +1 prefill and +7 decode, neither a pod, and the 8 free GPUs went
// unused every cycle; rounded, decode gets one pod.
func TestRescaleFillsWholePods(t *testing.T) {
	req := pdRescaleRequest(nil)
	req.CompositeSignal.Result.RoleDemand = map[string]float64{domain.RolePrefill: 1700, domain.RoleDecode: 2300}
	req.CompositeSignal.RoleCapacities = map[string]domain.RoleCapacity{
		domain.RolePrefill: {Role: domain.RolePrefill, TotalDemand: 1700, TotalSupply: 200},
		domain.RoleDecode:  {Role: domain.RoleDecode, TotalDemand: 2300, TotalSupply: 200},
	}
	for i := range req.VariantStates {
		req.VariantStates[i].GPUsPerReplica, req.VariantStates[i].CurrentReplicas = 8, 2
	}
	for i := range req.CompositeSignal.Result.VariantCapacities {
		req.CompositeSignal.Result.VariantCapacities[i].ReplicaCount = 2
	}
	got := rescaleTargets(req, 40, 8)
	assert.Equal(t, 2, got["p"])
	assert.Equal(t, 3, got["d"], "8 free GPUs buy one decode pod")
}

// TestRescaleDoesNotFillAHeldRole: a role held against growth is not filled,
// though its share and the free GPUs would allow it.
func TestRescaleDoesNotFillAHeldRole(t *testing.T) {
	req := pdRescaleRequest(nil)
	assert.Greater(t, rescaleTargets(req, 16, 8)["d"], 4, "unheld, decode fills")

	up := map[string]RoleHold{domain.RoleDecode: {NoScaleUp: true}}
	assert.Equal(t, 4, rescaleTargets(pdRescaleRequest(up), 16, 8)["d"])
}

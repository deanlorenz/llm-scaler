package allocation

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// pdWholeNodeRequest is pdRescaleRequest on 8-GPU pods: pods replicas per
// role, prefill demand prefill and decode demand decode at 100 per replica.
func pdWholeNodeRequest(pods int, prefill, decode float64) ModelScalingRequest {
	req := pdRescaleRequest(nil)
	req.CompositeSignal.Result.RoleDemand = map[string]float64{domain.RolePrefill: prefill, domain.RoleDecode: decode}
	supply := float64(pods) * 100
	req.CompositeSignal.RoleCapacities = map[string]domain.RoleCapacity{
		domain.RolePrefill: {Role: domain.RolePrefill, TotalDemand: prefill, TotalSupply: supply},
		domain.RoleDecode:  {Role: domain.RoleDecode, TotalDemand: decode, TotalSupply: supply},
	}
	for i := range req.CompositeSignal.Result.VariantCapacities {
		req.CompositeSignal.Result.VariantCapacities[i].ReplicaCount = pods
	}
	for i := range req.VariantStates {
		req.VariantStates[i].GPUsPerReplica, req.VariantStates[i].CurrentReplicas = 8, pods
	}
	return req
}

// TestRescaleCapsARoleAtItsMaxReplicas: a role at its maxReplicas is not
// given a share it cannot fill. Prefill is at its max of two 8-GPU pods with
// demand for 70, decode at two with demand for 10. A model target of 40 split
// by demand was 35 for prefill and 5 for decode, so decode was cut from 2 pods
// to 1 while prefill could not grow and all 64 free GPUs stayed free. Capped
// at its 16, prefill leaves decode 24: three pods.
func TestRescaleCapsARoleAtItsMaxReplicas(t *testing.T) {
	two := 2
	req := pdWholeNodeRequest(2, 7000, 1000)
	req.VariantStates[0].MaxReplicas = &two
	got := rescaleTargets(req, 40, 64)
	assert.Equal(t, 2, got["p"], "prefill is at its max")
	assert.Equal(t, 3, got["d"], "decode takes what prefill cannot hold")
}

// TestRescaleReclaimsWholePods: a model's cut is shed as whole pods. On 8-GPU
// pods, two per role, a target of 24 from 32 split equally is a cut of 4 from
// each role; each cut alone is less than a pod, so nothing was shed and the
// model kept 32. Summed, the cut is one pod.
func TestRescaleReclaimsWholePods(t *testing.T) {
	got := rescaleTargets(pdWholeNodeRequest(2, 100, 100), 24, 0)
	assert.Equal(t, 3, got["p"]+got["d"], "one pod of the 8-GPU cut is shed")
	assert.GreaterOrEqual(t, got["p"], 1)
	assert.GreaterOrEqual(t, got["d"], 1)
}

// TestRescaleReclaimsWholePodsFromBothRoles: on 8-GPU pods, four per role, a
// target of 32 from 64 weighed 1:2 by demand is 11 for prefill and 21 for
// decode, a cut of 21 and 11. Each cut rounded down alone shed 2 prefill pods
// and 1 decode pod and the model kept 40; the 5 and 3 left over make one more
// pod, taken from prefill, whose cut is the larger.
func TestRescaleReclaimsWholePodsFromBothRoles(t *testing.T) {
	got := rescaleTargets(pdWholeNodeRequest(4, 100, 200), 32, 0)
	assert.Equal(t, 1, got["p"])
	assert.Equal(t, 3, got["d"])
}

// TestRescaleReclaimsNoPodBelowAFloor: the rounded cut never takes a role
// below its floor. Prefill, with no demand, would be cut furthest, but its
// floor is its two 8-GPU pods -- minReplicas 2, or a scale-down hold -- so at
// a target of 24 the one pod the cut buys comes from decode.
func TestRescaleReclaimsNoPodBelowAFloor(t *testing.T) {
	two := 2
	req := pdWholeNodeRequest(2, 0, 100)
	req.VariantStates[0].MinReplicas = &two
	got := rescaleTargets(req, 24, 0)
	assert.Equal(t, 2, got["p"], "prefill's minReplicas stops its pod")
	assert.Equal(t, 1, got["d"], "the pod comes from decode")

	req = pdWholeNodeRequest(2, 0, 100)
	req.CompositeSignal.Holds = map[string]RoleHold{domain.RolePrefill: {NoScaleDown: true}}
	got = rescaleTargets(req, 24, 0)
	assert.Equal(t, 2, got["p"], "a role that may not shrink keeps its pods")
	assert.Equal(t, 1, got["d"], "the pod comes from decode")
}

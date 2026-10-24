package allocation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// aggRescaleRequest is an aggregated model with variant "a" on A100, two
// one-GPU replicas at 100 each, and demand.
func aggRescaleRequest(id string, demand float64) ModelScalingRequest {
	return ModelScalingRequest{
		ModelID:   id,
		Namespace: "ns",
		Priority:  1,
		CompositeSignal: NamedAnalyzerResult{
			Name: CompositeSignalName,
			Live: true,
			Result: &domain.AnalyzerResult{
				ModelID: id, Namespace: "ns", TotalDemand: demand,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: id + "-a", ReplicaCount: 2, PerReplicaCapacity: 100},
				},
			},
		},
		VariantStates: []domain.VariantReplicaState{
			{VariantName: id + "-a", CurrentReplicas: 2, GPUsPerReplica: 1},
		},
		Variants: []domain.VariantMetadata{
			{VariantName: id + "-a", AcceleratorName: "A100", Cost: 2},
		},
	}
}

// withUnnamedVariant adds variant "x" to req: no accelerator name, cheaper
// than "a", two one-GPU replicas at 100 each.
func withUnnamedVariant(req ModelScalingRequest) ModelScalingRequest {
	r := req.CompositeSignal.Result
	r.VariantCapacities = append(r.VariantCapacities,
		domain.VariantCapacity{VariantName: "x", ReplicaCount: 2, PerReplicaCapacity: 100})
	req.VariantStates = append(req.VariantStates, domain.VariantReplicaState{VariantName: "x", CurrentReplicas: 2, GPUsPerReplica: 1})
	req.Variants = append(req.Variants, domain.VariantMetadata{VariantName: "x", Cost: 1})
	return req
}

// TestRescaleLeavesAVariantWithNoAcceleratorAlone (#223): rescale counts only
// the GPUs on the model's accelerator type, so it moves only those variants.
// The cheaper variant with no accelerator name was filled first, with GPUs
// no budget counted, and was reclaimed to pay a cut it was never part of.
func TestRescaleLeavesAVariantWithNoAcceleratorAlone(t *testing.T) {
	req := withUnnamedVariant(aggRescaleRequest("m", 1000))
	_, ok := singleAccType(buildVariantRecords(req, req.CompositeSignal.Result))
	require.True(t, ok, "setup: the model passes the single-type gate")

	grow := rescaleTargets(req, 5, 3)
	assert.Equal(t, 2, grow["x"], "x is not filled")
	assert.Equal(t, 5, grow["m-a"], "the fill goes to the variant the budget counts")

	shrink := rescaleTargets(aggCut(req), 1, 0)
	assert.Equal(t, 2, shrink["x"], "x is not reclaimed")
	assert.Equal(t, 1, shrink["m-a"])
}

// aggCut is req with no demand, so rescale reclaims toward its target.
func aggCut(req ModelScalingRequest) ModelScalingRequest {
	req.CompositeSignal.Result.TotalDemand = 0
	return req
}

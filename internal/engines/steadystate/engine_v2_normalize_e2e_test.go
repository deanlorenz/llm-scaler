package steadystate

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
)

// This spec exercises the REAL pipeline end to end — collectV2ModelRequest
// (which runs runAnalyzersAndScore -> buildNamedResult -> normalizeToCompositeUnits)
// followed by the optimizer — with NONZERO demand. Every existing
// normalizeToCompositeUnits unit test calls the function directly and never
// checks RequiredCapacity/Remaining, and the two existing collectV2ModelRequest
// tests use a zero-value AnalyzerResult (zero demand), so none of them could
// have caught a bug where RequiredCapacity/SpareCapacity/Remaining/Spare are
// left in raw (token-scale) units after PerReplicaCapacity has already been
// normalized to a coverage fraction. This spec is that missing case.
//
// The numbers are chosen so the pre-fix and post-fix outcomes are wildly
// different (4 replicas vs. tens of thousands), so a regression here cannot be
// mistaken for rounding noise:
//
//	TotalDemand = 8000, PerReplicaCapacity = 2000, ReplicaCount = 1
//	scaleUp = 0.85, scaleDown = 0.70
//	raw RequiredCapacity = 8000/0.85 - 2000 = 7411.76...
//	normalized PRC       = 2000/8000 = 0.25
//	normalized RC (fix)  = 7411.76 / 8000 = 0.9264...  -> ceil(0.9264/0.25) = 4 additional replicas
//	un-normalized RC (bug) = 7411.76           -> ceil(7411.76/0.25) = 29648 additional replicas
var _ = Describe("normalizeToCompositeUnits: real-pipeline regression with nonzero demand (CT6)", func() {
	It("sizes a scale-up correctly through collectV2ModelRequest -> CostAwareOptimizer.Optimize", func() {
		fakeSat := &fakeAnalyzerWithResult{
			analyzerName: domain.SaturationAnalyzerName,
			result: &domain.AnalyzerResult{
				TotalDemand: 8000,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, ReplicaCount: 1, PerReplicaCapacity: 2000},
				},
			},
		}
		e := &Engine{
			saturationV2Analyzer: fakeSat,
			analyzersSnapshot: []analyzerEntry{
				{name: domain.SaturationAnalyzerName, analyzer: fakeSat},
			},
			started: true,
		}
		cfg := config.ScalingPolicy{
			ScaleUpThreshold:  0.85,
			ScaleDownBoundary: 0.70,
		}
		variantStates := []domain.VariantReplicaState{
			{VariantName: "v1", Role: domain.RoleBoth, CurrentReplicas: 1},
		}
		variantMetadata := []domain.VariantMetadata{
			{VariantName: "v1", Role: domain.RoleBoth, Cost: 1.0},
		}

		req, err := e.collectV2ModelRequest(context.Background(), "m", "ns", nil, cfg,
			variantStates, variantMetadata, nil, nil, nil, 0)
		Expect(err).NotTo(HaveOccurred())

		// The composite handed to the optimizer must already be in coverage units:
		// PerReplicaCapacity and RequiredCapacity divided by the SAME raw demand.
		composite := req.CompositeSignal
		Expect(composite.Result.VariantCapacities[0].PerReplicaCapacity).To(BeNumerically("~", 0.25, 1e-9))
		Expect(composite.RequiredCapacity).To(BeNumerically("~", 0.926470588, 1e-6),
			"RequiredCapacity must be normalized by the same raw demand as PerReplicaCapacity")
		Expect(composite.Remaining).To(BeNumerically("~", 0.926470588, 1e-6),
			"Remaining (buildNamedResult's working copy of RequiredCapacity) must also be normalized")

		optimizer := allocation.NewCostAwareOptimizer()
		decisions := optimizer.Optimize(context.Background(), []allocation.ModelScalingRequest{*req}, nil)

		dm := decisionsByVariant(decisions)
		Expect(dm).To(HaveKey("v1"))
		// Correct sizing: current 1 + ceil(0.9264.../0.25) = 1 + 4 = 5.
		// Pre-fix, RequiredCapacity stays at the raw 7411.76..., so the optimizer
		// computes ceil(7411.76/0.25) = 29648 additional replicas (target 29649) —
		// off by roughly 1/0.25 = 4x from the already-wrong intermediate, and by
		// four orders of magnitude from the correct answer. Either outcome fails
		// this assertion against the pre-fix code.
		Expect(dm["v1"].TargetReplicas).To(Equal(5),
			"expected current(1) + ceil(RC_normalized/PRC_normalized)=4; a non-normalized RequiredCapacity "+
				"produces a wildly larger target instead")
	})
})

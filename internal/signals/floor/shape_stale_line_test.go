package floor

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// A figure derived from a line that depends on the request shape -- an
// aggregated variant's, issue #432 -- while the shape changes describes the
// shape that left. It is routed as a borrowed line is: it may hold the fleet
// and must not grow it, with or without the stale-shape flag, and the hold
// names it.
var _ = Describe("Estimate with a shape-stale line", func() {
	variants := []domain.VariantCapacity{{
		VariantName: "v", Role: domain.RoleBoth, ReplicaCount: 1, PerReplicaCapacity: float64(runK1),
	}}
	derived := func(stale bool) capacity.ReplicaCapacity {
		return capacity.ReplicaCapacity{
			VariantName:                   "v",
			SaturatedThroughput:           runMu,
			SaturatedThroughputSamples:    MinThroughputSamplesToOrder,
			SaturatedThroughputDerived:    true,
			SaturatedThroughputShapeStale: stale,
		}
	}

	It("orders on the line while it is not stale", func() {
		f := Estimate(runLambda, []capacity.ReplicaCapacity{derived(false)},
			variants, nil, BacklogDrainSeconds, 0.85, false, 0, nil)
		Expect(f.Terms[domain.RoleBoth].Held).To(BeFalse())
	})

	It("holds and does not order on a stale line, and names it", func() {
		for _, staleShape := range []bool{false, true} {
			f := Estimate(runLambda, []capacity.ReplicaCapacity{derived(true)},
				variants, nil, BacklogDrainSeconds, 0.85, staleShape, 0, nil)
			Expect(f.Terms[domain.RoleBoth].Held).To(BeTrue(), "staleShape=%v", staleShape)
			Expect(f.ByRole[domain.RoleBoth]).To(BeNumerically("~", 0.85*float64(runK1), 1e-6),
				"staleShape=%v: capped at scaleUp x one replica", staleShape)
			want := "shape-stale-line"
			if staleShape {
				want += "+shape-change"
			}
			Expect(f.Terms[domain.RoleBoth].HeldWhy).To(Equal(want))
		}
	})
})

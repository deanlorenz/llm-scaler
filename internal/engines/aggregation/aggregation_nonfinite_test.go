package aggregation_test

import (
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/aggregation"
)

// These sums are multiplied by a replica count and then compared with `>` or `<`
// by every consumer -- the engine's RequiredCapacity and SpareCapacity clamps,
// and the throughput floor's two caps. A NaN makes every such comparison false,
// so one variant with an unusable PerReplicaCapacity did not merely corrupt its
// own term: it disabled the guard the consumer applied to the whole sum, and the
// clamp meant to bound the result silently did not fire.
//
// So the property under test is not "the arithmetic is right" but "no single
// variant can make a total non-finite".
var _ = Describe("non-finite per-replica capacity", func() {
	const good = 930_000.0

	for _, tc := range []struct {
		name string
		p    float64
	}{
		{"a NaN capacity", math.NaN()},
		{"an infinite capacity", math.Inf(1)},
		{"a negatively infinite capacity", math.Inf(-1)},
		{"a negative capacity", -1000},
		{"a zero capacity", 0},
	} {
		It(tc.name+" counts as no capacity at all", func() {
			vcs := []domain.VariantCapacity{
				{VariantName: "good", Role: domain.RoleDecode, ReplicaCount: 2,
					PendingReplicas: 1, PerReplicaCapacity: good},
				{VariantName: "bad", Role: domain.RoleDecode, ReplicaCount: 2,
					PendingReplicas: 1, PerReplicaCapacity: tc.p},
			}
			byRole := aggregation.AggregateByRole(vcs)[domain.RoleDecode]
			totals := map[string]float64{
				"SumTotalSupply":              aggregation.SumTotalSupply(vcs),
				"SumTotalAnticipatedSupply":   aggregation.SumTotalAnticipatedSupply(vcs),
				"AggregateByRole.Supply":      byRole.TotalSupply,
				"AggregateByRole.Anticipated": byRole.TotalAnticipatedSupply,
			}
			for label, v := range totals {
				Expect(math.IsNaN(v)).To(BeFalse(), "%s must never be NaN", label)
				Expect(math.IsInf(v, 0)).To(BeFalse(), "%s must never be infinite", label)
			}

			// The good variant's contribution is exactly what it was worth, so
			// the sanitizer drops the bad term rather than the whole sum.
			Expect(totals["SumTotalSupply"]).To(BeNumerically("~", 2*good, 1e-6))
			Expect(totals["SumTotalAnticipatedSupply"]).To(BeNumerically("~", 3*good, 1e-6))
			Expect(byRole.TotalSupply).To(BeNumerically("~", 2*good, 1e-6))
			Expect(byRole.TotalAnticipatedSupply).To(BeNumerically("~", 3*good, 1e-6))
		})
	}

	It("still reports a negative supply from a negative pending count", func() {
		// Only the CAPACITY is sanitized. A negative PendingReplicas is a real
		// case with a real guard downstream -- the floor holds the figure at
		// zero rather than publishing a negative -- and reinterpreting it here
		// would hide it from the consumer that handles it.
		vcs := []domain.VariantCapacity{
			{VariantName: "v", Role: domain.RoleDecode, ReplicaCount: 1,
				PendingReplicas: -3, PerReplicaCapacity: good},
		}
		Expect(aggregation.SumTotalAnticipatedSupply(vcs)).
			To(BeNumerically("~", -2*good, 1e-6),
				"the negative survives, for the downstream clamp to see")
	})

	It("leaves a large finite capacity alone", func() {
		// The sanitizer excludes non-finite values, not big ones.
		big := math.MaxFloat64 / 4
		vcs := []domain.VariantCapacity{
			{VariantName: "v", Role: domain.RoleDecode, ReplicaCount: 1,
				PerReplicaCapacity: big},
		}
		Expect(aggregation.SumTotalSupply(vcs)).To(Equal(big))
	})
})

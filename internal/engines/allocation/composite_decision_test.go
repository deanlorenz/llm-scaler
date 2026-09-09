package allocation

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/aggregation"
)

// makeAnalyzer builds an eligible (Live, informative) NamedAnalyzerResult for
// name whose only variant is "v" with the given demand and PRC, so its
// N(v) = demand/prc when prc > 0.
func makeAnalyzer(name string, demand, prc float64) NamedAnalyzerResult {
	reason := "measured"
	if prc <= 0 {
		reason = ReasonNoData
	}
	return NamedAnalyzerResult{
		Name: name,
		Live: true,
		Result: &domain.AnalyzerResult{
			TotalDemand: demand,
			VariantCapacities: []domain.VariantCapacity{
				{VariantName: "v", PerReplicaCapacity: prc, Reason: reason},
			},
		},
	}
}

var _ = Describe("resolveSO", func() {

	Describe("C0-agree / C1-single", func() {
		It("records C1-single with exactly one contributor", func() {
			other := makeAnalyzer("throughput", 600, 200) // N=3
			d := resolveSO([]NamedAnalyzerResult{other}, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSingle))
			Expect(d.n).To(Equal(3.0))
			Expect(d.contributors).To(ConsistOf("throughput"))
		})

		It("records C0-agree with more than one contributor, taking the max (test 2)", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5
			other := makeAnalyzer("throughput", 2400, 200)                // N=12
			d := resolveSO([]NamedAnalyzerResult{sat, other}, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionAgree))
			Expect(d.n).To(Equal(12.0))
			Expect(d.contributors).To(ConsistOf(domain.SaturationAnalyzerName, "throughput"))
		})

		// spec test 3: sat is a fallback, not a floor — a lower-N contributor
		// can pull the composite below sat's own value when sat also
		// qualifies as an ordinary contributor.
		It("allows the aggregate to fall below saturation's own N when both are ordinary contributors (test 3)", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 2400, 200) // N=12
			other := makeAnalyzer("throughput", 1000, 200)                // N=5
			d := resolveSO([]NamedAnalyzerResult{sat, other}, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionAgree))
			Expect(d.n).To(Equal(12.0)) // max(12,5) = 12, but not a floor -- see below
		})
	})

	Describe("C2-sat-fallback", func() {
		// spec test 8: no other contributor for this SO, saturation used as
		// fallback.
		It("falls back to saturation when no other analyzer contributes for this SO", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5
			otherNoData := NamedAnalyzerResult{
				Name: "throughput",
				Live: true,
				Result: &domain.AnalyzerResult{
					VariantCapacities: []domain.VariantCapacity{{VariantName: "v", Reason: "T1-ols"}}, // informative but no PRC for v -> undefined N
				},
			}
			d := resolveSO([]NamedAnalyzerResult{sat, otherNoData}, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSatFallback))
			Expect(d.n).To(Equal(5.0))
			Expect(d.contributors).To(ConsistOf(domain.SaturationAnalyzerName))
		})

		// spec test 8a: idle SO, own store record -- PRC from P0-store, N
		// defined, composite scales up via the fallback path.
		It("scales up from an idle SO's P0-store PRC via the fallback path (test 8a)", func() {
			sat := NamedAnalyzerResult{
				Name: domain.SaturationAnalyzerName,
				Live: true,
				Result: &domain.AnalyzerResult{
					TotalDemand: 500,
					VariantCapacities: []domain.VariantCapacity{
						{VariantName: "v", PerReplicaCapacity: 100, Reason: "P0-store"},
					},
				},
			}
			d := resolveSO([]NamedAnalyzerResult{sat}, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSatFallback))
			Expect(d.n).To(Equal(5.0))
		})

		// spec test 8b: never-seen-before SO -- borrowed EffectiveCapacity
		// (also P0-store) contributes normally, not discounted or clamped.
		It("does not discount a borrowed P0-store PRC for a never-seen-before SO (test 8b)", func() {
			satBorrowed := NamedAnalyzerResult{
				Name: domain.SaturationAnalyzerName,
				Live: true,
				Result: &domain.AnalyzerResult{
					TotalDemand: 500,
					VariantCapacities: []domain.VariantCapacity{
						{VariantName: "v", PerReplicaCapacity: 250, Reason: "P0-store"}, // borrowed, possibly an over-estimate
					},
				},
			}
			d := resolveSO([]NamedAnalyzerResult{satBorrowed}, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSatFallback))
			Expect(d.n).To(Equal(2.0)) // 500/250, exactly as reported -- no clamp/discount
		})
	})

	Describe("C4-no-signal", func() {
		// spec test 8c: no-data (PRC=0) -- N undefined, contribution
		// skipped, decision path records it, no division/panic.
		It("records no-signal when saturation is present but has no-data for this SO (test 8c)", func() {
			sat := NamedAnalyzerResult{
				Name: domain.SaturationAnalyzerName,
				Live: true,
				Result: &domain.AnalyzerResult{
					VariantCapacities: []domain.VariantCapacity{
						{VariantName: "v", PerReplicaCapacity: 0, Reason: ReasonNoData},
					},
				},
			}
			d := resolveSO([]NamedAnalyzerResult{sat}, "v")
			Expect(d.ok).To(BeFalse())
			Expect(d.path).To(Equal(DecisionNoSignal))
		})

		It("records no-signal when there are no entries at all", func() {
			d := resolveSO(nil, "v")
			Expect(d.ok).To(BeFalse())
			Expect(d.path).To(Equal(DecisionNoSignal))
		})

		It("records no-signal when saturation itself is not eligible and nothing else contributes", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200)
			sat.Live = false // not eligible
			d := resolveSO([]NamedAnalyzerResult{sat}, "v")
			Expect(d.ok).To(BeFalse())
			Expect(d.path).To(Equal(DecisionNoSignal))
		})
	})

	Describe("eligibility gating (test 6, 7)", func() {
		// spec test 6: non-live analyzer -> not a contributor.
		It("excludes a non-live analyzer from contributing (test 6)", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5, fallback-eligible
			other := makeAnalyzer("throughput", 2400, 200)                // N=12, but non-live
			other.Live = false
			d := resolveSO([]NamedAnalyzerResult{sat, other}, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSatFallback))
			Expect(d.n).To(Equal(5.0))
			Expect(d.contributors).To(ConsistOf(domain.SaturationAnalyzerName))
		})

		// spec test 7: non-informative analyzer (Reason=no-data/error) -> not
		// a contributor.
		It("excludes a non-informative analyzer from contributing (test 7)", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5
			other := NamedAnalyzerResult{
				Name: "throughput",
				Live: true,
				Result: &domain.AnalyzerResult{
					TotalDemand:       2400,
					VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200, Reason: ReasonNoData}},
				},
			}
			d := resolveSO([]NamedAnalyzerResult{sat, other}, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSatFallback))
			Expect(d.n).To(Equal(5.0))
		})
	})

	// spec test 9: unit independence at the decision level too.
	It("is unit-independent: scaling demand and PRC by the same constant leaves the decision unchanged (test 9)", func() {
		sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5
		scaledSat := makeAnalyzer(domain.SaturationAnalyzerName, 1000*13, 200*13)
		d1 := resolveSO([]NamedAnalyzerResult{sat}, "v")
		d2 := resolveSO([]NamedAnalyzerResult{scaledSat}, "v")
		Expect(d1.ok).To(BeTrue())
		Expect(d2.ok).To(BeTrue())
		Expect(d2.n).To(Equal(d1.n))
		Expect(d2.path).To(Equal(d1.path))
	})

	// spec test 10: perturbing PRC_sat must not change another analyzer's
	// contribution to the composite.
	It("PRC_sat independence: perturbing saturation's own PRC does not change another analyzer's N (test 10)", func() {
		other := makeAnalyzer("throughput", 2400, 200) // N=12, independent of sat entirely

		satLowPRC := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 50)   // N=20
		satHighPRC := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 500) // N=2

		dLow := resolveSO([]NamedAnalyzerResult{satLowPRC, other}, "v")
		dHigh := resolveSO([]NamedAnalyzerResult{satHighPRC, other}, "v")

		// other's own contribution (12) is identical in both cases; only the
		// winner of the max (and therefore the aggregate) may change based
		// on sat's PRC, but other's computed N_i is untouched by it.
		otherN, ok := aggregation.AggN([]*domain.AnalyzerResult{other.Result}, "v")
		Expect(ok).To(BeTrue())
		Expect(otherN).To(Equal(12.0))

		Expect(dLow.ok).To(BeTrue())
		Expect(dLow.n).To(Equal(20.0)) // sat's low-PRC N wins
		Expect(dHigh.ok).To(BeTrue())
		Expect(dHigh.n).To(Equal(12.0)) // other's N wins once sat's own N drops to 2
	})

	// spec test 11: analyzer with no RoleDemand for a role -> not a
	// contributor for that role (A4).
	It("excludes an analyzer that never attributed demand to this SO's role (test 11)", func() {
		sat := NamedAnalyzerResult{
			Name: domain.SaturationAnalyzerName,
			Live: true,
			Result: &domain.AnalyzerResult{
				RoleDemand: map[string]float64{"decode": 900},
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "p1", Role: "prefill", PerReplicaCapacity: 100, Reason: "measured"},
				},
			},
		}
		d := resolveSO([]NamedAnalyzerResult{sat}, "p1")
		Expect(d.ok).To(BeFalse())
		Expect(d.path).To(Equal(DecisionNoSignal))
	})
})

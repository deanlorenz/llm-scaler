package aggregation

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

var _ = Describe("replicasNeeded (N_i(SO))", func() {

	It("computes D/PRC for a non-disaggregated result (demand in TotalDemand)", func() {
		result := &domain.AnalyzerResult{
			TotalDemand:       1000,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}},
		}
		n, ok := replicasNeeded(result, "v")
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(5.0))
	})

	It("computes D/PRC for a disaggregated result, using the variant's own role", func() {
		result := &domain.AnalyzerResult{
			RoleDemand: map[string]float64{"prefill": 400, "decode": 900},
			VariantCapacities: []domain.VariantCapacity{
				{VariantName: "p1", Role: "prefill", PerReplicaCapacity: 100},
				{VariantName: "d1", Role: "decode", PerReplicaCapacity: 300},
			},
		}
		n, ok := replicasNeeded(result, "p1")
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(4.0))

		n, ok = replicasNeeded(result, "d1")
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(3.0))
	})

	// spec test 21: PRC > 0 with demand(role(SO)) == 0 is explicitly legal.
	It("is defined and zero when demand is a real zero (test 21)", func() {
		result := &domain.AnalyzerResult{
			TotalDemand:       0,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}},
		}
		n, ok := replicasNeeded(result, "v")
		Expect(ok).To(BeTrue())
		Expect(n).To(BeZero())
	})

	// spec test 18: PRC == 0, demand > 0 -> undefined, no division by zero.
	It("is undefined when PRC is zero (test 18)", func() {
		result := &domain.AnalyzerResult{
			TotalDemand:       500,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 0}},
		}
		_, ok := replicasNeeded(result, "v")
		Expect(ok).To(BeFalse())
	})

	It("is undefined when PRC is negative", func() {
		result := &domain.AnalyzerResult{
			TotalDemand:       500,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: -10}},
		}
		_, ok := replicasNeeded(result, "v")
		Expect(ok).To(BeFalse())
	})

	// spec test 19: both zero -> no panic, no NaN/Inf, just undefined.
	It("is undefined, not NaN, when both PRC and demand are zero (test 19)", func() {
		result := &domain.AnalyzerResult{
			TotalDemand:       0,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 0}},
		}
		n, ok := replicasNeeded(result, "v")
		Expect(ok).To(BeFalse())
		Expect(n).To(BeZero()) // zero value, not NaN — ok is the authority, not n
	})

	It("is undefined when the variant is absent from VariantCapacities", func() {
		result := &domain.AnalyzerResult{
			TotalDemand:       500,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "other", PerReplicaCapacity: 100}},
		}
		_, ok := replicasNeeded(result, "v")
		Expect(ok).To(BeFalse())
	})

	// spec §5.4/A4: an analyzer silent about a role does not participate in it.
	It("is undefined when the analyzer never attributed demand to the variant's role (A4)", func() {
		result := &domain.AnalyzerResult{
			RoleDemand:        map[string]float64{"decode": 900}, // says nothing about prefill
			VariantCapacities: []domain.VariantCapacity{{VariantName: "p1", Role: "prefill", PerReplicaCapacity: 100}},
		}
		_, ok := replicasNeeded(result, "p1")
		Expect(ok).To(BeFalse())
	})

	It("is undefined for a nil result", func() {
		_, ok := replicasNeeded(nil, "v")
		Expect(ok).To(BeFalse())
	})

	// spec test 9: unit independence — scaling both D and PRC by the same
	// constant k must leave N_i(SO) unchanged.
	It("is unit-independent: scaling demand and PRC by the same constant leaves N unchanged (test 9)", func() {
		base := &domain.AnalyzerResult{
			TotalDemand:       1000,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}},
		}
		scaled := &domain.AnalyzerResult{
			TotalDemand:       1000 * 37,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200 * 37}},
		}
		nBase, okBase := replicasNeeded(base, "v")
		nScaled, okScaled := replicasNeeded(scaled, "v")
		Expect(okBase).To(BeTrue())
		Expect(okScaled).To(BeTrue())
		Expect(nScaled).To(Equal(nBase))
	})
})

var _ = Describe("AggN", func() {

	It("is undefined for an empty contributor list", func() {
		_, ok := AggN(nil, "v")
		Expect(ok).To(BeFalse())
	})

	// spec test 1 (partial): sat-only reduces to sat's own N_i(SO) exactly.
	It("reduces to the single contributor's N when there is only one", func() {
		sat := &domain.AnalyzerResult{
			TotalDemand:       1000,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}},
		}
		n, ok := AggN([]*domain.AnalyzerResult{sat}, "v")
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(5.0))
	})

	// spec test 2: another analyzer with a higher N raises N_com.
	It("takes the higher N when another analyzer disagrees upward (test 2)", func() {
		sat := &domain.AnalyzerResult{
			TotalDemand:       1000,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}}, // N=5
		}
		other := &domain.AnalyzerResult{
			TotalDemand:       2400,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}}, // N=12
		}
		n, ok := AggN([]*domain.AnalyzerResult{sat, other}, "v")
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(12.0))
	})

	// spec test 3: another analyzer with a LOWER N can pull N_com below
	// saturation's — sat is a fallback, not a floor. Must pass, unlike the
	// v1-v3 floor-invariant design.
	It("allows N_com to fall below saturation's own N when another analyzer reports lower demand (test 3)", func() {
		sat := &domain.AnalyzerResult{
			TotalDemand:       2400,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}}, // N=12
		}
		other := &domain.AnalyzerResult{
			TotalDemand:       1000,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}}, // N=5
		}
		satOnly, ok := replicasNeeded(sat, "v")
		Expect(ok).To(BeTrue())
		Expect(satOnly).To(Equal(12.0))

		// Whether sat is even a contributor to this aggregation is the
		// decision-path logic of a later step (§5.1) — AggN itself is a
		// pure max over whatever contributor list it is given. This test
		// shows that when sat is NOT in that list (excluded upstream
		// because a higher-priority contributor exists), the aggregate
		// correctly comes out below sat's own N — proving AggN carries no
		// hidden floor at saturation's value, unlike the old design.
		n, ok := AggN([]*domain.AnalyzerResult{other}, "v")
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(5.0))
		Expect(n).To(BeNumerically("<", satOnly))
	})

	It("skips undefined contributors and aggregates over the defined ones", func() {
		zeroDemandButValidPRC := &domain.AnalyzerResult{
			TotalDemand:       500,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 0}}, // undefined (PRC=0)
		}
		valid := &domain.AnalyzerResult{
			TotalDemand:       600,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}}, // N=3
		}
		n, ok := AggN([]*domain.AnalyzerResult{zeroDemandButValidPRC, valid}, "v")
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(3.0))
	})

	It("is undefined when every contributor's N for this variant is undefined", func() {
		undefinedA := &domain.AnalyzerResult{
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 0}},
		}
		undefinedB := &domain.AnalyzerResult{
			VariantCapacities: []domain.VariantCapacity{{VariantName: "other", PerReplicaCapacity: 100}},
		}
		_, ok := AggN([]*domain.AnalyzerResult{undefinedA, undefinedB}, "v")
		Expect(ok).To(BeFalse())
	})

	// spec test 20: an undefined contribution must not silently act as +Inf
	// in the max, which would wrongly demand infinite replicas.
	It("does not let an undefined contributor act as +Inf in the max (test 20)", func() {
		undefined := &domain.AnalyzerResult{
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 0}}, // PRC=0 undefined
		}
		valid := &domain.AnalyzerResult{
			TotalDemand:       200,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 100}}, // N=2
		}
		n, ok := AggN([]*domain.AnalyzerResult{undefined, valid}, "v")
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(2.0)) // not +Inf
	})

	// spec test 13: AggN is a pure max — Score plays no role at all (there is
	// no Score parameter to this function in the first place, which is the
	// point: Score cannot leak into N_com through this seam).
	It("has no Score parameter — Score cannot influence the aggregated N by construction (test 13)", func() {
		a := &domain.AnalyzerResult{
			TotalDemand:       500,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 100}}, // N=5
		}
		b := &domain.AnalyzerResult{
			TotalDemand:       1000,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 100}}, // N=10
		}
		n1, ok1 := AggN([]*domain.AnalyzerResult{a, b}, "v")
		n2, ok2 := AggN([]*domain.AnalyzerResult{b, a}, "v") // order swapped; no score anywhere
		Expect(ok1).To(BeTrue())
		Expect(ok2).To(BeTrue())
		Expect(n1).To(Equal(10.0))
		Expect(n2).To(Equal(10.0))
	})
})

package steadystate

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
)

var _ = Describe("normalizeToCompositeUnits", func() {
	It("non-disaggregated: normalizes PRC and TotalDemand, captures SatDemand", func() {
		nr := &allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: 8000,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, PerReplicaCapacity: 2000},
				},
			},
		}

		normalizeToCompositeUnits(nr)

		Expect(nr.SatDemand).To(Equal(8000.0))
		Expect(nr.Result.TotalDemand).To(Equal(1.0))
		Expect(nr.Result.VariantCapacities[0].PerReplicaCapacity).To(BeNumerically("~", 0.25, 1e-9))
	})

	It("disaggregated: normalizes per-role PRC and demands independently", func() {
		nr := &allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: 12000,
				RoleDemand: map[string]float64{
					"prefill": 4000,
					"decode":  8000,
				},
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "prefill-v1", Role: "prefill", PerReplicaCapacity: 1000},
					{VariantName: "decode-v1", Role: "decode", PerReplicaCapacity: 2000},
				},
			},
		}

		normalizeToCompositeUnits(nr)

		Expect(nr.SatDemand).To(Equal(12000.0))
		Expect(nr.Result.TotalDemand).To(Equal(1.0))
		Expect(nr.Result.RoleDemand["prefill"]).To(Equal(1.0))
		Expect(nr.Result.RoleDemand["decode"]).To(Equal(1.0))
		// prefill PRC: 1000/4000 = 0.25
		Expect(nr.Result.VariantCapacities[0].PerReplicaCapacity).To(BeNumerically("~", 0.25, 1e-9))
		// decode PRC: 2000/8000 = 0.25
		Expect(nr.Result.VariantCapacities[1].PerReplicaCapacity).To(BeNumerically("~", 0.25, 1e-9))
	})

	It("demand=0: leaves PRC unchanged, still sets TotalDemand to 1.0", func() {
		origPRC := 500.0
		nr := &allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: 0,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, PerReplicaCapacity: origPRC},
				},
			},
		}

		normalizeToCompositeUnits(nr)

		Expect(nr.Result.TotalDemand).To(Equal(1.0))
		Expect(nr.Result.VariantCapacities[0].PerReplicaCapacity).To(Equal(origPRC))
	})

	It("PRC=0: leaves PRC as 0 after normalization", func() {
		nr := &allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: 8000,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, PerReplicaCapacity: 0},
				},
			},
		}

		normalizeToCompositeUnits(nr)

		Expect(nr.Result.TotalDemand).To(Equal(1.0))
		Expect(nr.Result.VariantCapacities[0].PerReplicaCapacity).To(Equal(0.0))
	})

	It("nil Result: does not panic and is a no-op", func() {
		nr := &allocation.NamedAnalyzerResult{
			Result:    nil,
			SatDemand: 0,
		}

		Expect(func() { normalizeToCompositeUnits(nr) }).NotTo(Panic())
		Expect(nr.SatDemand).To(Equal(0.0))
	})

	It("RoleCapacities.TotalDemand is normalized to 1.0", func() {
		nr := &allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: 8000,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, PerReplicaCapacity: 2000},
				},
			},
			RoleCapacities: map[string]domain.RoleCapacity{
				"prefill": {Role: "prefill", TotalDemand: 4000, TotalSupply: 8000},
			},
		}

		normalizeToCompositeUnits(nr)

		Expect(nr.RoleCapacities["prefill"].TotalDemand).To(Equal(1.0))
		// TotalSupply must be untouched
		Expect(nr.RoleCapacities["prefill"].TotalSupply).To(Equal(8000.0))
	})

	It("SatDemand equals original TotalDemand (not 1.0) after the call", func() {
		originalDemand := 16000.0
		nr := &allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: originalDemand,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, PerReplicaCapacity: 4000},
				},
			},
		}

		normalizeToCompositeUnits(nr)

		Expect(nr.SatDemand).To(Equal(originalDemand))
		Expect(nr.SatDemand).NotTo(Equal(nr.Result.TotalDemand))
		Expect(nr.Result.TotalDemand).To(Equal(1.0))
	})
})

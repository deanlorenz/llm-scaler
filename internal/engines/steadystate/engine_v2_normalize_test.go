package steadystate

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
)

var _ = Describe("normalizeToCompositeUnits", func() {
	It("non-disaggregated: normalizes PRC, TotalDemand, RC/SC/Remaining/Spare/supply, captures SatDemand", func() {
		src := allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: 8000,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, PerReplicaCapacity: 2000},
				},
			},
			// As buildNamedResult would have set them from buildCapacities/
			// applyUniversalThreshold, in the same raw (token-scale) units as
			// TotalDemand and PerReplicaCapacity above.
			RequiredCapacity:       6000,
			SpareCapacity:          1000,
			Remaining:              6000,
			Spare:                  1000,
			TotalSupply:            4000,
			TotalAnticipatedSupply: 5000,
		}

		nr := normalizeToCompositeUnits(src)

		Expect(nr.SatDemand).To(Equal(8000.0))
		Expect(nr.Result.TotalDemand).To(Equal(1.0))
		Expect(nr.Result.VariantCapacities[0].PerReplicaCapacity).To(BeNumerically("~", 0.25, 1e-9))

		// Every other raw-demand-scaled signal must be divided by the SAME raw
		// demand PerReplicaCapacity was divided by (8000), or a downstream
		// consumer dividing/subtracting it against the now-fractional PRC
		// produces a replica count wrong by ~1/0.25 = 4x.
		Expect(nr.RequiredCapacity).To(BeNumerically("~", 0.75, 1e-9))        // 6000/8000
		Expect(nr.SpareCapacity).To(BeNumerically("~", 0.125, 1e-9))          // 1000/8000
		Expect(nr.Remaining).To(BeNumerically("~", 0.75, 1e-9))               // 6000/8000
		Expect(nr.Spare).To(BeNumerically("~", 0.125, 1e-9))                  // 1000/8000
		Expect(nr.TotalSupply).To(BeNumerically("~", 0.5, 1e-9))              // 4000/8000
		Expect(nr.TotalAnticipatedSupply).To(BeNumerically("~", 0.625, 1e-9)) // 5000/8000
		// Recomputed from the normalized TotalDemand/TotalSupply: 1.0/0.5 = 2.0,
		// algebraically identical to the pre-normalization 8000/4000 = 2.0.
		Expect(nr.Utilization).To(BeNumerically("~", 2.0, 1e-9))
	})

	It("disaggregated: normalizes per-role PRC, demands, and per-role RC/SC/supply independently", func() {
		src := allocation.NamedAnalyzerResult{
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
			RoleCapacities: map[string]domain.RoleCapacity{
				"prefill": {
					Role: "prefill", TotalDemand: 4000,
					RequiredCapacity: 2000, SpareCapacity: 500,
					TotalSupply: 1000, TotalAnticipatedSupply: 1000,
				},
				"decode": {
					Role: "decode", TotalDemand: 8000,
					RequiredCapacity: 4000, SpareCapacity: 1000,
					TotalSupply: 2000, TotalAnticipatedSupply: 3000,
				},
			},
		}

		nr := normalizeToCompositeUnits(src)

		Expect(nr.SatDemand).To(Equal(12000.0))
		Expect(nr.SatRoleDemand).To(Equal(map[string]float64{"prefill": 4000, "decode": 8000}))
		Expect(nr.Result.TotalDemand).To(Equal(1.0))
		Expect(nr.Result.RoleDemand["prefill"]).To(Equal(1.0))
		Expect(nr.Result.RoleDemand["decode"]).To(Equal(1.0))
		// prefill PRC: 1000/4000 = 0.25
		Expect(nr.Result.VariantCapacities[0].PerReplicaCapacity).To(BeNumerically("~", 0.25, 1e-9))
		// decode PRC: 2000/8000 = 0.25
		Expect(nr.Result.VariantCapacities[1].PerReplicaCapacity).To(BeNumerically("~", 0.25, 1e-9))

		// Each role's RC/SC/supply divided by that role's OWN raw demand, not the
		// model-level demand — a P/D model's roles carry different demand.
		prefill := nr.RoleCapacities["prefill"]
		Expect(prefill.TotalDemand).To(Equal(1.0))
		Expect(prefill.RequiredCapacity).To(BeNumerically("~", 0.5, 1e-9))        // 2000/4000
		Expect(prefill.SpareCapacity).To(BeNumerically("~", 0.125, 1e-9))         // 500/4000
		Expect(prefill.TotalSupply).To(BeNumerically("~", 0.25, 1e-9))            // 1000/4000
		Expect(prefill.TotalAnticipatedSupply).To(BeNumerically("~", 0.25, 1e-9)) // 1000/4000

		decode := nr.RoleCapacities["decode"]
		Expect(decode.TotalDemand).To(Equal(1.0))
		Expect(decode.RequiredCapacity).To(BeNumerically("~", 0.5, 1e-9))         // 4000/8000
		Expect(decode.SpareCapacity).To(BeNumerically("~", 0.125, 1e-9))          // 1000/8000
		Expect(decode.TotalSupply).To(BeNumerically("~", 0.25, 1e-9))             // 2000/8000
		Expect(decode.TotalAnticipatedSupply).To(BeNumerically("~", 0.375, 1e-9)) // 3000/8000
	})

	It("demand=0: leaves PRC, RC/SC/Remaining/Spare/supply, and TotalDemand all unchanged", func() {
		origPRC := 500.0
		src := allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: 0,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, PerReplicaCapacity: origPRC},
				},
			},
			RequiredCapacity:       0,
			SpareCapacity:          500,
			Remaining:              0,
			Spare:                  500,
			TotalSupply:            500,
			TotalAnticipatedSupply: 500,
		}

		nr := normalizeToCompositeUnits(src)

		// demand=0 must flow through to the composite unchanged (spec's own
		// special case): coverage isn't meaningful without demand, and
		// ceil(0/PRC) still needs to read 0 downstream (e.g. rescale.go's
		// roleDemandGPUs) — forcing TotalDemand to 1.0 here would make that
		// read a phantom nonzero demand instead.
		Expect(nr.Result.TotalDemand).To(Equal(0.0))
		Expect(nr.Result.VariantCapacities[0].PerReplicaCapacity).To(Equal(origPRC))
		// Zero demand guards every new division exactly like the existing PRC
		// guard: nothing to divide by, so every raw-demand-scaled field is left
		// as buildCapacities/applyUniversalThreshold computed it.
		Expect(nr.RequiredCapacity).To(Equal(0.0))
		Expect(nr.SpareCapacity).To(Equal(500.0))
		Expect(nr.Remaining).To(Equal(0.0))
		Expect(nr.Spare).To(Equal(500.0))
		Expect(nr.TotalSupply).To(Equal(500.0))
		Expect(nr.TotalAnticipatedSupply).To(Equal(500.0))
	})

	It("PRC=0: leaves PRC as 0 after normalization", func() {
		src := allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: 8000,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, PerReplicaCapacity: 0},
				},
			},
		}

		nr := normalizeToCompositeUnits(src)

		Expect(nr.Result.TotalDemand).To(Equal(1.0))
		Expect(nr.Result.VariantCapacities[0].PerReplicaCapacity).To(Equal(0.0))
	})

	It("nil Result: does not panic and is a no-op", func() {
		src := allocation.NamedAnalyzerResult{
			Result:    nil,
			SatDemand: 0,
		}

		var nr allocation.NamedAnalyzerResult
		Expect(func() { nr = normalizeToCompositeUnits(src) }).NotTo(Panic())
		Expect(nr.SatDemand).To(Equal(0.0))
	})

	It("RoleCapacities.TotalDemand is normalized to 1.0, and TotalSupply is normalized alongside it", func() {
		// Result.RoleDemand["prefill"] mirrors RoleCapacities["prefill"].TotalDemand,
		// as buildRoleCapacities always sets them together in the real pipeline —
		// demandForRole resolves a role's raw demand from Result.RoleDemand (via
		// SatRoleDemand), not from the RoleCapacities entry's own TotalDemand field.
		src := allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: 8000,
				RoleDemand:  map[string]float64{"prefill": 4000},
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, PerReplicaCapacity: 2000},
				},
			},
			RoleCapacities: map[string]domain.RoleCapacity{
				"prefill": {Role: "prefill", TotalDemand: 4000, TotalSupply: 8000},
			},
		}

		nr := normalizeToCompositeUnits(src)

		Expect(nr.RoleCapacities["prefill"].TotalDemand).To(Equal(1.0))
		// TotalSupply keeps the struct's documented invariant
		// (TotalSupply = Σ replicas × PerReplicaCapacity) true after normalization:
		// divided by the role's own raw demand (4000), same as the role's PRC.
		Expect(nr.RoleCapacities["prefill"].TotalSupply).To(BeNumerically("~", 2.0, 1e-9)) // 8000/4000
	})

	It("SatDemand equals original TotalDemand (not 1.0) after the call", func() {
		originalDemand := 16000.0
		src := allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: originalDemand,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, PerReplicaCapacity: 4000},
				},
			},
		}

		nr := normalizeToCompositeUnits(src)

		Expect(nr.SatDemand).To(Equal(originalDemand))
		Expect(nr.SatDemand).NotTo(Equal(nr.Result.TotalDemand))
		Expect(nr.Result.TotalDemand).To(Equal(1.0))
	})

	It("captures SatRoleDemand as a copy independent of the source map", func() {
		src := allocation.NamedAnalyzerResult{
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

		nr := normalizeToCompositeUnits(src)

		Expect(nr.SatRoleDemand).To(Equal(map[string]float64{"prefill": 4000, "decode": 8000}))
		// Result.RoleDemand is now 1.0 for every role; SatRoleDemand must not have
		// been aliased to the same map and overwritten alongside it.
		Expect(nr.Result.RoleDemand["prefill"]).To(Equal(1.0))
		Expect(nr.SatRoleDemand["prefill"]).To(Equal(4000.0))
	})

	It("non-disaggregated result has a nil SatRoleDemand (nothing to capture)", func() {
		src := allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: 8000,
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: domain.RoleBoth, PerReplicaCapacity: 2000},
				},
			},
		}

		nr := normalizeToCompositeUnits(src)

		Expect(nr.SatRoleDemand).To(BeNil())
	})

	It("does not alias src: mutating the returned result leaves src's Result, RoleCapacities, and RoleSpare untouched", func() {
		src := allocation.NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				TotalDemand: 8000,
				RoleDemand:  map[string]float64{"prefill": 8000},
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Role: "prefill", PerReplicaCapacity: 2000},
				},
			},
			RoleCapacities: map[string]domain.RoleCapacity{
				"prefill": {Role: "prefill", TotalDemand: 8000, RequiredCapacity: 6000},
			},
			RoleSpare: map[string]float64{"prefill": 1234},
		}

		nr := normalizeToCompositeUnits(src)

		// The returned result is normalized...
		Expect(nr.Result.TotalDemand).To(Equal(1.0))
		Expect(nr.Result.VariantCapacities[0].PerReplicaCapacity).To(BeNumerically("~", 0.25, 1e-9))
		Expect(nr.RoleCapacities["prefill"].TotalDemand).To(Equal(1.0))

		// ...but src, which normalizeToCompositeUnits never mutates, must still
		// read exactly as constructed. A value-copy alone would NOT catch this,
		// because Result is a pointer and RoleCapacities/RoleSpare are maps —
		// this is the aliasing hazard the deep copy exists to close.
		Expect(src.Result.TotalDemand).To(Equal(8000.0))
		Expect(src.Result.RoleDemand["prefill"]).To(Equal(8000.0))
		Expect(src.Result.VariantCapacities[0].PerReplicaCapacity).To(Equal(2000.0))
		Expect(src.RoleCapacities["prefill"].TotalDemand).To(Equal(8000.0))
		Expect(src.RoleCapacities["prefill"].RequiredCapacity).To(Equal(6000.0))
		Expect(src.RoleSpare["prefill"]).To(Equal(1234.0))

		// The returned result's own maps must be independent objects too, not
		// just independently-valued right now — mutating them must not reach src.
		nr.RoleCapacities["prefill"] = domain.RoleCapacity{Role: "prefill", TotalDemand: 999}
		nr.RoleSpare["prefill"] = 999
		nr.Result.RoleDemand["prefill"] = 999
		Expect(src.RoleCapacities["prefill"].TotalDemand).To(Equal(8000.0))
		Expect(src.RoleSpare["prefill"]).To(Equal(1234.0))
		Expect(src.Result.RoleDemand["prefill"]).To(Equal(8000.0))
	})
})

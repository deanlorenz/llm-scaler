package allocation

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

var _ = Describe("replicasForDemand", func() {
	It("returns ceil(demand/prc)", func() {
		Expect(replicasForDemand(1000, 300)).To(Equal(4)) // ceil(3.33) = 4
	})

	It("returns 0 when prc is 0", func() {
		Expect(replicasForDemand(1000, 0)).To(Equal(0))
	})

	It("returns 0 when prc is negative", func() {
		Expect(replicasForDemand(1000, -5)).To(Equal(0))
	})

	It("returns 0 for a negative demand rather than a negative replica count", func() {
		Expect(replicasForDemand(-1000, 300)).To(Equal(0))
	})

	It("returns 0 for zero demand", func() {
		Expect(replicasForDemand(0, 300)).To(Equal(0))
	})
})

var _ = Describe("safeReplicasForSpare", func() {
	It("returns floor(spare/prc)", func() {
		Expect(safeReplicasForSpare(1000, 300)).To(Equal(3)) // floor(3.33) = 3
	})

	It("returns 0 when prc is 0", func() {
		Expect(safeReplicasForSpare(1000, 0)).To(Equal(0))
	})

	It("returns 0 when prc is negative", func() {
		Expect(safeReplicasForSpare(1000, -5)).To(Equal(0))
	})

	It("returns 0 for negative spare rather than a negative replica count", func() {
		Expect(safeReplicasForSpare(-1000, 300)).To(Equal(0))
	})
})

var _ = Describe("demandForRoleOrModel", func() {
	It("returns 0 for a nil Result", func() {
		Expect(demandForRoleOrModel(NamedAnalyzerResult{Result: nil}, "prefill")).To(BeZero())
	})

	It("returns model-level TotalDemand for domain.RoleBoth", func() {
		nr := NamedAnalyzerResult{Result: &domain.AnalyzerResult{TotalDemand: 500}}
		Expect(demandForRoleOrModel(nr, domain.RoleBoth)).To(Equal(500.0))
	})

	It("canonicalizes an empty role to domain.RoleBoth", func() {
		nr := NamedAnalyzerResult{Result: &domain.AnalyzerResult{TotalDemand: 500}}
		Expect(demandForRoleOrModel(nr, "")).To(Equal(500.0))
	})

	It("reads RoleCapacities[role].TotalDemand for a non-both role when present", func() {
		nr := NamedAnalyzerResult{
			Result:         &domain.AnalyzerResult{TotalDemand: 9999}, // decoy
			RoleCapacities: map[string]domain.RoleCapacity{"prefill": {TotalDemand: 400}},
		}
		Expect(demandForRoleOrModel(nr, "prefill")).To(Equal(400.0))
	})

	It("falls back to model-level TotalDemand when the role is absent from RoleCapacities", func() {
		nr := NamedAnalyzerResult{
			Result:         &domain.AnalyzerResult{TotalDemand: 700},
			RoleCapacities: map[string]domain.RoleCapacity{"decode": {TotalDemand: 400}}, // no "prefill"
		}
		Expect(demandForRoleOrModel(nr, "prefill")).To(Equal(700.0))
	})

	It("never consults RoleCapacities for domain.RoleBoth even if a \"both\" entry exists", func() {
		// Mirrors roleDemandGPUs's original behavior exactly: the "both" role
		// always reads the model-level scalar, unconditionally.
		nr := NamedAnalyzerResult{
			Result:         &domain.AnalyzerResult{TotalDemand: 500},
			RoleCapacities: map[string]domain.RoleCapacity{domain.RoleBoth: {TotalDemand: 999}},
		}
		Expect(demandForRoleOrModel(nr, domain.RoleBoth)).To(Equal(500.0))
	})
})

var _ = Describe("requiredSpareForRoleOrModel", func() {
	It("returns model-level RequiredCapacity/SpareCapacity when RoleCapacities has no entry for role", func() {
		nr := NamedAnalyzerResult{RequiredCapacity: 100, SpareCapacity: 10}
		rc, sc := requiredSpareForRoleOrModel(nr, "prefill")
		Expect(rc).To(Equal(100.0))
		Expect(sc).To(Equal(10.0))
	})

	It("prefers RoleCapacities[role] when present, for a non-both role", func() {
		nr := NamedAnalyzerResult{
			RequiredCapacity: 9999, SpareCapacity: 8888, // decoys
			RoleCapacities: map[string]domain.RoleCapacity{"prefill": {RequiredCapacity: 100, SpareCapacity: 10}},
		}
		rc, sc := requiredSpareForRoleOrModel(nr, "prefill")
		Expect(rc).To(Equal(100.0))
		Expect(sc).To(Equal(10.0))
	})

	It("canonicalizes an empty role to domain.RoleBoth before the lookup", func() {
		nr := NamedAnalyzerResult{
			RequiredCapacity: 9999, SpareCapacity: 8888,
			RoleCapacities: map[string]domain.RoleCapacity{domain.RoleBoth: {RequiredCapacity: 300, SpareCapacity: 30}},
		}
		rc, sc := requiredSpareForRoleOrModel(nr, "")
		Expect(rc).To(Equal(300.0))
		Expect(sc).To(Equal(30.0))
	})

	// Confirms the deliberate difference from demandForRoleOrModel: THIS
	// helper attempts the RoleCapacities lookup for domain.RoleBoth too (a
	// disaggregated model may still carry a "both" RoleCapacities entry,
	// e.g. an empty-Role variant mapped in alongside prefill/decode), unlike
	// demandForRoleOrModel which never consults RoleCapacities for
	// RoleBoth. Each mirrors its own original call site's exact behavior.
	It("DOES consult RoleCapacities for domain.RoleBoth when a \"both\" entry exists", func() {
		nr := NamedAnalyzerResult{
			RequiredCapacity: 9999, SpareCapacity: 8888,
			RoleCapacities: map[string]domain.RoleCapacity{domain.RoleBoth: {RequiredCapacity: 300, SpareCapacity: 30}},
		}
		rc, sc := requiredSpareForRoleOrModel(nr, domain.RoleBoth)
		Expect(rc).To(Equal(300.0))
		Expect(sc).To(Equal(30.0))
	})
})

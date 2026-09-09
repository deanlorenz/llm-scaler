package aggregation

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

var _ = Describe("demandForRole", func() {

	Describe("non-disaggregated layout (RoleDemand nil)", func() {
		It("reads TotalDemand for domain.RoleBoth", func() {
			result := &domain.AnalyzerResult{TotalDemand: 42.0, RoleDemand: nil}
			value, present := demandForRole(result, domain.RoleBoth)
			Expect(present).To(BeTrue())
			Expect(value).To(Equal(42.0))
		})

		It("canonicalizes an empty role string to domain.RoleBoth", func() {
			result := &domain.AnalyzerResult{TotalDemand: 42.0, RoleDemand: nil}
			value, present := demandForRole(result, "")
			Expect(present).To(BeTrue())
			Expect(value).To(Equal(42.0))
		})

		It("reads a real zero TotalDemand as present, not absent", func() {
			result := &domain.AnalyzerResult{TotalDemand: 0, RoleDemand: nil}
			value, present := demandForRole(result, domain.RoleBoth)
			Expect(present).To(BeTrue())
			Expect(value).To(BeZero())
		})

		It("reports not-present for a specific role when the layout is non-disaggregated", func() {
			result := &domain.AnalyzerResult{TotalDemand: 42.0, RoleDemand: nil}
			_, present := demandForRole(result, "prefill")
			Expect(present).To(BeFalse())

			_, present = demandForRole(result, "decode")
			Expect(present).To(BeFalse())
		})
	})

	Describe("disaggregated layout (RoleDemand non-nil)", func() {
		It("reads a present role's value", func() {
			result := &domain.AnalyzerResult{
				RoleDemand: map[string]float64{"prefill": 10.0, "decode": 20.0},
			}
			value, present := demandForRole(result, "prefill")
			Expect(present).To(BeTrue())
			Expect(value).To(Equal(10.0))

			value, present = demandForRole(result, "decode")
			Expect(present).To(BeTrue())
			Expect(value).To(Equal(20.0))
		})

		It("distinguishes present-and-zero from absent", func() {
			result := &domain.AnalyzerResult{
				RoleDemand: map[string]float64{"prefill": 0.0},
			}
			value, present := demandForRole(result, "prefill")
			Expect(present).To(BeTrue())
			Expect(value).To(BeZero())

			_, present = demandForRole(result, "decode")
			Expect(present).To(BeFalse())
		})

		It("canonicalizes an empty role to domain.RoleBoth when looking up a disaggregated map", func() {
			result := &domain.AnalyzerResult{
				RoleDemand: map[string]float64{domain.RoleBoth: 5.0, "prefill": 10.0},
			}
			value, present := demandForRole(result, "")
			Expect(present).To(BeTrue())
			Expect(value).To(Equal(5.0))
		})

		It("reports not-present for a role entirely absent from the map", func() {
			result := &domain.AnalyzerResult{
				RoleDemand: map[string]float64{"prefill": 10.0},
			}
			_, present := demandForRole(result, "decode")
			Expect(present).To(BeFalse())
		})
	})
})

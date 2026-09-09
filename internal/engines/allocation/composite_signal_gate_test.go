package allocation

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

var _ = Describe("HasUsableCompositeSignal", func() {

	It("is false for a nil Result (absent — no analysis this cycle)", func() {
		Expect(HasUsableCompositeSignal(NamedAnalyzerResult{Result: nil})).To(BeFalse())
	})

	It("is true for a real, informative composite result, regardless of its Name", func() {
		composite := NamedAnalyzerResult{
			Name: "CompositeSignal", // not "saturation" -- the whole point of the rename (spec §8)
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v", PerReplicaCapacity: 100, Reason: "P0-store"},
				},
			},
		}
		Expect(HasUsableCompositeSignal(composite)).To(BeTrue())
	})

	// test 14: renamed composite -> the quota guard still fires. This is the
	// direct behavioral assertion that A11's fix-by-intent survives the
	// rename: the OLD hasSaturationResult (Name == domain.SaturationAnalyzerName
	// && Result != nil) would go permanently false here since Name is no
	// longer "saturation" -- HasUsableCompositeSignal must not care.
	It("is true for a renamed composite carrying a usable signal (test 14 — guard not silently disabled)", func() {
		composite := NamedAnalyzerResult{
			Name: "CompositeSignal",
			Result: &domain.AnalyzerResult{
				TotalDemand:       500,
				VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 100, Reason: "measured"}},
			},
		}
		Expect(HasUsableCompositeSignal(composite)).To(BeTrue())
		Expect(composite.Name).ToNot(Equal(domain.SaturationAnalyzerName),
			"the test is only meaningful if the composite's name really is not saturation's")
	})

	// test 8d: no signal at all -> no autoscaling. Every VariantCapacity
	// carries a no-data/error sentinel (mirrors what a composite with every
	// SO's decision path bottoming out at C4-no-signal would look like).
	It("is false when every VariantCapacity is a no-data/error sentinel (test 8d — no signal at all)", func() {
		composite := NamedAnalyzerResult{
			Name: "CompositeSignal",
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Reason: ReasonNoData},
					{VariantName: "v2", Reason: ReasonError},
				},
			},
		}
		Expect(HasUsableCompositeSignal(composite)).To(BeFalse())
	})

	It("is false for an empty VariantCapacities slice — no capacity signal at all", func() {
		composite := NamedAnalyzerResult{
			Name:   "CompositeSignal",
			Result: &domain.AnalyzerResult{},
		}
		Expect(HasUsableCompositeSignal(composite)).To(BeFalse())
	})

	It("is true when at least one VariantCapacity is informative even if others are not", func() {
		composite := NamedAnalyzerResult{
			Name: "CompositeSignal",
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Reason: ReasonNoData},
					{VariantName: "v2", PerReplicaCapacity: 200, Reason: "P0-store"},
				},
			},
		}
		Expect(HasUsableCompositeSignal(composite)).To(BeTrue())
	})
})

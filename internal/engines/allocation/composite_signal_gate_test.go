package allocation

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

var _ = Describe("CompositeHasSignal", func() {

	It("is false for a nil Result (absent — no analysis this cycle)", func() {
		Expect(CompositeHasSignal(NamedAnalyzerResult{Result: nil})).To(BeFalse())
	})

	It("is true for a composite with at least one SO reaching a real decision path, regardless of its Name", func() {
		composite := NamedAnalyzerResult{
			Name: "CompositeSignal", // not "saturation" -- the whole point of the rename (spec §8)
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v", PerReplicaCapacity: 100, Reason: string(DecisionSingle)},
				},
			},
		}
		Expect(CompositeHasSignal(composite)).To(BeTrue())
	})

	// test 14: renamed composite -> the quota guard still fires. This is the
	// direct behavioral assertion that A11's fix-by-intent survives the
	// rename: the OLD hasSaturationResult (Name == domain.SaturationAnalyzerName
	// && Result != nil) would go permanently false here since Name is no
	// longer "saturation" -- CompositeHasSignal must not care.
	It("is true for a renamed composite carrying a usable signal (test 14 — guard not silently disabled)", func() {
		composite := NamedAnalyzerResult{
			Name: "CompositeSignal",
			Result: &domain.AnalyzerResult{
				TotalDemand:       500,
				VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 100, Reason: string(DecisionSatFallback)}},
			},
		}
		Expect(CompositeHasSignal(composite)).To(BeTrue())
		Expect(composite.Name).ToNot(Equal(domain.SaturationAnalyzerName),
			"the test is only meaningful if the composite's name really is not saturation's")
	})

	// test 8d: no signal at all -> no autoscaling. Every SO's decision path
	// bottomed out at C4-no-signal.
	It("is false when every VariantCapacity's decision path is C4-no-signal (test 8d — no signal at all)", func() {
		composite := NamedAnalyzerResult{
			Name: "CompositeSignal",
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Reason: string(DecisionNoSignal)},
					{VariantName: "v2", Reason: string(DecisionNoSignal)},
				},
			},
		}
		Expect(CompositeHasSignal(composite)).To(BeFalse())
	})

	It("is false for an empty VariantCapacities slice — no capacity signal at all", func() {
		composite := NamedAnalyzerResult{
			Name:   "CompositeSignal",
			Result: &domain.AnalyzerResult{},
		}
		Expect(CompositeHasSignal(composite)).To(BeFalse())
	})

	It("is true when at least one SO reached a real decision even if others are C4-no-signal", func() {
		composite := NamedAnalyzerResult{
			Name: "CompositeSignal",
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", Reason: string(DecisionNoSignal)},
					{VariantName: "v2", PerReplicaCapacity: 200, Reason: string(DecisionAgree)},
				},
			},
		}
		Expect(CompositeHasSignal(composite)).To(BeTrue())
	})

	// The regression this fix exists to catch: an analyzer-style no-data
	// sentinel string is NOT the same thing as the composite's own
	// DecisionNoSignal marker, and the two must never be conflated -- a
	// composite Reason of anything other than literally "C4-no-signal" (even
	// an analyzer sentinel string used by mistake) must count as usable,
	// because on the real composite construction path Reason is always a
	// decision-path value, never an analyzer sentinel.
	It("treats a composite Reason equal to an analyzer sentinel string as usable, since composite Reason means decision path, not analyzer provenance", func() {
		composite := NamedAnalyzerResult{
			Name: "CompositeSignal",
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v", Reason: ReasonNoData}, // NOT DecisionNoSignal -- a different string
				},
			},
		}
		Expect(CompositeHasSignal(composite)).To(BeTrue())
	})
})

var _ = Describe("SOHasSignal", func() {

	It("is false for a nil Result", func() {
		Expect(SOHasSignal(NamedAnalyzerResult{Result: nil}, "v")).To(BeFalse())
	})

	It("is true when the named variant's own decision path is not C4-no-signal", func() {
		composite := NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", PerReplicaCapacity: 100, Reason: string(DecisionSingle)},
					{VariantName: "v2", Reason: string(DecisionNoSignal)},
				},
			},
		}
		Expect(SOHasSignal(composite, "v1")).To(BeTrue())
	})

	It("is false when the named variant's own decision path is C4-no-signal, even if another SO has a real signal", func() {
		composite := NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", PerReplicaCapacity: 100, Reason: string(DecisionSingle)},
					{VariantName: "v2", Reason: string(DecisionNoSignal)},
				},
			},
		}
		Expect(SOHasSignal(composite, "v2")).To(BeFalse())
	})

	It("is false when the named variant is absent from VariantCapacities entirely", func() {
		composite := NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v1", PerReplicaCapacity: 100, Reason: string(DecisionSingle)},
				},
			},
		}
		Expect(SOHasSignal(composite, "does-not-exist")).To(BeFalse())
	})

	// Same regression this file's CompositeHasSignal cases pin, at the
	// per-SO granularity: an analyzer-style sentinel string is not
	// DecisionNoSignal and must count as usable.
	It("treats a Reason equal to an analyzer sentinel string as usable for that SO", func() {
		composite := NamedAnalyzerResult{
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v", Reason: ReasonNoData},
				},
			},
		}
		Expect(SOHasSignal(composite, "v")).To(BeTrue())
	})
})

package aggregation

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("modelCoverageFromRoles", func() {

	// spec test 5 (cross-role rule), purely disaggregated case: cov(both) = 0.
	It("is min(prefill, decode) for a purely disaggregated model (no both entry) (test 5)", func() {
		coverage, ok := modelCoverageFromRoles(map[string]float64{"prefill": 0.8, "decode": 0.6})
		Expect(ok).To(BeTrue())
		Expect(coverage).To(Equal(0.6))
	})

	// spec test 5, non-disaggregated case: both only.
	It("is cov(both) alone for a non-disaggregated model (no prefill/decode entries) (test 5)", func() {
		coverage, ok := modelCoverageFromRoles(map[string]float64{"both": 0.75})
		Expect(ok).To(BeTrue())
		Expect(coverage).To(Equal(0.75))
	})

	It("sums min(prefill, decode) and both when all three are present", func() {
		coverage, ok := modelCoverageFromRoles(map[string]float64{
			"prefill": 0.9, "decode": 0.5, "both": 0.1,
		})
		Expect(ok).To(BeTrue())
		Expect(coverage).To(Equal(0.6)) // min(0.9, 0.5) + 0.1
	})

	// spec test 23 (part): a role carrying demand but with no defined
	// coverage (absent, not present-and-zero) must not act as a spurious 0
	// in the min -- that would wrongly report a genuinely-covered role pair
	// as fully uncovered.
	It("does not let an absent role act as a spurious 0 in the min (test 23, A14)", func() {
		// decode has no defined coverage at all (e.g. no SO serves it yet);
		// only prefill is defined. A naive map lookup defaulting to 0 would
		// compute min(0.8, 0) = 0, wrongly reporting zero model coverage.
		coverage, ok := modelCoverageFromRoles(map[string]float64{"prefill": 0.8})
		Expect(ok).To(BeTrue())
		Expect(coverage).To(Equal(0.8), "decode's absence must not zero out prefill's real coverage")
	})

	It("is not-ok when the map is empty -- no defined coverage anywhere", func() {
		_, ok := modelCoverageFromRoles(map[string]float64{})
		Expect(ok).To(BeFalse())
	})

	It("is not-ok for a nil map", func() {
		_, ok := modelCoverageFromRoles(nil)
		Expect(ok).To(BeFalse())
	})

	It("treats a real coverage of exactly 0 as defined, not absent", func() {
		coverage, ok := modelCoverageFromRoles(map[string]float64{"both": 0.0})
		Expect(ok).To(BeTrue())
		Expect(coverage).To(BeZero())
	})

	It("treats a real prefill coverage of exactly 0 as defined and lets it win the min honestly", func() {
		coverage, ok := modelCoverageFromRoles(map[string]float64{"prefill": 0.0, "decode": 0.9})
		Expect(ok).To(BeTrue())
		Expect(coverage).To(BeZero()) // min(0, 0.9) + 0(both absent) = 0, and this IS the true answer
	})
})

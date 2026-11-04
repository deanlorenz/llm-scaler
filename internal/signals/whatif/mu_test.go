package whatif

import (
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PricingK", func() {
	It("is the two thresholds multiplied, inside the band", func() {
		Expect(PricingK(0.80, 0.85, DefaultBand())).To(BeNumerically("~", 0.68, 1e-15))
	})

	It("clamps to the band it is given", func() {
		Expect(PricingK(0.80, 0.85, FillBand{Lo: 0.3, Hi: 0.5})).To(Equal(0.5))
		Expect(PricingK(0.20, 0.50, FillBand{Lo: 0.3, Hi: 0.5})).To(Equal(0.3))
	})

	// Clamped to an inverted band the answer is whichever edge the last clamp
	// applied: 0.3 one way round, 0.5 the other. Neither is a price, so the
	// band is replaced by the default and the product is returned.
	DescribeTable("prices on the default band when the band given breaks FillBand's precondition",
		func(band FillBand) {
			Expect(PricingK(0.80, 0.85, band)).To(Equal(PricingK(0.80, 0.85, DefaultBand())))
		},
		Entry("inverted", FillBand{Lo: 0.5, Hi: 0.3}),
		Entry("a point", FillBand{Lo: 0.4, Hi: 0.4}),
		Entry("NaN bottom", FillBand{Lo: math.NaN(), Hi: 0.8}),
		Entry("NaN top", FillBand{Lo: 0.15, Hi: math.NaN()}),
		Entry("bottom at 0", FillBand{Lo: 0, Hi: 0.8}),
		Entry("top at 1", FillBand{Lo: 0.15, Hi: 1}),
	)
})

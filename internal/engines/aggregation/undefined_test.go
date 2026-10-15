package aggregation

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("maxOfDefined", func() {
	It("returns not-ok for zero items", func() {
		_, ok := maxOfDefined(0, func(i int) (float64, bool) { return 0, false })
		Expect(ok).To(BeFalse())
	})

	It("returns not-ok when every item is undefined", func() {
		values := []bool{false, false, false}
		_, ok := maxOfDefined(len(values), func(i int) (float64, bool) { return 0, values[i] })
		Expect(ok).To(BeFalse())
	})

	It("takes the max across only the defined items, skipping undefined ones", func() {
		values := []float64{5, 100, 3}
		defined := []bool{true, false, true}
		value, ok := maxOfDefined(len(values), func(i int) (float64, bool) { return values[i], defined[i] })
		Expect(ok).To(BeTrue())
		Expect(value).To(Equal(5.0)) // 100 is undefined and must not win
	})

	It("a spurious 0 does not mask a genuine negative-free max (undefined never enters as 0)", func() {
		values := []float64{-5, -1}
		defined := []bool{true, true}
		value, ok := maxOfDefined(len(values), func(i int) (float64, bool) { return values[i], defined[i] })
		Expect(ok).To(BeTrue())
		Expect(value).To(Equal(-1.0))
	})

	It("does not let an undefined contributor act as +Inf", func() {
		// If undefined silently became +Inf, this would return +Inf instead of 7.
		values := []float64{7}
		value, ok := maxOfDefined(1, func(i int) (float64, bool) {
			if i == 0 {
				return values[0], true
			}
			return 0, false
		})
		Expect(ok).To(BeTrue())
		Expect(value).To(Equal(7.0))
	})
})

var _ = Describe("minOfDefined", func() {
	It("returns not-ok for zero items", func() {
		_, ok := minOfDefined(0, func(i int) (float64, bool) { return 0, false })
		Expect(ok).To(BeFalse())
	})

	It("returns not-ok when every item is undefined", func() {
		values := []bool{false, false}
		_, ok := minOfDefined(len(values), func(i int) (float64, bool) { return 0, values[i] })
		Expect(ok).To(BeFalse())
	})

	It("takes the min across only the defined items, skipping undefined ones", func() {
		values := []float64{5, -100, 3}
		defined := []bool{true, false, true}
		value, ok := minOfDefined(len(values), func(i int) (float64, bool) { return values[i], defined[i] })
		Expect(ok).To(BeTrue())
		Expect(value).To(Equal(3.0)) // -100 is undefined and must not win
	})

	It("does not let an undefined contributor act as a spurious 0", func() {
		// If undefined silently became 0, this would wrongly return 0 instead of 2.
		values := []float64{2, 4}
		defined := []bool{true, true}
		value, ok := minOfDefined(len(values), func(i int) (float64, bool) { return values[i], defined[i] })
		Expect(ok).To(BeTrue())
		Expect(value).To(Equal(2.0))
	})
})

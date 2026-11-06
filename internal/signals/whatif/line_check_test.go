package whatif

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

var _ = Describe("CheckLine", func() {
	const kvReq = 1500.0
	d := Decode{Line: itl.Model{A: 0.01, B: 0.005}, CapacityTokens: 100000, ReplicaCap: 1000}
	// rate is the token rate d's line predicts at k, off by factor.
	rate := func(k, factor float64) float64 {
		return factor * ReplicaTokenRate(d.Line, k, d.CapacityTokens, kvReq, d.ReplicaCap)
	}
	row := func(cycle int, k, factor float64) LineRow {
		return LineRow{Cycle: cycle, K: k, KVPerRequest: kvReq, TokenRate: rate(k, factor)}
	}

	It("passes a line that predicts the rows, and says how many it read", func() {
		c := CheckLine(d, []LineRow{row(7, 0.4, 1.02), row(8, 0.5, 0.98), row(9, 0.6, 1)})
		Expect(c.Mismatch()).To(BeFalse())
		Expect(c.Compared).To(Equal(3))
		Expect(c.Past).To(Equal(0))
		Expect(c.FromCycle).To(Equal(7))
		Expect(c.ToCycle).To(Equal(9))
		Expect(c.FillLo).To(Equal(0.4))
		Expect(c.FillHi).To(Equal(0.6))
	})

	It("flags a line past 15% and signs it: a line reading high over-promises", func() {
		// The replica made half the tokens the line says: 100% off, line high.
		c := CheckLine(d, []LineRow{row(1, 0.4, 0.5), row(2, 0.5, 0.5), row(3, 0.6, 0.5)})
		Expect(c.Mismatch()).To(BeTrue())
		Expect(c.MedianErrorPct).To(BeNumerically("~", 100, 1e-6))
		Expect(c.SignedErrorPct).To(BeNumerically("~", 100, 1e-6))
		Expect(c.Past).To(Equal(3))

		low := CheckLine(d, []LineRow{row(1, 0.4, 2), row(2, 0.5, 2)})
		Expect(low.Mismatch()).To(BeTrue())
		Expect(low.SignedErrorPct).To(BeNumerically("~", -50, 1e-6))
	})

	It("reads the threshold as the analyzers do: 15% is not past it", func() {
		on := CheckLine(d, []LineRow{row(1, 0.5, 1/1.15)})
		Expect(on.MedianErrorPct).To(BeNumerically("~", 15, 1e-9))
		just := CheckLine(d, []LineRow{row(1, 0.5, 1/1.16)})
		Expect(just.Mismatch()).To(BeTrue())
	})

	It("takes the median: one noisy row of three decides nothing, and is still counted", func() {
		c := CheckLine(d, []LineRow{row(1, 0.4, 1), row(2, 0.5, 1), row(3, 0.6, 0.3)})
		Expect(c.Mismatch()).To(BeFalse())
		Expect(c.Past).To(Equal(1))
		Expect(c.WorstErrorPct).To(BeNumerically(">", 200))
	})

	It("leaves rows on a ramp out, and says how many", func() {
		ramp := row(2, 0.5, 0.2)
		ramp.Ramp = true
		c := CheckLine(d, []LineRow{row(1, 0.4, 1), ramp})
		Expect(c.Compared).To(Equal(1))
		Expect(c.OnRamp).To(Equal(1))
		Expect(c.Mismatch()).To(BeFalse())
	})

	// The analyzers' floor: below 0.30 a percentage is quantisation.
	It("leaves out a row the GPS check would not compare", func() {
		c := CheckLine(d, []LineRow{
			row(1, 0.29, 0.1),
			{Cycle: 2, K: 0.5, KVPerRequest: kvReq},
			{Cycle: 3, K: 0.5, TokenRate: 100},
		})
		Expect(c.Compared).To(Equal(0))
		Expect(c.OnRamp).To(Equal(0))
	})

	It("is no mismatch and no pass with nothing to compare", func() {
		c := CheckLine(d, nil)
		Expect(c.Mismatch()).To(BeFalse())
		Expect(c.Compared).To(Equal(0))
	})

	// itl.GPSErrorPct prices a cache: a replica at its batch cap holds fewer
	// sequences than k implies. Uncapped, a true line at the cap reads as a
	// mismatch of 100%.
	It("caps the sequences as every price does", func() {
		capped := d
		capped.ReplicaCap = 20
		k := 0.6
		seqs := k * capped.CapacityTokens / kvReq
		Expect(seqs).To(BeNumerically(">=", 2*float64(capped.ReplicaCap)))
		r := LineRow{Cycle: 1, K: k, KVPerRequest: kvReq, TokenRate: float64(capped.ReplicaCap) / capped.Line.ITLAt(k)}
		Expect(CheckLine(capped, []LineRow{r}).MedianErrorPct).To(BeNumerically("~", 0, 1e-9))
	})
})

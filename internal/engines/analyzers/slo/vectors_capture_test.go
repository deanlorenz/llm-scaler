package slo

import (
	"math"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// The decode pricing this package runs is moving to internal/signals/whatif
// (docs/proposals/slo-capacity-planner.md, PR D1). These vectors are its
// inputs and outputs as captured on main before the move; the move must
// reproduce every output bit for bit. WHATIF_WRITE_VECTORS=1 rewrites the file
// from the code as it stands; without it the test checks the code against it.

const sloVectorsFile = "../../../signals/whatif/testdata/slo-pricing.jsonl.gz"

// vecLines are ITL lines (A, B): a full fit, pinned-B fits on a learned and on
// the default 6 ms intercept, a per-token line of a speculating engine, and
// lines no fit should produce, which the pricing must refuse rather than use.
var vecLines = []itl.Model{
	{A: 0.004438, B: 0.001557},             // ols: the ols-sweep golden's last line
	{A: 0.040, B: 0.020},                   // ols: price_test.go's line
	{A: 0.0045, B: 0.006},                  // pinned-B on the default intercept
	{A: 0.003, B: 0.0021},                  // pinned-B on a learned intercept
	{A: 0.004438 / 2.6, B: 0.001557 / 2.6}, // per token, 1.6 accepted per step
	{A: 1e-12, B: 0.002},                   // a slope at the noise floor
	{A: 0.05, B: -0.004},                   // a negative intercept
	{A: 0, B: 0},                           // no line
	{A: 0, B: 0.02},                        // flat
	{A: -0.01, B: 0.02},                    // inverted
	{A: math.NaN(), B: 0.02},               // NaN slope
	{A: 0.04, B: math.NaN()},               // NaN intercept
	{A: math.Inf(1), B: 0.02},              // infinite slope
	{A: 0.04, B: math.Inf(1)},              // infinite intercept
	{A: 0.04, B: math.Inf(-1)},             // minus-infinite intercept
	{A: 5e-324, B: 0.001},                  // subnormal slope
}

// vecBands are fill bands: the default, the widest, a narrow one, a point, an
// inverted one validation would reject, and one above the default top.
var vecBands = []fillBand{
	defaultBand, {lo: 0, hi: 1}, {lo: 0.3, hi: 0.5}, {lo: 0.25, hi: 0.25}, {lo: 0.5, hi: 0.3}, {lo: 0.15, hi: 0.95},
}

var (
	vecTargets = []float64{0.001, 0.002, 0.0035, 0.00366, 0.004, 0.006, 0.026, 0.027, 0.044, 1.0,
		0, -0.001, math.NaN(), math.Inf(1), math.Inf(-1), 5e-324}
	vecFills = []float64{0, 1, 1e-9, 0.15, 0.474, 0.6, 0.68, 0.732, 0.8, 0.85, -0.1, 1.5, math.NaN(), math.Inf(1)}
	// vecCapKV pairs a KV capacity in tokens with a per-request footprint.
	vecCapKV = [][2]float64{
		{131072, 1500}, {100000, 500}, {1e6, 4000}, {131072, 1e-300},
		{0, 1500}, {-1, 1500}, {math.NaN(), 1500}, {math.Inf(1), 1500},
		{9.223372036854776e18, 1500}, {131072, 0}, {131072, math.NaN()}, {131072, math.Inf(1)},
	}
	vecSeqCaps = []int64{0, 1, 8, 64, 256, 512, -1}
	vecScaleUp = []float64{0.85, 0.95, 1, 0, 0.3, 1.2, -1, math.NaN()}
)

func TestDecodePricingVectors(t *testing.T) {
	var got []vector
	for _, b := range vecBands {
		for _, l := range vecLines {
			targets := append(append([]float64{}, vecTargets...), l.ITLAt(b.lo))
			for _, tgt := range targets {
				p := priceTPOT(b, l, tgt)
				got = append(got, vector{Fn: "PriceTPOT",
					F: fbits(b.lo, b.hi, l.A, l.B, tgt), OF: fbits(p.fill, p.target), OB: []bool{p.feasible, p.ok}})
			}
		}
	}
	for _, l := range vecLines {
		for _, k := range vecFills {
			for _, ck := range vecCapKV {
				for _, s := range vecSeqCaps {
					got = append(got, vector{Fn: "ReplicaTokenRate", F: fbits(l.A, l.B, k, ck[0], ck[1]), I: []int64{s},
						OF: fbits(replicaTokenRate(l, k, ck[0], ck[1], s))})
				}
			}
		}
	}
	for _, b := range vecBands {
		for _, k := range vecFills {
			for _, ck := range vecCapKV {
				for _, s := range vecSeqCaps {
					got = append(got, vector{Fn: "ReachableFill", F: fbits(b.lo, b.hi, k, ck[0], ck[1]), I: []int64{s},
						OF: fbits(reachableFill(b, k, ck[0], ck[1], s))})
				}
			}
		}
	}
	realK := []float64{0.15, 0.474, 0.6, 0.732, 0.8, 1, 0, math.NaN()}
	for _, b := range vecBands[:2] {
		for _, l := range append(vecLines[:5:5], vecLines[10]) {
			for _, k := range realK {
				for _, su := range vecScaleUp {
					for _, ck := range vecCapKV[:5] {
						for _, s := range []int64{0, 8, 64} {
							got = append(got, vector{Fn: "RealisedFill", F: fbits(b.lo, b.hi, l.A, l.B, k, su, ck[0], ck[1]),
								I: []int64{s}, OF: fbits(realisedFill(b, l, k, su, ck[0], ck[1], s))})
						}
					}
				}
			}
		}
	}
	checkVectors(t, sloVectorsFile, got)
}

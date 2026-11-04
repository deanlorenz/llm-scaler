package saturation

import (
	"math"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
)

// The mu-from-ITL arithmetic this package runs is moving to
// internal/signals/whatif (docs/proposals/slo-capacity-planner.md, PR D1).
// These vectors are its inputs and outputs as captured on main before the
// move; the move must reproduce every output bit for bit.
// WHATIF_WRITE_VECTORS=1 rewrites the file from the code as it stands; without
// it the test checks the code against it.

const saturationVectorsFile = "../../../signals/whatif/testdata/saturation-mu.jsonl.gz"

// muLines are ITL lines (A, B): full fits, pinned-B fits on a learned and on
// the default 6 ms intercept, a per-token line of a speculating engine, and
// lines no fit should produce.
var muLines = []itl.Model{
	{A: 0.004438, B: 0.001557},             // ols: the ols-sweep golden's last line
	{A: 0.040, B: 0.020},                   // ols
	{A: 0.0045, B: 0.006},                  // pinned-B on the default intercept
	{A: 0.003, B: 0.0021},                  // pinned-B on a learned intercept
	{A: 0.004438 / 2.6, B: 0.001557 / 2.6}, // per token, 1.6 accepted per step
	{A: 0.05, B: -0.04},                    // ITL negative at low k
	{A: 0, B: 0},                           // no line
	{A: 0, B: 0.02},                        // flat
	{A: math.NaN(), B: 0.02},               // NaN slope
	{A: 0.04, B: math.Inf(1)},              // infinite intercept
}

// muParams are the deployment's engine parameters as capacityTokensFor reads
// them: absent, empty, a flag, a negative flag and the overflow sentinel.
var muParams = []*capacity.EngineParams{
	nil, {}, {TotalKvTokensOverride: 200000}, {TotalKvTokensOverride: -5}, {TotalKvTokensOverride: math.MaxInt64},
}

func TestMuFromITLVectors(t *testing.T) {
	var got []vector
	ptr := func(p *capacity.EngineParams) []int64 {
		if p == nil {
			return []int64{0, 0}
		}
		return []int64{1, p.TotalKvTokensOverride}
	}

	// pricingK over the thresholds of a tier and the band it sets. The band
	// is recorded as the policy resolves it (ITLBand), since that is what the
	// moved function takes.
	got = append(got, vector{Fn: "PricingK", I: []int64{0},
		F:  fbits(0, 0, config.DefaultITLMinObservableK, config.DefaultITLMaxObservableK),
		OF: fbits(pricingK(nil))})
	for _, kv := range []float64{0.8, 0.9, 1, 0.5, 1.2, 0, -0.5, math.NaN(), math.Inf(1)} {
		for _, su := range []float64{0.85, 0.95, 1, 0.7, 0, -1, math.NaN()} {
			for _, b := range [][2]float64{{0, 0}, {0.15, 0.80}, {0.1, 0.9}, {0.3, 0.5}, {0.5, 0.3}, {0.2, 0.99}} {
				cfg := &config.ScalingPolicy{KvCacheThreshold: kv, ScaleUpThreshold: su}
				cfg.AnalyzerTuning.ITLBand.MinObservableK, cfg.AnalyzerTuning.ITLBand.MaxObservableK = b[0], b[1]
				lo, hi := cfg.ITLBand()
				got = append(got, vector{Fn: "PricingK", I: []int64{1},
					F: fbits(kv, su, lo, hi), OF: fbits(pricingK(cfg))})
			}
		}
	}

	for _, p := range muParams {
		for _, ranks := range []int{-1, 0, 1, 2, 8} {
			for _, live := range []int64{0, 1, 131072, -1, math.MaxInt64 - 1, math.MaxInt64} {
				got = append(got, vector{Fn: "CapacityTokens", I: append(ptr(p), int64(ranks), live),
					OF: fbits(capacityTokensFor(p, ranks, live))})
			}
		}
	}

	for _, m := range muLines {
		for _, p := range muParams[:3] {
			for _, batchCap := range []int64{0, 64, -1} {
				for _, ranks := range []int{0, 8} {
					for _, kvTok := range []int64{0, 131072} {
						for _, kvReq := range []float64{1500, 0, math.NaN()} {
							for _, kPrice := range []float64{0.68, 0.8, 0.15, 1, 0, 1.0000001, math.NaN()} {
								for _, out := range []float64{1000, 0, math.NaN()} {
									d := deriveMuAtCap(m, p, batchCap, ranks, kvTok, shape.Shape{KVreq: kvReq}, kPrice, out)
									got = append(got, vector{Fn: "DeriveMuAtCap",
										F:  fbits(m.A, m.B, kvReq, kPrice, out),
										I:  append(ptr(p), batchCap, int64(ranks), kvTok),
										OF: fbits(d.rate, d.seqs, d.tokenSec), OB: []bool{d.ok, d.capAtMeasured}, OS: []string{d.why}})
								}
							}
						}
					}
				}
			}
		}
	}
	checkVectors(t, saturationVectorsFile, got)
}

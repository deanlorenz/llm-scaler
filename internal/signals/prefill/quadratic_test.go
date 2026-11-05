package prefill

import (
	"encoding/csv"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle/settlerow"
)

// The curved fit of a (fixed.go): the quadratic s0 + b*I + c*I*I through the
// prompt-length clusters' medians where the minutes carry three parameters,
// the line otherwise.

// curveMinutes is one low-load cycle per prompt length, each minute, on a
// pod whose service time is s0 + b*I + c*I*I and whose overhead is 2.5 ms,
// at 0.02 requests/s so that a second-long prompt is still low load.
func curveMinutes(s0, b, c float64, prompts ...float64) []Cycle {
	out := make([]Cycle, len(prompts))
	for i, p := range prompts {
		out[i] = cycle(time.Time{}, lowLoad(p, s0+b*p+c*p*p, 0.02, 0.0025))
	}
	return out
}

// steadyLevels is each prompt length held for n minutes: steadyPrompts with
// a longer hold. The steady gate keeps n-2 of each level after the first,
// and n-1 of the first.
func steadyLevels(n int, levels ...float64) []float64 {
	out := make([]float64, 0, n*len(levels))
	for _, l := range levels {
		for range n {
			out = append(out, l)
		}
	}
	return out
}

// lineOnly is cs with the quadratic off.
func lineOnly(cs []Cycle) []Cycle {
	for i := range cs {
		cs[i].ALineOnly = true
	}
	return cs
}

// run 3's pod, as the medians of the TTFT recording measured it: Qwen3-0.6B
// at TP1, 2.8 ms + 1.73e-6 s/token + 1.73e-10 s/token^2.
const (
	run3S0 = 0.0028
	run3B  = 1.734e-6
	run3C  = 1.727e-10
)

// Known truth: a pod whose service time is a convex quadratic, swept over
// three prompt levels 8k to 37k, gives back s0, b and c, and a adds the
// least overhead. The line through the same minutes meets the axis below
// zero and is refused, as P1 shipped refused run 3.
func TestQuadratic_RecoversTheCurve(t *testing.T) {
	prompts := steadyLevels(5, 8000, 30000, 37000)
	est := run(NewObserver(), curveMinutes(run3S0, run3B, run3C, prompts...)...)
	require.Empty(t, est.ARefused)
	assert.Equal(t, AModelQuadratic, est.AModel)
	assert.Empty(t, est.ALineReason)
	assert.Equal(t, 3, est.AClusters)
	assert.InDelta(t, run3S0, est.AIntercept, 1e-9)
	assert.InEpsilon(t, run3B, est.ASlope, 1e-6)
	assert.InEpsilon(t, run3C, est.ACurve, 1e-6)
	assert.InDelta(t, run3S0+0.0025, est.A, 1e-9)
	// The validation bound stays the line's: 1/slope of the chord.
	lineIntercept, lineSlope := ols(lowLoadXs(prompts), lowLoadYs(prompts, run3S0, run3B, run3C))
	assert.InEpsilon(t, 1/lineSlope, est.SlopeBound, 1e-9)

	off := run(NewObserver(), lineOnly(curveMinutes(run3S0, run3B, run3C, prompts...))...)
	assert.Equal(t, AModelLinear, off.AModel)
	assert.Equal(t, LineOnly, off.ALineReason)
	assert.Equal(t, NonPositiveIntercept, off.ARefused, "the line's intercept %.4f", lineIntercept)
	assert.Zero(t, off.A)
}

// lowLoadXs is the prompt lengths of steadyLevels the steady gate admits,
// and lowLoadYs their service times on the curve.
func lowLoadXs(prompts []float64) []float64 {
	var out []float64
	for i, p := range prompts {
		if i == 0 || p != prompts[i-1] || (i > 1 && p != prompts[i-2]) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func lowLoadYs(prompts []float64, s0, b, c float64) []float64 {
	xs := lowLoadXs(prompts)
	out := make([]float64, len(xs))
	for i, p := range xs {
		out[i] = s0 + b*p + c*p*p
	}
	return out
}

// On random convex pods swept over three or more levels spanning at least
// MinQuadraticSpread, the curve gives back s0, b and c.
func TestQuadratic_PropertyRecoversRandomCurves(t *testing.T) {
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 23))
		s0 := 0.001 + 0.02*rng.Float64()
		b := 1e-7 + 5e-6*rng.Float64()
		c := 1e-12 + 5e-10*rng.Float64()
		lo := 500 + 4000*rng.Float64()
		levels := []float64{lo, lo * (1.3 + 1.5*rng.Float64())}
		levels = append(levels, lo*(MinQuadraticSpread+5*rng.Float64()))
		if rng.IntN(2) == 0 {
			levels = append(levels, levels[2]*(1.3+rng.Float64()))
		}
		est := run(NewObserver(), curveMinutes(s0, b, c, steadyLevels(5, levels...)...)...)
		require.Empty(t, est.ARefused, "seed %d", seed)
		require.Equal(t, AModelQuadratic, est.AModel, "seed %d", seed)
		require.InDelta(t, s0, est.AIntercept, 1e-9, "seed %d", seed)
		require.InEpsilon(t, b, est.ASlope, 1e-5, "seed %d", seed)
		require.InEpsilon(t, c, est.ACurve, 1e-5, "seed %d", seed)
	}
}

// A straight-line pod still uses the line where the minutes cannot carry
// the curve: two prompt levels (ols-sweep's 1k and 6k), three levels
// spanning under MinQuadraticSpread, and three whose middle level holds too
// few minutes to count. Each gives the line's a exactly.
func TestQuadratic_TheLineWhereTheClustersCannotCarryIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prompts  []float64
		why      LineReason
		clusters int
	}{
		{"two levels", steadyLevels(5, 1000, 6000), TooFewPromptClusters, 2},
		{"three levels under 3x", steadyLevels(5, 1000, 1800, 2900), NarrowPromptClusters, 3},
		// The 3000-token level held three minutes: the steady gate keeps
		// one, under MinQuadraticClusterMinutes.
		{"a thin middle level", append(append(steadyLevels(5, 1000), steadyLevels(3, 3000)...), steadyLevels(5, 6000)...),
			TooFewPromptClusters, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			est := run(NewObserver(), curveMinutes(0.008, 2e-6, 0, tc.prompts...)...)
			require.Empty(t, est.ARefused)
			assert.Equal(t, AModelLinear, est.AModel)
			assert.Equal(t, tc.why, est.ALineReason)
			assert.Equal(t, tc.clusters, est.AClusters)
			assert.InDelta(t, 0.008, est.AIntercept, 1e-12)
			assert.InDelta(t, 2e-6, est.ASlope, 1e-15)
			assert.Zero(t, est.ACurve)
			assert.InDelta(t, 0.0105, est.A, 1e-12)
		})
	}
}

// A fitted c not above zero is not convex: the line. A pod whose per-token
// cost falls with the prompt (concave) reads the line's intercept.
func TestQuadratic_ANonConvexFitIsTheLine(t *testing.T) {
	t.Run("concave", func(t *testing.T) {
		prompts := steadyLevels(5, 2000, 8000, 16000)
		est := run(NewObserver(), curveMinutes(0.010, 4e-6, -5e-11, prompts...)...)
		require.Empty(t, est.ARefused)
		assert.Equal(t, AModelLinear, est.AModel)
		assert.Equal(t, ConcaveFit, est.ALineReason)
		assert.Equal(t, 3, est.AClusters)
		assert.Zero(t, est.ACurve)
		want, _ := ols(lowLoadXs(prompts), lowLoadYs(prompts, 0.010, 4e-6, -5e-11))
		assert.InDelta(t, want, est.AIntercept, 1e-12)
	})
}

// A quadratic whose intercept is not above zero is refused, not handed to
// the line: a convex pod with s0 of -1 ms reads AModel quadratic and
// NonPositiveIntercept, with its curve reported. Two slow minutes at the
// shortest level hold the line's intercept above zero (about 4 ms) and
// leave that cluster's median on the curve, so a fallback to the line
// would publish an a.
func TestQuadratic_ANonPositiveInterceptIsRefused(t *testing.T) {
	const s0, b, c = -0.001, 2e-6, 1e-10
	prompts := append(steadyLevels(7, 2000), steadyLevels(5, 8000, 16000)...)
	cs := curveMinutes(s0, b, c, prompts...)
	for _, i := range []int{5, 6} {
		cs[i].Rows[0] = lowLoad(2000, 0.03, 0.02, 0.0025)
	}
	var xs, ys []float64
	for i, cy := range cs {
		if i == 0 || (i >= 7 && (i-7)%5 < 2) {
			continue // not steady (steadyLevels)
		}
		xs, ys = append(xs, cy.Rows[0].PrefillComputedTokenRate/cy.Rows[0].RequestRate), append(ys, cy.Rows[0].AvgServiceTime)
	}
	lineIntercept, _ := ols(xs, ys)
	require.Greater(t, lineIntercept, 0.0, "the premise: the line would publish an a")

	est := run(NewObserver(), cs...)
	require.Equal(t, 12, est.ASamples)
	assert.Equal(t, NonPositiveIntercept, est.ARefused)
	assert.Equal(t, AModelQuadratic, est.AModel)
	assert.Empty(t, est.ALineReason)
	assert.Zero(t, est.AIntercept)
	assert.Zero(t, est.A)
	assert.InEpsilon(t, b, est.ASlope, 1e-6, "the curve is reported with the refusal")
	assert.InEpsilon(t, c, est.ACurve, 1e-6, "the curve is reported with the refusal")

}

// The thresholds, each at its edge: three clusters are the fewest, three
// minutes the fewest a cluster counts with, and a spread of exactly
// MinQuadraticSpread is enough.
func TestQuadratic_TheThresholds(t *testing.T) {
	require.Equal(t, 3, MinQuadraticClusters)
	require.Equal(t, 3, MinQuadraticClusterMinutes)
	require.Equal(t, 3.0, MinQuadraticSpread)

	pts := func(levels []float64, per []int) ([]float64, []float64) {
		xs, ys := make([]float64, 0, 16), make([]float64, 0, 16)
		for i, l := range levels {
			for range per[i] {
				xs, ys = append(xs, l), append(ys, run3S0+run3B*l+run3C*l*l)
			}
		}
		return xs, ys
	}
	for _, tc := range []struct {
		name   string
		levels []float64
		per    []int
		why    LineReason
	}{
		{"three clusters of three, exactly 3x", []float64{4000, 8000, 12000}, []int{3, 3, 3}, ""},
		{"just under 3x", []float64{4000, 8000, 11990}, []int{3, 3, 3}, NarrowPromptClusters},
		{"a cluster of two", []float64{4000, 8000, 12000}, []int{3, 2, 3}, TooFewPromptClusters},
		{"two clusters", []float64{4000, 12000}, []int{5, 5}, TooFewPromptClusters},
		// The thin cluster is dropped and the other three still carry it.
		{"four clusters, one thin", []float64{4000, 6000, 8000, 12000}, []int{3, 1, 3, 3}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			xs, ys := pts(tc.levels, tc.per)
			q, why := fitQuadratic(xs, ys)
			assert.Equal(t, tc.why, why)
			if why == "" {
				assert.InDelta(t, run3S0, q.a, 1e-9)
				assert.InEpsilon(t, run3C, q.c, 1e-6)
			}
		})
	}

	// Clusters break at MaxPromptDrift between neighbours.
	t.Run("cluster edges", func(t *testing.T) {
		cl := promptClusters([]float64{1000, 1100, 1210, 1332}, []float64{1, 2, 3, 4})
		assert.Len(t, cl, 2, "1210 is within 10 percent of 1100, 1332 is not of 1210")
		assert.Equal(t, 3, cl[0].n)
		assert.Equal(t, 1100.0, cl[0].x)
		assert.Equal(t, 2.0, cl[0].y)
	})
}

// One slow minute in a cluster of five does not move the curve: each
// cluster is its median. Run 3's 22:28 minute read 0.375 s at 38k against
// about 0.30 s at its length; fitted through the minutes, it pulls the
// intercept from 3.7 ms to 21.5 ms.
func TestQuadratic_OneSlowMinuteIsTheMediansNot(t *testing.T) {
	prompts := steadyLevels(7, 8000, 30000, 37000)
	cs := curveMinutes(run3S0, run3B, run3C, prompts...)
	last := cs[len(cs)-1].Rows[0]
	cs[len(cs)-1].Rows[0] = lowLoad(37000, 1.25*last.AvgServiceTime, 0.5, 0.0025)
	est := run(NewObserver(), cs...)
	require.Empty(t, est.ARefused)
	assert.Equal(t, AModelQuadratic, est.AModel)
	assert.InDelta(t, run3S0, est.AIntercept, 1e-9)
}

// The quadratic solve is exact on a large scale and a small one, prompt
// lengths of a few thousandths to eight million.
func TestQuadratic_TheSolveIsExactAtEveryScale(t *testing.T) {
	for _, scale := range []float64{1e-3, 1, 1e3, 1e6} {
		xs := []float64{1 * scale, 2 * scale, 4 * scale, 8 * scale}
		ys := make([]float64, len(xs))
		for i, x := range xs {
			u := x / scale
			ys[i] = 0.5 + 0.25*u + 0.125*u*u
		}
		a, b, c := quadratic(xs, ys)
		assert.InDelta(t, 0.5, a, 1e-12, "scale %g", scale)
		assert.InDelta(t, 0.25, b*scale, 1e-12, "scale %g", scale)
		assert.InDelta(t, 0.125, c*scale*scale, 1e-12, "scale %g", scale)
	}
	a, b, c := quadratic([]float64{1, 1, 2, 2}, []float64{1, 1, 2, 2})
	assert.True(t, math.IsNaN(a) && math.IsNaN(b) && math.IsNaN(c), "two distinct x")
	a, _, _ = quadratic([]float64{0, 0, 0}, []float64{1, 2, 3})
	assert.True(t, math.IsNaN(a), "no x")
}

// run3Rows is testdata/run3-prefill-rows.csv: every prefill row the run 3
// recording of the TTFT track carries (one P/D prefill pod of Qwen3-0.6B at
// TP1 on an H200, swept at low load over 8k, 30k and 37k prompts, a cycle
// every 15 s), with the fields the observer and the settle tracker read.
// The recording itself stays out of the tree.
type run3Row struct {
	now time.Time
	rm  domain.ReplicaMetrics
}

func run3Rows(t *testing.T) []run3Row {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "run3-prefill-rows.csv"))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	recs, err := csv.NewReader(f).ReadAll()
	require.NoError(t, err)
	require.Equal(t, []string{"now", "ready", "ready_since", "kv", "queue", "request_rate", "computed_rate",
		"avg_input_recent", "service_time", "ttft"}, recs[0])
	num := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		require.NoError(t, err)
		return v
	}
	out := make([]run3Row, 0, len(recs)-1)
	for _, r := range recs[1:] {
		now, err := time.Parse(time.RFC3339Nano, r[0])
		require.NoError(t, err)
		since, err := time.Parse(time.RFC3339Nano, r[2])
		require.NoError(t, err)
		rm := base("prefill-0")
		rm.Ready, rm.ReadySince = r[1] == "true", since
		rm.KvCacheUsage = num(r[3])
		rm.QueueLength = int(num(r[4]))
		rm.RequestRate, rm.PrefillComputedTokenRate = num(r[5]), num(r[6])
		rm.AvgInputTokensRecent = num(r[7])
		rm.AvgServiceTime, rm.AvgTTFT = num(r[8]), num(r[9])
		out = append(out, run3Row{now: now, rm: rm})
	}
	return out
}

// replayRun3 feeds run 3's rows to an observer through a settle tracker at
// the recording's 15 s interval, as the controller observed them, and
// returns the last estimate.
func replayRun3(t *testing.T, lineOnly bool) Estimate {
	t.Helper()
	tr := settle.NewTracker()
	tr.SetInterval(15 * time.Second)
	roles := map[string]string{"p": domain.RolePrefill}
	o := NewObserver()
	var est Estimate
	for _, r := range run3Rows(t) {
		tr.Observe("ns/m", settlerow.Of([]domain.ReplicaMetrics{r.rm}, roles), r.now)
		est = o.Observe(Cycle{
			Key: "k", Engine: inferenceengine.EngineVLLM, Disaggregated: true, Rows: []domain.ReplicaMetrics{r.rm},
			Settle: &Settle{View: tr, Model: "ns/m"}, ALineOnly: lineOnly, Now: r.now,
		})
	}
	return est
}

// Run 3's real rows: the line P1 shipped refuses a (its intercept is about
// -55 ms over the fifteen admitted minutes), and the curve gives a positive
// intercept near the 2.8 ms of the quadratic through the medians of all 86
// low-load rows (curve.py): 3.69 ms, through the medians of the 5 admitted
// minutes of each level. The tolerance is 1.5 ms either side of 2.8 ms: the
// admitted minutes are a third of the rows, and their medians move the
// extrapolated intercept by about 1 ms against the 86 rows'. The curve's b
// and c are within 10% of curve.py's.
func TestQuadratic_Run3RowsGiveAPositiveIntercept(t *testing.T) {
	rows := run3Rows(t)
	require.Len(t, rows, 93, "the recording's prefill rows")

	off := replayRun3(t, true)
	assert.Equal(t, NonPositiveIntercept, off.ARefused, "the line, as P1 shipped")
	assert.Equal(t, AModelLinear, off.AModel)
	assert.Equal(t, 15, off.ASamples)

	est := replayRun3(t, false)
	require.Empty(t, est.ARefused)
	assert.Equal(t, AModelQuadratic, est.AModel)
	assert.Equal(t, 15, est.ASamples)
	assert.Equal(t, 3, est.AClusters)
	assert.Greater(t, est.AIntercept, 0.0)
	assert.InDelta(t, 0.0028, est.AIntercept, 0.0015, "a's intercept")
	assert.InEpsilon(t, run3B, est.ASlope, 0.1, "b")
	assert.InEpsilon(t, run3C, est.ACurve, 0.1, "c")
	assert.True(t, est.AOverheadKnown)
	assert.InDelta(t, est.AIntercept+est.AOverhead, est.A, 1e-12)
	// The validation bound is the line's, unchanged by the curve.
	assert.Equal(t, off.SlopeBound, est.SlopeBound)
	t.Logf("run 3: a %.5f s = intercept %.5f + overhead %.5f; b %.4e s/token, c %.4e s/token^2; %d minutes in %d clusters, prompts %.0f..%.0f; slope bound %.0f tok/s",
		est.A, est.AIntercept, est.AOverhead, est.ASlope, est.ACurve, est.ASamples, est.AClusters, est.APromptMin, est.APromptMax, est.SlopeBound)
}

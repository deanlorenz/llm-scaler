package prefill

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
)

// unknownView is a settle view that knows no replica.
type unknownView struct{ settle.View }

func (unknownView) Replica(string, settle.Key) (settle.Replica, bool) {
	return settle.Replica{}, false
}

var termT0 = time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)

// decodeRow is an admitted decode row: D as the test's truth at step itl.
func decodeRow(pod string, ttft, itl, prompt float64) domain.ReplicaMetrics {
	return domain.ReplicaMetrics{
		PodName: pod, Ready: true, AvgTTFT: ttft, AvgTTFTKnown: true, AvgITL: itl, AvgITLKnown: true,
		RequestRate: 5, RequestRateKnown: true, AvgInputTokensRecent: prompt, AvgInputTokens: prompt,
	}
}

// feedMinutes gives a window one row a minute, each from row(i).
func feedMinutes(w *DecodeTermWindow, n int, row func(i int) domain.ReplicaMetrics) {
	for i := range n {
		w.Observe(DecodeTermCycle{Now: termT0.Add(time.Duration(i) * time.Minute), Decode: []domain.ReplicaMetrics{row(i)}, PromptTokens: 1000})
	}
}

// truth is the D the fits below must recover: not the staircase's, so that no
// figure of that run can pass for a learned one.
func truth(i, itl float64) float64 { return 0.002 + 3e-6*i + 5*itl }

// D is recovered from a sweep of both: the three terms.
func TestDecodeFit_RecoversThePlane(t *testing.T) {
	w := NewDecodeTermWindow()
	prompts := []float64{1000, 2000, 4000, 8000}
	feedMinutes(w, 40, func(i int) domain.ReplicaMetrics {
		itl, p := 0.002+float64(i%7)*0.002, prompts[i%4]
		return decodeRow("d", truth(p, itl), itl, p)
	})
	f := w.Estimate().Decode
	require.Empty(t, f.Refused)
	assert.Equal(t, 40, f.Minutes)
	assert.InDelta(t, 0.002, f.Base, 1e-9)
	assert.InDelta(t, 3e-6, f.PerToken, 1e-12)
	assert.InDelta(t, 5, f.PerStep, 1e-6)
	assert.Zero(t, f.HeldPromptTokens)
}

// One prompt length cannot tell base from perToken: base stands for both, held
// at the shortest prompt read, which the caller scales from.
func TestDecodeFit_OnePromptIsHeld(t *testing.T) {
	w := NewDecodeTermWindow()
	feedMinutes(w, 20, func(i int) domain.ReplicaMetrics {
		itl := 0.002 + float64(i%5)*0.002
		p := 8000 + float64(i%3)*100
		return decodeRow("d", truth(8000, itl), itl, p)
	})
	f := w.Estimate().Decode
	require.Empty(t, f.Refused)
	assert.InDelta(t, 0.026, f.Base, 1e-9)
	assert.InDelta(t, 5, f.PerStep, 1e-6)
	assert.Zero(t, f.PerToken)
	assert.Equal(t, 8000.0, f.HeldPromptTokens)
}

// A fleet at flat load has no slope to read, and says so, however long it runs.
func TestDecodeFit_FlatLoadIsRefused(t *testing.T) {
	w := NewDecodeTermWindow()
	feedMinutes(w, 60, func(i int) domain.ReplicaMetrics {
		return decodeRow("d", 0.02+float64(i%2)*1e-4, 0.003+float64(i%2)*1e-4, 1000)
	})
	f := w.Estimate().Decode
	assert.Equal(t, NoStepSpread, f.Refused)
	assert.Zero(t, f.PerStep, "a refused fit carries no slope")
}

func TestDecodeFit_TooFewMinutes(t *testing.T) {
	w := NewDecodeTermWindow()
	feedMinutes(w, MinDecodeMinutes-1, func(i int) domain.ReplicaMetrics {
		itl := 0.002 + float64(i)*0.002
		return decodeRow("d", truth(1000, itl), itl, 1000)
	})
	assert.Equal(t, TooFewDecodeMinutes, w.Estimate().Decode.Refused)
	w = NewDecodeTermWindow()
	feedMinutes(w, MinDecodeMinutes, func(i int) domain.ReplicaMetrics {
		itl := 0.002 + float64(i)*0.002
		return decodeRow("d", truth(1000, itl), itl, 1000)
	})
	assert.Empty(t, w.Estimate().Decode.Refused)
}

// The spread is read inside the ends: one stray minute does not open it.
func TestDecodeFit_OneStrayMinuteIsNoSpread(t *testing.T) {
	w := NewDecodeTermWindow()
	feedMinutes(w, 20, func(i int) domain.ReplicaMetrics {
		itl := 0.003
		if i == 3 {
			itl = 0.03
		}
		return decodeRow("d", truth(1000, itl), itl, 1000)
	})
	assert.Equal(t, NoStepSpread, w.Estimate().Decode.Refused)
}

// A slope, prompt or step, that reads below zero is no fit of what the term is.
func TestDecodeFit_RefusesWhatCannotBe(t *testing.T) {
	steep := func(ttft func(itl, p float64) float64, prompts ...float64) DecodeFit {
		w := NewDecodeTermWindow()
		feedMinutes(w, 40, func(i int) domain.ReplicaMetrics {
			itl, p := 0.002+float64(i%7)*0.002, prompts[i%len(prompts)]
			return decodeRow("d", ttft(itl, p), itl, p)
		})
		return w.Estimate().Decode
	}
	assert.Equal(t, NonPositiveStepSlope, steep(func(itl, _ float64) float64 { return 0.1 - 2*itl }, 1000).Refused)
	assert.Equal(t, NonPositiveDecodeBase, steep(func(itl, _ float64) float64 { return 5*itl - 0.005 }, 1000).Refused)
	assert.Equal(t, NonPositiveStepSlope, steep(func(itl, p float64) float64 { return 0.1 + 1e-6*p - 2*itl }, 1000, 2000, 4000).Refused)
	assert.Equal(t, NegativePromptSlope, steep(func(itl, p float64) float64 { return 0.1 - 1e-6*p + 2*itl }, 1000, 2000, 4000).Refused)
}

// Each gate of a row, alone: the row it refuses is otherwise admitted, and a
// row a gate let through would pull the fit (a slope of 5) off it.
func TestDecodeRow_TheGates(t *testing.T) {
	good := decodeRow("d", 0.02, 0.003, 1000)
	_, _, _, ok := admittedDecodeRow(good)
	require.True(t, ok)
	for name, spoil := range map[string]func(*domain.ReplicaMetrics){
		"not ready":          func(r *domain.ReplicaMetrics) { r.Ready = false },
		"ttft unread":        func(r *domain.ReplicaMetrics) { r.AvgTTFTKnown = false },
		"ttft NaN":           func(r *domain.ReplicaMetrics) { r.AvgTTFT = math.NaN() },
		"ttft Inf":           func(r *domain.ReplicaMetrics) { r.AvgTTFT = math.Inf(1) },
		"itl unread":         func(r *domain.ReplicaMetrics) { r.AvgITLKnown = false },
		"itl NaN":            func(r *domain.ReplicaMetrics) { r.AvgITL = math.NaN() },
		"speculating":        func(r *domain.ReplicaMetrics) { r.SpecAcceptedPerStep = 0.5 },
		"a slot queue":       func(r *domain.ReplicaMetrics) { r.CapacityQueueLength = 1 },
		"behind on pulls":    func(r *domain.ReplicaMetrics) { r.QueueLength = MaxDecodeQueue + 1 },
		"rate unread":        func(r *domain.ReplicaMetrics) { r.RequestRateKnown = false },
		"too few requests":   func(r *domain.ReplicaMetrics) { r.RequestRate = MinDecodeRequestRate / 2 },
		"rate NaN":           func(r *domain.ReplicaMetrics) { r.RequestRate = math.NaN() },
		"prompt moved":       func(r *domain.ReplicaMetrics) { r.AvgInputTokens = 1000 / (1 + MaxDecodePromptDrift + 0.01) },
		"prompt moved, down": func(r *domain.ReplicaMetrics) { r.AvgInputTokensRecent = 800 },
		"prompt NaN":         func(r *domain.ReplicaMetrics) { r.AvgInputTokensRecent = math.NaN() },
		"no prompt":          func(r *domain.ReplicaMetrics) { r.AvgInputTokens = 0 },
	} {
		r := good
		spoil(&r)
		_, _, _, ok := admittedDecodeRow(r)
		assert.False(t, ok, name)
	}
	r := good
	r.QueueLength = MaxDecodeQueue
	_, _, _, ok = admittedDecodeRow(r)
	assert.True(t, ok, "the pull in transit is D")
}

// The row's own prompt of the minute, not the cycle's: on a ramp the two part.
func TestDecodeRow_ReadsItsOwnPrompt(t *testing.T) {
	w := NewDecodeTermWindow()
	w.Observe(DecodeTermCycle{Now: termT0, Decode: []domain.ReplicaMetrics{decodeRow("d", 0.02, 0.003, 1000)}, PromptTokens: 7000})
	require.Len(t, w.decode, 1)
	assert.Equal(t, 1000.0, w.decode[0].prompt)
}

// Two rows of a pod inside a minute share most of their window: one sample.
func TestDecodeWindow_SpacesAPodsSamples(t *testing.T) {
	w := NewDecodeTermWindow()
	for i := range 4 {
		w.Observe(DecodeTermCycle{Now: termT0.Add(time.Duration(i) * 15 * time.Second), PromptTokens: 1000,
			Decode: []domain.ReplicaMetrics{decodeRow("a", 0.02, 0.003, 1000), decodeRow("b", 0.02, 0.003, 1000)}})
	}
	assert.Len(t, w.decode, 2, "t0 only: the next minute starts at 60 s")
	w.Observe(DecodeTermCycle{Now: termT0.Add(time.Minute), PromptTokens: 1000, Decode: []domain.ReplicaMetrics{decodeRow("a", 0.02, 0.003, 1000)}})
	assert.Len(t, w.decode, 3)
}

// A replica still warming up is not admitted: the settle view reads it.
func TestDecodeWindow_ReadsTheSettleView(t *testing.T) {
	w := NewDecodeTermWindow()
	unknown := &Settle{View: unknownView{}, Model: "m"}
	w.Observe(DecodeTermCycle{Now: termT0, PromptTokens: 1000, Settled: unknown, Decode: []domain.ReplicaMetrics{decodeRow("a", 0.02, 0.003, 1000)}})
	assert.Empty(t, w.decode)
}

// What is older than Retention leaves the fit, and the bounds hold.
func TestDecodeWindow_Ages(t *testing.T) {
	w := NewDecodeTermWindow()
	feedMinutes(w, 5, func(int) domain.ReplicaMetrics { return decodeRow("d", 0.02, 0.003, 1000) })
	require.Len(t, w.decode, 5)
	w.Observe(DecodeTermCycle{Now: termT0.Add(Retention + 10*time.Minute), PromptTokens: 1000})
	assert.Empty(t, w.decode)
	assert.Empty(t, w.lastAt)
}

func TestDecodeWindow_NoPromptAdmitsNothing(t *testing.T) {
	w := NewDecodeTermWindow()
	for _, p := range []float64{0, math.NaN(), math.Inf(1)} {
		w.Observe(DecodeTermCycle{Now: termT0, PromptTokens: p, Decode: []domain.ReplicaMetrics{decodeRow("a", 0.02, 0.003, 1000)}})
	}
	assert.Empty(t, w.decode)
}

// hopCycle is one cycle of a steady fleet: engines p and d, the router's mean
// over its window, rate requests a second.
func hopCycle(at time.Time, p, d, router, rate, prompt float64) DecodeTermCycle {
	pre := domain.ReplicaMetrics{PodName: "p", Ready: true, AvgTTFT: p, AvgTTFTKnown: true, RequestRate: rate, RequestRateKnown: true}
	dec := domain.ReplicaMetrics{PodName: "d", Ready: true, AvgTTFT: d, AvgTTFTKnown: true, RequestRate: rate, RequestRateKnown: true}
	var rl *domain.RouterLatency
	if router > 0 {
		rl = &domain.RouterLatency{WindowSeconds: 300, TTFTSumRate: router * rate, TTFTCountRate: rate}
	}
	return DecodeTermCycle{Now: at, Prefill: []domain.ReplicaMetrics{pre}, Decode: []domain.ReplicaMetrics{dec}, PromptTokens: prompt, Router: rl}
}

// hopRun runs a steady fleet for minutes, a cycle every 15 s, from start.
func hopRun(w *DecodeTermWindow, start time.Time, minutes int, r, prompt float64) time.Time {
	at := start
	for range minutes * 4 {
		w.Observe(hopCycle(at, 0.05, 0.03, 0.08+r, 10, prompt))
		at = at.Add(15 * time.Second)
	}
	return at
}

// R is the router's mean less the engines': held at one prompt.
func TestHop_OnePromptIsHeld(t *testing.T) {
	w := NewDecodeTermWindow()
	hopRun(w, termT0, 15, 0.007, 8000)
	f := w.Estimate().Hop
	require.Empty(t, f.Refused)
	assert.InDelta(t, 0.007, f.Base, 1e-9)
	assert.Zero(t, f.PerToken)
	assert.Equal(t, 8000.0, f.HeldPromptTokens)
	assert.GreaterOrEqual(t, f.Windows, MinHopWindows)
}

// Two prompt lengths give the line through their medians.
func TestHop_TwoPromptsGiveALine(t *testing.T) {
	w := NewDecodeTermWindow()
	at := hopRun(w, termT0, 15, 0.005, 1000)
	hopRun(w, at, 15, 0.012, 8000)
	f := w.Estimate().Hop
	require.Empty(t, f.Refused)
	assert.InDelta(t, 1e-6, f.PerToken, 1e-9)
	assert.InDelta(t, 0.004, f.Base, 1e-6)
	assert.Zero(t, f.HeldPromptTokens)
}

// A window a queue or a ramp spoiled moves the median by nothing: one in ten
// reading a second off.
func TestHop_TheMedianHoldsAgainstOneWindow(t *testing.T) {
	var s []hopSample
	for i := range 21 {
		r := 0.007
		if i%10 == 0 {
			r = 1
		}
		s = append(s, hopSample{at: termT0, r: r, prompt: 1000})
	}
	f := fitHop(s)
	require.Empty(t, f.Refused)
	assert.Equal(t, 0.007, f.Base)
}

// A hop that falls with the prompt is held at the largest median, not priced
// down: the side that asks for more.
func TestHop_AFallingLineIsHeldHigh(t *testing.T) {
	var s []hopSample
	for range 5 {
		s = append(s, hopSample{r: 0.02, prompt: 1000}, hopSample{r: 0.01, prompt: 8000})
	}
	f := fitHop(s)
	require.Empty(t, f.Refused)
	assert.Equal(t, 0.02, f.Base)
	assert.Equal(t, 1000.0, f.HeldPromptTokens)
	assert.Zero(t, f.PerToken)
}

func TestHop_Refusals(t *testing.T) {
	w := NewDecodeTermWindow()
	assert.Equal(t, TooFewHopWindows, w.Estimate().Hop.Refused)
	hopRun(w, termT0, 15, -0.002, 1000)
	assert.Equal(t, NonPositiveHop, w.Estimate().Hop.Refused, "the engines already hold the router's mean")
}

// No window stands before the cycles span the router's, and a window with a
// ramp in it is no reading: its two means weigh different requests.
func TestHop_TheWindowGates(t *testing.T) {
	short := NewDecodeTermWindow()
	for i := range 8 {
		short.Observe(hopCycle(termT0.Add(time.Duration(i)*15*time.Second), 0.05, 0.03, 0.09, 10, 1000))
	}
	assert.Empty(t, short.hops, "two minutes of a five-minute window")

	for name, cycle := range map[string]func(i int, at time.Time) DecodeTermCycle{
		"rate ramp": func(i int, at time.Time) DecodeTermCycle {
			return hopCycle(at, 0.05, 0.03, 0.09, 5+float64(i), 1000)
		},
		"prompt ramp": func(i int, at time.Time) DecodeTermCycle {
			return hopCycle(at, 0.05, 0.03, 0.09, 10, 1000+100*float64(i))
		},
		"too few requests": func(_ int, at time.Time) DecodeTermCycle {
			return hopCycle(at, 0.05, 0.03, 0.09, MinHopRequestRate/2, 1000)
		},
	} {
		w := NewDecodeTermWindow()
		for i := range 40 {
			w.Observe(cycle(i, termT0.Add(time.Duration(i)*15*time.Second)))
		}
		assert.Empty(t, w.hops, name)
	}
}

// With no router mean there is nothing to subtract from, and half a pair is
// not one: buckets alone leave R unlearned.
func TestHop_NoRouterMeanNoHop(t *testing.T) {
	w := NewDecodeTermWindow()
	for i := range 80 {
		c := hopCycle(termT0.Add(time.Duration(i)*15*time.Second), 0.05, 0.03, 0.09, 10, 1000)
		c.Router.TTFTSumRate = 0
		w.Observe(c)
	}
	assert.Empty(t, w.hops)
	assert.Equal(t, TooFewHopWindows, w.Estimate().Hop.Refused)
}

// The windows are spaced as the rows are: one reading a minute.
func TestHop_OneWindowAMinute(t *testing.T) {
	w := NewDecodeTermWindow()
	hopRun(w, termT0, 10, 0.007, 1000)
	assert.LessOrEqual(t, len(w.hops), 6, "minutes 5 to 10")
	assert.GreaterOrEqual(t, len(w.hops), 5)
}

func TestMeanStep(t *testing.T) {
	assert.Zero(t, MeanStep(nil))
	rows := []domain.ReplicaMetrics{decodeRow("a", 0.02, 0.002, 1000), decodeRow("b", 0.02, 0.004, 1000),
		{Ready: true, AvgITL: 9}, {AvgITL: 9, AvgITLKnown: true}}
	assert.InDelta(t, 0.003, MeanStep(rows), 1e-12)
}

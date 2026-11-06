package whatif

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
)

// stateTerm is a learned term with figures of no recording: D = 2 ms +
// 3e-6*I + 5*ITL, R = 4 ms + 1e-6*I.
func stateTerm() *DecodeTerm {
	return &DecodeTerm{Engine: PromptLine{Base: 0.002, PerToken: 3e-6}, PerStep: 5,
		Hop: PromptLine{Base: 0.004, PerToken: 1e-6}}
}

func termFleet(t *DecodeTerm) TTFTFleet {
	fl := workedFleet()
	fl.DecodeTerm = t
	return fl
}

// The term replaces T_xfer + ITL(k): the hand sum at the fill, T_xfer gone, the
// per-token parts in the own latency, and TransferOmitted lifted, since D holds
// the pull.
func TestTerm_PricesTheHandSum(t *testing.T) {
	fl := termFleet(stateTerm())
	fl.Prefill.TransferSecondsPerToken = 1e-3 // would be seconds, were it priced
	const rate = 8.0
	v := mustTTFT(t, ttftAt(t, fl, rate, 0))
	d := *fl.Decode
	k, ok := d.fillAtReplicaRate(rate / float64(d.Replicas))
	if !ok {
		t.Fatal("no fill")
	}
	i := fl.Prefill.PromptTokens
	want := v.Parts.EngineWait + v.Parts.DecodeWait + fl.Prefill.FixedLatency + i/fl.Prefill.VP +
		0.002 + 3e-6*i + 5*d.Line.ITLAt(k) + 0.004 + 1e-6*i
	relNear(t, "mean", v.Seconds, want, 1e-12)
	relNear(t, "D's fixed part", v.Parts.DecodeEngine, 0.002+5*d.Line.ITLAt(k), 1e-12)
	relNear(t, "R's fixed part", v.Parts.RouterHop, 0.004, 1e-12)
	relNear(t, "own", v.Parts.OwnPrefill, fl.Prefill.FixedLatency+i/fl.Prefill.VP+4e-6*i, 1e-9)
	if v.Parts.FirstStep != 0 || v.HasFlag(TransferOmitted) || v.HasFlag(DecodeTermOmitted) || v.HasFlag(DecodeStepSlopeAssumed) || v.HasFlag(RouterHopAssumed) {
		t.Errorf("first step %g, flags %v", v.Parts.FirstStep, v.Flags)
	}
	if !v.HasFlag(UnvalidatedTTFT) {
		t.Errorf("UnvalidatedTTFT stays: %v", v.Flags)
	}
}

// The line is read at the actual fill below the band, and at fill 0 for the
// floor: at the clamp the staircase's bucket A overshot its floor by 28 ms. The
// flag stands: below the band the line is an extrapolation.
func TestTerm_ReadsTheLineAtTheActualFill(t *testing.T) {
	fl := termFleet(stateTerm())
	d := *fl.Decode
	k, ok := d.fillAtReplicaRate(0.5 / float64(d.Replicas))
	if !ok || !(k < d.Band.Lo) {
		t.Fatalf("want a fill under the band's bottom, got %g", k)
	}
	v := mustTTFT(t, ttftAt(t, fl, 0.5, 0))
	relNear(t, "D at the actual fill", v.Parts.DecodeEngine, 0.002+5*d.Line.ITLAt(k), 1e-12)
	if !v.HasFlag(BelowFittedBand) {
		t.Errorf("flags %v", v.Flags)
	}
	i := fl.Prefill.PromptTokens
	relNear(t, "floor", v.Floor, fl.Prefill.FixedLatency+i/fl.Prefill.VP+0.002+3e-6*i+5*d.Line.B+0.004+1e-6*i, 1e-9)
}

// A target between the old floor and the corrected one refuses, where the old
// sum gave a rate: the goal of the term.
func TestTerm_ATargetBelowTheCorrectedFloorRefuses(t *testing.T) {
	old := workedFleet()
	old.LegacyDecodeSum = true
	oldFloor := mustTTFT(t, ttftAt(t, old, 1, 0)).Floor
	fl := termFleet(stateTerm())
	newFloor := mustTTFT(t, ttftAt(t, fl, 1, 0)).Floor
	if !(newFloor > oldFloor*1.1) {
		t.Fatalf("floors %g, %g", oldFloor, newFloor)
	}
	target := TTFTTarget{Seconds: (oldFloor + newFloor) / 2}
	if a := mustAnswer(t, rateForTTFT(t, old, target)); !(a.Value > 0) {
		t.Errorf("the old sum gave %g", a.Value)
	}
	u, ok := rateForTTFT(t, fl, target).(Unanswerable)
	if !ok || u.Reason != TargetBelowFloor {
		t.Errorf("want TargetBelowFloor, got %#v", rateForTTFT(t, fl, target))
	}
	if a := mustAnswer(t, rateForTTFT(t, fl, TTFTTarget{Seconds: newFloor * 1.5})); !(a.Value > 0) {
		t.Errorf("a target above the floor: %g", a.Value)
	}
}

// TTFT still rises with the rate, so the bisection stands, at each shape.
func TestTerm_RisesWithTheRate(t *testing.T) {
	for _, term := range []*DecodeTerm{stateTerm(), nil,
		{Engine: PromptLine{Base: 0.04, HeldPromptTokens: 500}, PerStep: AssumedDecodeStepSlope, FromITL: 0.01, StepAssumed: true}} {
		fl := termFleet(term)
		pl, u := fl.plan()
		if u != nil {
			t.Fatal(u)
		}
		last := 0.0
		for f := 0.0; f < 0.99; f += 0.02 {
			v := mustTTFT(t, ttftAt(t, fl, f*pl.lowestKnee(), 0.9))
			if v.Seconds < last-1e-12 {
				t.Fatalf("%+v: TTFT fell to %g from %g at %g of the knee", term, v.Seconds, last, f)
			}
			last = v.Seconds
		}
	}
}

// With no term the old sum stands, and says so: never silently. The figures are
// the old sum's to the last digit.
func TestTerm_NoTermIsFlagged(t *testing.T) {
	old := workedFleet()
	old.LegacyDecodeSum = true
	nilTerm := workedFleet()
	for _, q := range []float64{0, 0.5, 0.9, 0.99} {
		o, n := mustTTFT(t, ttftAt(t, old, 8, q)), mustTTFT(t, ttftAt(t, nilTerm, 8, q))
		if o.Seconds != n.Seconds || o.Floor != n.Floor {
			t.Errorf("q %g: %g, %g; floors %g, %g", q, o.Seconds, n.Seconds, o.Floor, n.Floor)
		}
		if o.HasFlag(DecodeTermOmitted) || !n.HasFlag(DecodeTermOmitted) {
			t.Errorf("q %g: flags %v / %v", q, o.Flags, n.Flags)
		}
	}
}

// A fall-back never prices under the old sum, whatever the transfer, the
// measured figure, the prompt or the rate: a term with no fit of D may only
// add, and its flag is on the answer.
func TestTerm_AFallBackIsNeverUnderTheOldSum(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
	for range 2000 {
		fl := workedFleet()
		fl.Prefill.PromptTokens = 200 + 9000*r.Float64()
		fl.Prefill.TransferSecondsPerToken = 1e-5 * r.Float64()
		term := DecodeTerm{Engine: PromptLine{Base: 0.2 * r.Float64() * r.Float64(), HeldPromptTokens: 200 + 9000*r.Float64()},
			PerStep: AssumedDecodeStepSlope, FromITL: 0.02 * r.Float64(), StepAssumed: true, HopAssumed: true,
			Hop: PromptLine{Base: AssumedRouterHopSeconds}}
		if r.IntN(3) == 0 {
			term = DecodeTerm{EngineOmitted: true, HopAssumed: true, Hop: PromptLine{Base: AssumedRouterHopSeconds}}
		}
		fl.DecodeTerm = &term
		old := fl
		old.DecodeTerm, old.LegacyDecodeSum = nil, true
		pl, u := fl.plan()
		if u != nil {
			t.Fatal(u)
		}
		rate := 0.9 * r.Float64() * pl.lowestKnee()
		for _, q := range []float64{0, 0.5, 0.9, 0.99} {
			v, o := mustTTFT(t, ttftAt(t, fl, rate, q)), mustTTFT(t, ttftAt(t, old, rate, q))
			if v.Seconds < o.Seconds*(1-1e-12) || v.Floor < o.Floor*(1-1e-12) {
				t.Fatalf("%+v at %g, q %g: %g under the old sum's %g (floors %g, %g)", term, rate, q, v.Seconds, o.Seconds, v.Floor, o.Floor)
			}
			if !v.HasFlag(RouterHopAssumed) || (!v.HasFlag(DecodeStepSlopeAssumed) && !v.HasFlag(DecodeTermOmitted)) {
				t.Fatalf("flags %v", v.Flags)
			}
		}
	}
}

// An omitted D prices the old step and the transfer the caller gave, and only
// that one keeps TransferOmitted: a measured D holds the pull.
func TestTerm_TransferOmittedStandsOnlyWithNoD(t *testing.T) {
	om := mustTTFT(t, ttftAt(t, termFleet(&DecodeTerm{EngineOmitted: true}), 8, 0))
	if !om.HasFlag(TransferOmitted) || om.Parts.FirstStep == 0 || om.Parts.DecodeEngine != 0 {
		t.Errorf("%+v, %v", om.Parts, om.Flags)
	}
	held := mustTTFT(t, ttftAt(t, termFleet(&DecodeTerm{Engine: PromptLine{Base: 0.05, HeldPromptTokens: 1000}, PerStep: 10, StepAssumed: true}), 8, 0))
	if held.HasFlag(TransferOmitted) || held.Parts.DecodeEngine == 0 {
		t.Errorf("%+v, %v", held.Parts, held.Flags)
	}
}

// The measured D holds the pull, and the own latency still holds T_xfer on a
// fall-back: the two are not added. The mean carries D once.
func TestTerm_AFallBackCountsThePullOnce(t *testing.T) {
	plain := termFleet(&DecodeTerm{Engine: PromptLine{Base: 0.05, HeldPromptTokens: 1000}, PerStep: 10, FromITL: 1, StepAssumed: true})
	with := plain
	with.Prefill.TransferSecondsPerToken = 1e-5 // 10 ms at the 1000 tokens, under D
	a, b := mustTTFT(t, ttftAt(t, plain, 2, 0)), mustTTFT(t, ttftAt(t, with, 2, 0))
	relNear(t, "the mean with T_xfer beside D", b.Seconds, a.Seconds, 1e-12)
}

// An assumed slope adds only above the ITL D was measured at: below it D is
// held, the higher figure, and the fit's FromITL of 0 adds the whole step.
func TestTerm_AnAssumedSlopeClimbsFromItsReading(t *testing.T) {
	tp := termPlan{engine: 0.05, perStep: 10, fromITL: 0.004}
	engine, _ := tp.at(0.003, 0)
	relNear(t, "held below", engine, 0.05, 0)
	engine, _ = tp.at(0.006, 0)
	relNear(t, "climbs above", engine, 0.07, 1e-12)
	tp.fromITL = 0
	engine, _ = tp.at(0.006, 0)
	relNear(t, "a fit adds it all", engine, 0.11, 1e-12)
}

func TestPromptLine_Split(t *testing.T) {
	l := PromptLine{Base: 0.04, HeldPromptTokens: 2000}
	if f, p := l.split(1000); f != 0.04 || p != 0 {
		t.Errorf("held for a shorter prompt: %g, %g", f, p)
	}
	if f, p := l.split(4000); f != 0 || p != 2e-5 {
		t.Errorf("scaled up for a longer one: %g, %g", f, p)
	}
	if f, p := (PromptLine{Base: 0.01, PerToken: 3e-6}).split(4000); f != 0.01 || p != 3e-6 {
		t.Errorf("a line: %g, %g", f, p)
	}
}

// A negative fitted intercept with a small step never prices a negative term.
func TestTerm_NeverNegative(t *testing.T) {
	v := mustTTFT(t, ttftAt(t, termFleet(&DecodeTerm{Engine: PromptLine{Base: -5, PerToken: 1e-6}, PerStep: 1, Hop: PromptLine{Base: -1}}), 0.5, 0))
	if v.Parts.DecodeEngine != 0 || v.Parts.RouterHop != 0 {
		t.Errorf("%+v", v.Parts)
	}
}

func TestTermFromEstimate(t *testing.T) {
	measured := DecodeTTFT{Seconds: 0.05, PromptTokens: 1000, ITLSeconds: 0.004}
	fit := prefill.DecodeFit{Base: 0.002, PerToken: 3e-6, PerStep: 5, Minutes: 40}
	hop := prefill.HopFit{Base: 0.004, PerToken: 1e-6, Windows: 9}

	got := TermFromEstimate(prefill.DecodeTermEstimate{Decode: fit, Hop: hop}, measured)
	want := DecodeTerm{Engine: PromptLine{Base: 0.002, PerToken: 3e-6}, PerStep: 5, Hop: PromptLine{Base: 0.004, PerToken: 1e-6}}
	if got != want {
		t.Errorf("learned: %+v", got)
	}

	got = TermFromEstimate(prefill.DecodeTermEstimate{Decode: prefill.DecodeFit{Refused: prefill.NoStepSpread}}, measured)
	want = DecodeTerm{Engine: PromptLine{Base: 0.05, HeldPromptTokens: 1000}, PerStep: AssumedDecodeStepSlope, FromITL: 0.004,
		StepAssumed: true, Hop: PromptLine{Base: AssumedRouterHopSeconds}, HopAssumed: true}
	if got != want {
		t.Errorf("refused, measured: %+v", got)
	}

	got = TermFromEstimate(prefill.DecodeTermEstimate{}, DecodeTTFT{})
	want = DecodeTerm{EngineOmitted: true, Hop: PromptLine{Base: AssumedRouterHopSeconds}, HopAssumed: true}
	if got != want {
		t.Errorf("nothing: %+v", got)
	}
	if got := TermFromEstimate(prefill.DecodeTermEstimate{Hop: prefill.HopFit{Windows: 9, Refused: prefill.NonPositiveHop}}, DecodeTTFT{}); !got.HopAssumed {
		t.Errorf("a refused hop is assumed: %+v", got)
	}
}

func TestTerm_RefusesWhatNoReadingGives(t *testing.T) {
	for _, term := range []DecodeTerm{
		{Engine: PromptLine{Base: math.NaN()}},
		{Engine: PromptLine{PerToken: math.Inf(1)}},
		{Hop: PromptLine{Base: math.NaN()}},
		{PerStep: math.Inf(1)},
		{PerStep: -1},
		{FromITL: -1},
		{Engine: PromptLine{PerToken: -1}},
		{Hop: PromptLine{PerToken: -1}},
		{Engine: PromptLine{HeldPromptTokens: -1}},
		{Hop: PromptLine{HeldPromptTokens: -1}},
	} {
		term := term
		u, ok := TTFTAt(termFleet(&term), 1, 0.9).(Unanswerable)
		if !ok || u.Reason != InvalidInput {
			t.Errorf("%+v: %#v", term, TTFTAt(termFleet(&term), 1, 0.9))
		}
	}
}

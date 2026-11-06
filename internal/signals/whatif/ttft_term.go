package whatif

import (
	"fmt"
	"slices"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
)

// PromptLine is a term in the prompt, Base + PerToken*I, in seconds. With
// HeldPromptTokens the prompts it was read at could not separate the two, so
// Base stands for the whole term at that prompt and is priced as
// DecodeTTFT.at is: held for a shorter prompt and scaled up for a longer one.
// Both read high.
type PromptLine struct {
	Base, PerToken, HeldPromptTokens float64
}

// split is the line at a prompt of i tokens, as a fixed part and a part per
// token: the second goes in the request's own latency, as T_xfer does, so it
// follows the prompt's quantile.
func (l PromptLine) split(i float64) (fixed, perToken float64) {
	if l.HeldPromptTokens > 0 {
		if i > l.HeldPromptTokens {
			return 0, l.Base / l.HeldPromptTokens
		}
		return l.Base, 0
	}
	return l.Base, l.PerToken
}

// DecodeTerm is decode's part of a TTFT on a decode line
// (docs/proposals/slo-capacity-planner-ttft.md, "As built: the decode term"),
// in place of T_xfer + ITL(k):
//
//	D(I, ITL) + R = Engine(I) + PerStep*max(ITL(k) - FromITL, 0) + Hop(I)
//
// ITL(k) is the line at the fill the rate runs at, with no clamp to the band.
// TermFromEstimate builds it from what the variant learned, flagged when
// assumed.
type DecodeTerm struct {
	// Engine is D at no step time, and PerStep its seconds of TTFT per second
	// of ITL. FromITL is 0 on a fit. On a measured D held at the ITL it was
	// measured at, FromITL is that ITL: only a step time above it adds.
	Engine           PromptLine
	PerStep, FromITL float64
	// Hop is R, the router's mean less the engines'.
	Hop PromptLine
	// StepAssumed is a D with no fit: Engine is the decode engine's measured
	// TTFT and PerStep AssumedDecodeStepSlope. EngineOmitted is no D at all.
	// With either the old sum stands under the term (termPlan.at).
	StepAssumed, EngineOmitted bool
	// HopAssumed is an R with no fit: Hop.Base is AssumedRouterHopSeconds.
	HopAssumed bool
}

// TermFromEstimate is the term a decode variant supports. D is the fit; with
// none, the decode engine's measured TTFT, held, climbing from the ITL it was
// measured at by AssumedDecodeStepSlope, flagged DecodeStepSlopeAssumed; with
// neither, omitted, flagged DecodeTermOmitted. R is the fit, or else
// AssumedRouterHopSeconds, flagged RouterHopAssumed. An unknown input takes
// the value that asks for more, and is flagged, as the callers' other inputs
// do (slo.ttftFleet).
func TermFromEstimate(est prefill.DecodeTermEstimate, measured DecodeTTFT) DecodeTerm {
	var t DecodeTerm
	switch f := est.Decode; {
	case f.Refused == "" && f.PerStep > 0:
		t.Engine = PromptLine{Base: f.Base, PerToken: f.PerToken, HeldPromptTokens: f.HeldPromptTokens}
		t.PerStep = f.PerStep
	case measured.Seconds > 0 && measured.PromptTokens >= MinDecodeTTFTPrompt:
		t.Engine = PromptLine{Base: measured.Seconds, HeldPromptTokens: measured.PromptTokens}
		t.PerStep, t.FromITL, t.StepAssumed = AssumedDecodeStepSlope, measured.ITLSeconds, true
	default:
		t.EngineOmitted = true
	}
	if h := est.Hop; h.Refused == "" && h.Windows > 0 {
		t.Hop = PromptLine{Base: h.Base, PerToken: h.PerToken, HeldPromptTokens: h.HeldPromptTokens}
	} else {
		t.Hop, t.HopAssumed = PromptLine{Base: AssumedRouterHopSeconds}, true
	}
	return t
}

// check refuses a term with a figure no reading gives.
func (t DecodeTerm) check() *Unanswerable {
	for _, x := range []float64{t.Engine.Base, t.Engine.PerToken, t.Engine.HeldPromptTokens, t.Hop.Base, t.Hop.PerToken,
		t.Hop.HeldPromptTokens, t.PerStep, t.FromITL} {
		if !Finite(x) {
			u := refuse(InvalidInput, fmt.Sprintf("decode term %+v", t))
			return &u
		}
	}
	if t.PerStep < 0 || t.FromITL < 0 || t.Engine.PerToken < 0 || t.Hop.PerToken < 0 ||
		t.Engine.HeldPromptTokens < 0 || t.Hop.HeldPromptTokens < 0 {
		u := refuse(InvalidInput, fmt.Sprintf("decode term %+v", t))
		return &u
	}
	return nil
}

// termPlan is a checked term at the question's prompt.
type termPlan struct {
	// engine and hop are the fixed parts, seconds.
	engine, hop float64
	perStep     float64
	fromITL     float64
	// keepsOldSum is a term with no fit of D: it never prices under T_xfer +
	// ITL at the clamped fill, and transfer, the T_xfer the own latency
	// holds, is what it subtracts to compare. omitted is no D at all: the
	// step is the old sum's own first step.
	keepsOldSum, omitted bool
	transfer             float64
}

// termOn fits the term to the plan's prompt, moves the per-token parts to the
// own latency and raises the term's flags.
func termOn(t DecodeTerm, pf *prefillPlan) *termPlan {
	i := pf.p.PromptTokens
	engine, engineToken := t.Engine.split(i)
	hop, hopToken := t.Hop.split(i)
	tp := &termPlan{engine: engine, hop: hop, perStep: t.PerStep, fromITL: t.FromITL, omitted: t.EngineOmitted}
	if t.StepAssumed || t.EngineOmitted {
		tp.keepsOldSum = true
		tp.engine += engineToken * i
		tp.transfer = pf.xfer * i
		pf.xfer += hopToken
	} else {
		pf.xfer = engineToken + hopToken
	}
	pf.setVP(pf.vp)
	if !t.EngineOmitted {
		pf.flags = slices.DeleteFunc(pf.flags, func(f Flag) bool { return f == TransferOmitted })
	}
	switch {
	case t.EngineOmitted:
		pf.flags = append(pf.flags, DecodeTermOmitted)
	case t.StepAssumed:
		pf.flags = append(pf.flags, DecodeStepSlopeAssumed)
	}
	if t.HopAssumed {
		pf.flags = append(pf.flags, RouterHopAssumed)
	}
	return tp
}

// at is the term's D and R at step time itl, the line at the actual fill.
// oldStep is the line at the clamped fill, the step of the old sum: with no fit
// of D the two together never read under it.
func (t termPlan) at(itl, oldStep float64) (engine, hop float64) {
	engine = t.engine + t.perStep*max(itl-t.fromITL, 0)
	if t.keepsOldSum {
		engine = max(engine-t.transfer, oldStep)
	}
	return max(engine, 0), max(t.hop, 0)
}

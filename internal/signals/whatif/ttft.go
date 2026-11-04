package whatif

import (
	"fmt"
	"math"
	"slices"
)

// This file is the TTFT model of the what-if planner (PR P3 of
// docs/proposals/slo-capacity-planner.md; the model is
// docs/proposals/slo-capacity-planner-ttft.md):
//
//	TTFT = W_router + W_engine + own_prefill + T_xfer + W_decode + ITL(k_d)
//
// on a P/D fleet. Aggregated TTFT is not built (M1).
//
// UNVALIDATED, and on no switch: nothing in the controller calls it. The plan
// has P3 depend on P2, the prefill-stress GPU recording kept as a golden; that
// recording is blocked on cluster access, so P3 is built against closed-form
// queueing values and synthetic fleets instead, and every figure carries
// UnvalidatedTTFT. What P2 must check is listed on the TTFT page, "As built
// (P3)". The earlier TTFT = A*T + B fit is not repeated: TTFT includes the
// queue wait, which landed in that fit's intercept (82 s;
// docs/proposals/prefill-ttft-model.md). Here the wait is a queue of its own,
// and the fixed cost a is measured at low load, where there is none.

// Prefill is one prefill role as the TTFT model prices it: Replicas pods,
// each a work-conserving server of prompt tokens at V_P, the busiest of them
// fed f times its share of the arrivals. The figures are P1's
// (internal/signals/prefill; PrefillFromEstimate maps them), or assumed and
// flagged.
type Prefill struct {
	// VP is V_P, one pod's prompt-token throughput while it has work, in
	// tokens per second.
	VP float64
	// VPLowerBound marks a V_P from a source that is a lower bound
	// (VPLowerBound), VPAboveSlopeBound one above the low-load slope's
	// bound, and VPAssumed one the caller supplied (AssumedVP).
	VPLowerBound, VPAboveSlopeBound, VPAssumed bool
	// FixedLatency is a, in seconds: the latency one request pays on a pod
	// besides its prompt's work. It enters the request's own latency only,
	// never the queue's work (busy-minute V_P is already net of it).
	FixedLatency float64
	// Imbalance is f, at least 1 and at most Replicas: the busiest pod's
	// share of the work over the mean share. 0 is unknown, which one pod
	// prices as 1 and more than one refuses (NoImbalance).
	// ImbalanceAssumed marks one the caller supplied (AssumedImbalance).
	Imbalance        float64
	ImbalanceAssumed bool
	// Replicas is N_p.
	Replicas int
	// PromptTokens is E[I_eff], the mean prompt a pod computes (prefix
	// hits are not prefill work).
	PromptTokens float64
	// PromptCV is the coefficient of variation of I_eff, in [0, 10]: the
	// prompt is taken as gamma distributed with it (exponential at 1,
	// constant at 0), and Cs^2 = PromptCV^2. PromptCVAssumed marks one the
	// caller assumed (AssumedPromptSpread).
	PromptCV        float64
	PromptCVAssumed bool
	// ArrivalCV2 is Ca^2, the arrivals' squared coefficient of variation:
	// Kingman's 1 + Cs^2 becomes Ca^2 + Cs^2. 0 means Poisson, 1.
	ArrivalCV2 float64
	// TransferSecondsPerToken is the KV transfer's time per prompt token,
	// kappa/BW. 0 omits T_xfer, flagged TransferOmitted: it is folded into
	// the request's own latency, a + I*(1/V_P + kappa/BW), not the queue's
	// work.
	TransferSecondsPerToken float64
}

// maxPrefillReplicas bounds N_p: the pooled figure's Erlang C recursion
// runs once per pod, as the admission chain runs once per slot.
const maxPrefillReplicas = maxSlots

// maxPromptCV bounds the prompt's coefficient of variation: past 10 the
// gamma's shape is below 0.01 and the model's exponential tail says nothing.
const maxPromptCV = 10

// FlowControlState is what is known of EPP flow control.
type FlowControlState int

const (
	// FlowControlOff is flow control known to be off (the upstream default):
	// the router admits everything, W_router = 0.
	FlowControlOff FlowControlState = iota
	// FlowControlNoSeries is off because none of its series exist: priced
	// as off, flagged FlowControlInferredOff.
	FlowControlNoSeries
	// FlowControlOn is the flowControl feature gate on, with the default
	// utilization detector under the static policy, one band.
	FlowControlOn
)

// FlowControl is EPP flow control as the TTFT model prices it. With it on,
// the gate closes when the unweighted mean of the endpoints' scores
// max(queueDepth/QueueThreshold, kvUsage/KVThreshold) reaches 1, per stage
// on P/D.
type FlowControl struct {
	State FlowControlState
	// QueueThreshold and KVThreshold are the utilization detector's
	// (upstream 5 and 0.8). Read only when State is FlowControlOn; not both
	// positive and finite there is FlowControlUnknown.
	QueueThreshold, KVThreshold float64
}

// TTFTFleet is the fleet a TTFT is priced on: the prefill role, the decode
// role it hands to, and flow control.
type TTFTFleet struct {
	Prefill Prefill
	// Decode is the decode role; nil prices the prefill side alone, flagged
	// DecodeSideOmitted (no W_decode, no first step). The SLO analyzer's
	// shadow pricing of a prefill variant (S1) is that case.
	Decode      *Decode
	FlowControl FlowControl
}

// TTFTTarget is a TTFT service level, end to end.
type TTFTTarget struct {
	// Seconds is the target.
	Seconds float64
	// Percentile is q in (0, 1), or 0 for the mean.
	Percentile float64
}

// prefillPlan is a checked Prefill with its derived figures.
type prefillPlan struct {
	p Prefill
	// f is the imbalance used (1 for one pod with none given).
	f float64
	// ca2, cs2 are Ca^2 and Cs^2.
	ca2, cs2 float64
	// work is E[S_work] = E[I]/V_P, the queue's work per request; ownRate
	// is the own latency per prompt token, 1/V_P + kappa/BW.
	work, ownRate float64
	flags         []Flag
}

// check refuses a Prefill the model cannot price, naming the first input
// that fails, in the order an operator fixes them: the question, then the
// capacity, the fixed latency, the imbalance and the shape.
func (p Prefill) check() (prefillPlan, *Unanswerable) {
	var u Unanswerable
	switch {
	case p.Replicas < 1:
		u = refuse(NoPrefillReplicas, fmt.Sprintf("prefill replicas %d", p.Replicas))
	case p.Replicas > maxPrefillReplicas:
		u = refuse(InvalidInput, fmt.Sprintf("%d prefill replicas, above the %d priced", p.Replicas, maxPrefillReplicas))
	case !Finite(p.VP) || !(p.VP > 0):
		u = refuse(NoPrefillCapacity, fmt.Sprintf("V_P %g tokens/s", p.VP))
	case !Finite(p.FixedLatency) || !(p.FixedLatency > 0):
		u = refuse(NoFixedLatency, fmt.Sprintf("a %g s", p.FixedLatency))
	case p.Imbalance == 0 && p.Replicas > 1:
		u = refuse(NoImbalance, fmt.Sprintf("no f for %d prefill pods, learned or assumed", p.Replicas))
	case p.Imbalance != 0 && !(p.Imbalance >= 1 && p.Imbalance <= float64(p.Replicas)):
		u = refuse(InvalidInput, fmt.Sprintf("f %g, want 1 <= f <= %d (the pods)", p.Imbalance, p.Replicas))
	case !Finite(p.PromptTokens) || !(p.PromptTokens > 0):
		u = refuse(NoTraffic, fmt.Sprintf("prompt %g tokens", p.PromptTokens))
	case !(p.PromptCV >= 0 && p.PromptCV <= maxPromptCV):
		u = refuse(NoTraffic, fmt.Sprintf("prompt cv %g, want 0 <= cv <= %d", p.PromptCV, maxPromptCV))
	case !Finite(p.ArrivalCV2) || p.ArrivalCV2 < 0:
		u = refuse(InvalidInput, fmt.Sprintf("Ca^2 %g, want a finite value of at least 0 (0 is Poisson)", p.ArrivalCV2))
	case !Finite(p.TransferSecondsPerToken) || p.TransferSecondsPerToken < 0:
		u = refuse(InvalidInput, fmt.Sprintf("transfer %g s per token", p.TransferSecondsPerToken))
	default:
		pl := prefillPlan{p: p, f: max(p.Imbalance, 1), ca2: p.ArrivalCV2, cs2: p.PromptCV * p.PromptCV}
		if pl.ca2 == 0 {
			pl.ca2 = 1
		}
		pl.work = p.PromptTokens / p.VP
		pl.ownRate = 1/p.VP + p.TransferSecondsPerToken
		pl.flags = []Flag{UnvalidatedTTFT}
		for _, c := range []struct {
			on bool
			f  Flag
		}{
			{p.VPAssumed, AssumedVP}, {p.VPLowerBound, VPLowerBound}, {p.VPAboveSlopeBound, VPAboveSlopeBound},
			{p.ImbalanceAssumed, AssumedImbalance}, {p.PromptCVAssumed, AssumedPromptSpread},
			{p.TransferSecondsPerToken == 0, TransferOmitted},
		} {
			if c.on {
				pl.flags = append(pl.flags, c.f)
			}
		}
		return pl, nil
	}
	return prefillPlan{}, &u
}

// knee is the prefill knee, N_p * V_P / (f * E[I_eff]): the rate at which the
// worst pod saturates.
func (pl prefillPlan) knee() float64 {
	return float64(pl.p.Replicas) * pl.p.VP / (pl.f * pl.p.PromptTokens)
}

// utilization is rho_w, the worst pod's: f * rate * E[I_eff] / (N_p * V_P).
func (pl prefillPlan) utilization(rate float64) float64 {
	return pl.f * rate * pl.work / float64(pl.p.Replicas)
}

// waitScale is theta, E[S_work](Ca^2 + Cs^2) / (2(1 - rho)).
func (pl prefillPlan) waitScale(rho float64) float64 {
	return pl.work * (pl.ca2 + pl.cs2) / (2 * (1 - rho))
}

// own is the request's own latency less a: I * (1/V_P + kappa/BW).
func (pl prefillPlan) own() ownTime {
	return newOwnTime(pl.p.PromptTokens*pl.ownRate, pl.p.PromptCV)
}

// queueLength is a pod's mean waiting count at utilization rho,
// rho^2 (Ca^2 + Cs^2) / (2(1 - rho)) (Little's law on the mean wait).
func (pl prefillPlan) queueLength(rho float64) float64 {
	return rho * rho * (pl.ca2 + pl.cs2) / (2 * (1 - rho))
}

// meanScore is the prefill stage's mean endpoint score at rate: the worst
// pod at f, the other N_p - 1 sharing the rest evenly, each scoring its mean
// waiting count over the threshold. Prefill KV usage is not in it: a prefill
// pod's queue binds long before its KV does (run PK held 730 waiting at 25%
// KV).
func (pl prefillPlan) meanScore(rate, threshold float64) float64 {
	n := float64(pl.p.Replicas)
	rw := pl.utilization(rate)
	if rw >= 1 {
		return math.Inf(1)
	}
	sum := pl.queueLength(rw)
	if n > 1 {
		sum += (n - 1) * pl.queueLength(rate*pl.work*(n-pl.f)/(n*(n-1)))
	}
	return sum / n / threshold
}

// ttftPlan is a checked fleet.
type ttftPlan struct {
	pf     prefillPlan
	d      *Decode
	fc     FlowControl
	onsets Onsets
	flags  []Flag
}

// plan checks a fleet and places its flow-control onsets.
func (fl TTFTFleet) plan() (ttftPlan, *Unanswerable) {
	pf, u := fl.Prefill.check()
	if u != nil {
		return ttftPlan{}, u
	}
	if fl.Decode != nil {
		if u := fl.Decode.check(); u != nil {
			return ttftPlan{}, u
		}
	}
	pl := ttftPlan{pf: pf, d: fl.Decode, fc: fl.FlowControl, flags: pf.flags}
	if fl.Decode == nil {
		pl.flags = append(pl.flags, DecodeSideOmitted)
	}
	switch fl.FlowControl.State {
	case FlowControlOff:
	case FlowControlNoSeries:
		pl.flags = append(pl.flags, FlowControlInferredOff)
	case FlowControlOn:
		qt, kt := fl.FlowControl.QueueThreshold, fl.FlowControl.KVThreshold
		if !Finite(qt) || !(qt > 0) || !Finite(kt) || !(kt > 0) {
			u := refuse(FlowControlUnknown, fmt.Sprintf("queue threshold %g, KV threshold %g", qt, kt))
			return ttftPlan{}, &u
		}
		on, u := pl.placeOnsets()
		if u != nil {
			return ttftPlan{}, u
		}
		pl.onsets = on
	default:
		u := refuse(InvalidInput, fmt.Sprintf("flow-control state %d", fl.FlowControl.State))
		return ttftPlan{}, &u
	}
	return pl, nil
}

// placeOnsets finds each stage's onset by bisection on the rate: the least
// rate at which the stage's mean endpoint score reaches 1, below the stage's
// knee. A stage whose score stays under 1 up to its knee has its knee as the
// onset.
func (pl ttftPlan) placeOnsets() (Onsets, *Unanswerable) {
	qt, kt := pl.fc.QueueThreshold, pl.fc.KVThreshold
	var on Onsets
	on.Prefill = onset(pl.pf.knee(), func(r float64) float64 { return pl.pf.meanScore(r, qt) })
	if pl.d == nil {
		return on, nil
	}
	d := *pl.d
	if d.full().slots > maxSlots {
		u := refuse(TooManySlots, fmt.Sprintf("%g slots on one replica, above the %d the chain holds", d.full().slots, maxSlots))
		return Onsets{}, &u
	}
	on.Decode = onset(d.kneeRate(), func(r float64) float64 { return decodeScore(d, r, qt, kt) })
	return on, nil
}

// decodeScore is a decode pod's score at rate, the decode pods balanced:
// max(Lq/QueueThreshold, fill/KVThreshold) from the admission chain, with Lq
// the mean waiting count, lambda_rep times the mean admission wait (Little),
// and fill the chain's mean fill. +Inf where the chain has no stationary
// state.
func decodeScore(d Decode, rate, qt, kt float64) float64 {
	adm, ok := DecodeAdmission(d, rate).(Admission)
	if !ok {
		return math.Inf(1)
	}
	lq := rate / float64(d.Replicas) * adm.MeanWait()
	return max(lq/qt, adm.Basis.Fill/kt)
}

// onset is the least rate in [0, knee) at which score reaches 1, or knee if
// it never does there. score is non-decreasing in the rate.
func onset(knee float64, score func(float64) float64) float64 {
	if score(knee*(1-1e-12)) < 1 {
		return knee
	}
	lo, hi := 0.0, knee
	for range 200 {
		mid := lo + (hi-lo)/2
		if score(mid) < 1 {
			lo = mid
		} else {
			hi = mid
		}
		if hi-lo <= 1e-12*hi {
			break
		}
	}
	return hi
}

// PrefillKnee is the prefill knee, N_p * V_P / (f * E[I_eff]), in requests
// per second over the fleet (question 2, step 1): the rate at which the
// busiest prefill pod saturates. Basis.KneeRate repeats it.
func PrefillKnee(p Prefill) Result {
	pl, u := p.check()
	if u != nil {
		return *u
	}
	k := pl.knee()
	return Answer{Value: k, Flags: pl.flags, Basis: Basis{KneeRate: k, BoundBy: []Bound{BoundPrefillKnee}}}
}

// FlowControlOnsets are the rates at which EPP flow control's gate starts
// holding requests (docs/proposals/slo-capacity-planner-ttft.md, "The router
// queue"): zero with flow control off. The decode onset is an extra knee:
// RateForTTFT caps at it and TTFTAt refuses above it.
func FlowControlOnsets(fl TTFTFleet) Result {
	pl, u := fl.plan()
	if u != nil {
		return *u
	}
	return pl.onsets
}

// lowestKnee is the lower of the two roles' knees.
func (pl ttftPlan) lowestKnee() float64 {
	k := pl.pf.knee()
	if pl.d != nil {
		k = min(k, pl.d.kneeRate())
	}
	return k
}

// priced is one TTFT at one rate, before it becomes a TTFT result.
type priced struct {
	out TTFT
	// u is set when the rate cannot be priced.
	u *Unanswerable
}

// price is the TTFT at rate and percentile q (0 for the mean). With
// withDecodeWait false the decode admission wait is left out: the prefill
// side alone, which RateForTTFT compares against.
func (pl ttftPlan) price(rate, q float64, withDecodeWait, extras bool) priced {
	pk := pl.pf.knee()
	out := TTFT{PrefillKnee: pk, Onsets: pl.onsets, Flags: append([]Flag(nil), pl.flags...)}
	if rate >= pk {
		u := refuse(Saturated, fmt.Sprintf("%g r/s at or above the prefill knee %g r/s", rate, pk))
		return priced{u: &u}
	}
	if pl.fc.State == FlowControlOn {
		if pl.d != nil && rate >= pl.onsets.Decode {
			u := refuse(AboveDecodeOnset, fmt.Sprintf("%g r/s at or above the decode gate's onset %g r/s", rate, pl.onsets.Decode))
			return priced{u: &u}
		}
		if rate >= pl.onsets.Prefill {
			out.Flags = append(out.Flags, AboveFlowControlOnset)
		}
	}

	rho := pl.pf.utilization(rate)
	theta := pl.pf.waitScale(rho)
	own := pl.pf.own()
	a := pl.pf.p.FixedLatency
	b1 := block{wait: expWait(rho, 1/theta), own: own}
	out.PrefillUtilization, out.WaitScale = rho, theta
	out.Parts.EngineWait = rho * theta
	out.Parts.OwnPrefill = a + own.mean

	var dec waitTail
	var first, floorStep float64
	if pl.d != nil {
		d := *pl.d
		out.DecodeKnee = d.kneeRate()
		adm, ok := DecodeAdmission(d, rate).(Admission)
		if !ok {
			r := DecodeAdmission(d, rate).(Unanswerable)
			return priced{u: &r}
		}
		k, ok := d.fillAtReplicaRate(rate / float64(d.Replicas))
		if !ok {
			u := refuse(Saturated, fmt.Sprintf("no decode fill carries %g r/s", rate))
			return priced{u: &u}
		}
		if k < d.Band.Lo {
			k = d.Band.Lo
			out.Flags = addFlags(out.Flags, BelowFittedBand)
		}
		first, floorStep = d.Line.ITLAt(k), d.Line.ITLAt(d.Band.Lo)
		out.Flags = addFlags(out.Flags, adm.Flags...)
		out.DecodeWaitProbability = adm.WaitProbability
		if withDecodeWait {
			dec = expWait(adm.WaitProbability, adm.Decay)
			out.Parts.DecodeWait = adm.MeanWait()
		}
	}
	out.Parts.FirstStep = first

	if q == 0 {
		mean := out.Parts.EngineWait + out.Parts.OwnPrefill + out.Parts.DecodeWait + first
		out.Seconds, out.Independent = mean, mean
		out.Floor = a + own.mean + floorStep
		if extras {
			out.Pooled = pl.pooledWait(rate).mean() + out.Parts.OwnPrefill + out.Parts.DecodeWait + first
		}
		return priced{out: out}
	}
	v, beta := split(b1, dec, q)
	if !Finite(v) {
		// Only within rounding of a knee, where the wait's scale overflows.
		u := refuse(Saturated, fmt.Sprintf("no finite TTFT at %g r/s", rate))
		return priced{u: &u}
	}
	out.Seconds, out.DecodeShare = a+v+first, beta
	out.Floor = a + own.quantile(q) + floorStep
	out.Independent = out.Seconds
	if extras {
		if len(dec.w) > 0 {
			out.Independent = a + block{wait: b1.wait.sum(dec), own: own}.quantile(1-q) + first
		}
		pv, _ := split(block{wait: pl.pooledWait(rate), own: own}, dec, q)
		out.Pooled = a + pv + first
	}
	return priced{out: out}
}

// pooledWait is the optimistic wait: the N_p pods as one M/G/N_p queue with
// no imbalance (Allen-Cunneen), P(W > t) = C * exp(-t/theta_p), C Erlang C at
// the offered load and theta_p = E[S](Ca^2 + Cs^2) / (2 N_p (1 - rho)).
func (pl ttftPlan) pooledWait(rate float64) waitTail {
	n := pl.pf.p.Replicas
	load := rate * pl.pf.work
	rho := load / float64(n)
	thetaP := pl.pf.work * (pl.pf.ca2 + pl.pf.cs2) / (2 * float64(n) * (1 - rho))
	return expWait(erlangCProb(n, load), 1/thetaP)
}

// checkPercentile refuses a TTFT percentile outside (0, 1); 0 is the mean.
func checkPercentile(q float64) *Unanswerable {
	if q == 0 || (q > 0 && q < 1) {
		return nil
	}
	u := refuse(InvalidInput, fmt.Sprintf("TTFT percentile %g, want 0 < q < 1, or 0 for the mean", q))
	return &u
}

// TTFTAt is the end-to-end TTFT at rate requests per second over the fleet,
// at percentile q, or the mean for q = 0 (question 2's TTFT): the union bound
// with its parts, the independent figure beside it, the pooled optimistic
// figure and the floor.
//
// It refuses (Saturated) at or above the lower knee, and, with flow control
// on, at or above the decode gate's onset (AboveDecodeOnset); at or above the
// prefill gate's onset it answers, flagged AboveFlowControlOnset.
func TTFTAt(fl TTFTFleet, rate, q float64) Result {
	pl, u := fl.plan()
	if u != nil {
		return *u
	}
	if u := checkRate(rate); u != nil {
		return *u
	}
	if u := checkPercentile(q); u != nil {
		return *u
	}
	p := pl.price(rate, q, true, true)
	if p.u != nil {
		return *p.u
	}
	return p.out
}

// RateForTTFT is question 1 for TTFT: the largest rate, in requests per
// second over the fleet, whose TTFT at the target's percentile (or mean) is at
// most the target, decode admission priced at the same rate
// (docs/proposals/slo-capacity-planner.md, "Question 1").
//
// TTFT rises with the rate, so the rate is bisected below the lower knee and,
// with flow control on, below the decode gate's onset, which caps it
// (BoundDecodeOnset). When the prefill side alone would allow more (by more
// than a relative 1e-6), the rate is the one decode admission holds it to
// (BoundBy TTFT then BoundDecodeAdmission, flagged HeldByDecodeAdmission); Basis.Uncapped is the prefill side's own rate. If
// the TTFT at no load, the floor, already reaches the target, it refuses
// (TargetBelowFloor). The operating-point cap is not applied: the fleet
// carries the smaller of the roles' rates, then the cap, as the caller
// combines them.
//
// Basis.KneeRate is the lower knee, Basis.Floor the TTFT floor and
// Basis.Fill the decode fill at the answer (0 with no decode side).
func RateForTTFT(fl TTFTFleet, t TTFTTarget) Result {
	pl, u := fl.plan()
	if u != nil {
		return *u
	}
	if !Finite(t.Seconds) || !(t.Seconds > 0) {
		return refuse(InvalidInput, fmt.Sprintf("TTFT target %g s", t.Seconds))
	}
	if u := checkPercentile(t.Percentile); u != nil {
		return *u
	}
	zero := pl.price(0, t.Percentile, true, false)
	if zero.u != nil {
		return *zero.u
	}
	floor := zero.out.Floor
	if floor >= t.Seconds {
		return refuse(TargetBelowFloor, fmt.Sprintf("target %g s at or below the TTFT floor %g s", t.Seconds, floor))
	}
	limit, limitBy := pl.lowestKnee(), Bound("")
	if pl.fc.State == FlowControlOn && pl.d != nil && pl.onsets.Decode < limit {
		limit, limitBy = pl.onsets.Decode, BoundDecodeOnset
	}
	full := pl.bisect(t, limit, true)
	alone := pl.bisect(t, limit, false)

	var by []Bound
	flags := append([]Flag(nil), pl.flags...)
	switch {
	case limitBy != "" && full >= limit:
		by = []Bound{limitBy}
	case alone > full*(1+1e-6):
		// The target sets the rate; decode admission's share of the tail
		// holds it below what the prefill side alone allows.
		by = []Bound{BoundTTFT, BoundDecodeAdmission}
		flags = append(flags, HeldByDecodeAdmission)
	default:
		by = []Bound{BoundTTFT}
	}
	at := pl.price(min(full, limit*(1-1e-12)), t.Percentile, true, false)
	if at.u != nil {
		return *at.u
	}
	for _, f := range at.out.Flags {
		if f == BelowFittedBand || f == KVBoundSlots || f == AboveFlowControlOnset {
			flags = addFlags(flags, f)
		}
	}
	var fill float64
	if pl.d != nil {
		fill, _ = pl.d.fillAtReplicaRate(full / float64(pl.d.Replicas))
	}
	return Answer{Value: full, Flags: flags, Basis: Basis{
		Fill: fill, KneeRate: pl.lowestKnee(), Floor: floor, Uncapped: alone, BoundBy: by,
	}}
}

// bisect is the largest rate below limit whose TTFT meets the target, or
// limit itself when the TTFT just under it still does (only an onset: at a
// knee the TTFT is unbounded).
func (pl ttftPlan) bisect(t TTFTTarget, limit float64, withDecodeWait bool) float64 {
	meets := func(rate float64) bool {
		p := pl.price(rate, t.Percentile, withDecodeWait, false)
		return p.u == nil && p.out.Seconds <= t.Seconds
	}
	if meets(limit * (1 - 1e-12)) {
		return limit
	}
	lo, hi := 0.0, limit
	for range 200 {
		mid := lo + (hi-lo)/2
		if meets(mid) {
			lo = mid
		} else {
			hi = mid
		}
		if hi-lo <= 1e-12*hi {
			break
		}
	}
	return lo
}

// addFlags appends each flag not already in flags.
func addFlags(flags []Flag, add ...Flag) []Flag {
	for _, f := range add {
		if !slices.Contains(flags, f) {
			flags = append(flags, f)
		}
	}
	return flags
}

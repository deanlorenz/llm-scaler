package whatif

import (
	"math"
	"slices"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
)

// The TTFT model's known-truth cases. None of them is a measurement: P2, the
// prefill-stress recording, is what will check the model against a real pod.
// What they pin is the arithmetic, against values computed without it:
// closed-form queues (M/M/1, M/D/1, Pollaczek-Khinchine, Erlang C, the
// incomplete gamma's closed forms) and an independent numerical reference
// (scipy quad over a different form of the convolution) for the worked example
// and the cases with no closed form.

// workedPrefill is the worked example's prefill role
// (docs/proposals/slo-capacity-planner.md, "Worked example: ols-sweep"): one
// pod, V_P = 30,000 tok/s assumed, a = 10.1 + 2.5 ms as the page computed it,
// prompts of 1000 tokens with an assumed cv of 1.
func workedPrefill() Prefill {
	return Prefill{
		VP: 30000, VPAssumed: true, FixedLatency: 0.0126, Replicas: 1,
		PromptTokens: 1000, PromptCV: 1, PromptCVAssumed: true,
	}
}

// workedFleet is the worked example's fleet, flow control inferred off.
func workedFleet() TTFTFleet {
	d := olsSweep()
	return TTFTFleet{Prefill: workedPrefill(), Decode: &d, FlowControl: FlowControl{State: FlowControlNoSeries}}
}

func mustTTFT(t *testing.T, r Result) TTFT {
	t.Helper()
	v, ok := r.(TTFT)
	if !ok {
		t.Fatalf("want a TTFT, got %#v", r)
	}
	return v
}

func relNear(t *testing.T, what string, got, want, rel float64) {
	t.Helper()
	if !(math.Abs(got-want) <= rel*math.Abs(want)) {
		t.Errorf("%s = %.10g, want %.10g within %g relative", what, got, want, rel)
	}
}

// The page's worked example, against the independent reference's figures
// (scipy: 0.15996 / 0.15440 at p90, 0.23061 / 0.20999 at p95), which round to
// the page's 160 / 154 and 231 / 210 ms.
func TestTTFTWorkedExample(t *testing.T) {
	fl := workedFleet()
	op := 0.85 * mustAnswer(t, KneeRate(*fl.Decode)).Value
	p90 := mustTTFT(t, TTFTAt(fl, op, 0.9))
	relNear(t, "TTFT p90", p90.Seconds, 0.1599611515667, 1e-6)
	relNear(t, "independent p90", p90.Independent, 0.15440219990354434, 1e-5)
	relNear(t, "rho", p90.PrefillUtilization, 0.3771893403059625, 1e-12)
	relNear(t, "theta", p90.WaitScale, 0.053520813773015215, 1e-12)
	relNear(t, "decode share", p90.DecodeShare, 0.03177463658731497, 1e-4)
	relNear(t, "floor p90", p90.Floor, 0.09157553643313485, 1e-9)
	p95 := mustTTFT(t, TTFTAt(fl, op, 0.95))
	relNear(t, "TTFT p95", p95.Seconds, 0.2306087421668428, 1e-6)
	relNear(t, "independent p95", p95.Independent, 0.20999230485834747, 1e-5)
	for _, f := range []Flag{UnvalidatedTTFT, AssumedVP, AssumedPromptSpread, TransferOmitted, FlowControlInferredOff} {
		if !p90.HasFlag(f) {
			t.Errorf("worked example not flagged %s: %v", f, p90.Flags)
		}
	}
	// One pod, no imbalance: the pooled queue is the pod's own.
	relNear(t, "pooled p90 on one pod", p90.Pooled, p90.Seconds, 1e-12)
}

// Question 1 on the worked example: 200 ms gives 11.64 r/s at p90 and 11.10
// at p95 (the reference: 11.63853, 11.10495).
func TestTTFTWorkedExample_RateForTTFT(t *testing.T) {
	fl := workedFleet()
	for _, c := range []struct{ q, want float64 }{{0.9, 11.638534256575051}, {0.95, 11.104948214254135}} {
		a := mustAnswer(t, RateForTTFT(fl, TTFTTarget{Seconds: 0.2, Percentile: c.q}))
		relNear(t, "rate for 200 ms", a.Value, c.want, 1e-5)
		// The decode wait takes the tail's first share, so the prefill side
		// alone would carry more.
		if !(a.Basis.Uncapped > a.Value) || !a.HasFlag(HeldByDecodeAdmission) ||
			!slices.Equal(a.Basis.BoundBy, []Bound{BoundTTFT, BoundDecodeAdmission}) {
			t.Errorf("q %g: uncapped %g, bound %v, flags %v", c.q, a.Basis.Uncapped, a.Basis.BoundBy, a.Flags)
		}
		relNear(t, "knee", a.Basis.KneeRate, 13.312564951975148, 1e-9)
		back := mustTTFT(t, TTFTAt(fl, a.Value, c.q))
		relNear(t, "TTFT back at the rate", back.Seconds, 0.2, 1e-6)
	}
}

// Flow control's onsets on the worked example: decode 12.50 r/s, prefill
// 25.6 (the reference: the chain's mean waiting count reaches 5 at 12.50098;
// rho^2/(1 - rho) = 5 at rho = 0.854102).
func TestFlowControlOnsets(t *testing.T) {
	fl := workedFleet()
	fl.FlowControl = FlowControl{State: FlowControlOn, QueueThreshold: 5, KVThreshold: 0.8}
	on, ok := FlowControlOnsets(fl).(Onsets)
	if !ok {
		t.Fatalf("no onsets: %#v", FlowControlOnsets(fl))
	}
	relNear(t, "decode onset", on.Decode, 12.500975816826703, 1e-6)
	relNear(t, "prefill onset", on.Prefill, 25.623058987490534, 1e-6)

	// At or above the decode onset TTFT is not priced; just below it, it is.
	if u, ok := TTFTAt(fl, on.Decode, 0.9).(Unanswerable); !ok || u.Reason != AboveDecodeOnset {
		t.Errorf("at the decode onset: %#v", TTFTAt(fl, on.Decode, 0.9))
	}
	below := mustTTFT(t, TTFTAt(fl, 0.99*on.Decode, 0.9))
	off := mustTTFT(t, TTFTAt(workedFleet(), 0.99*on.Decode, 0.9))
	// Below both onsets flow control changes nothing.
	if below.Seconds != off.Seconds || below.HasFlag(AboveFlowControlOnset) {
		t.Errorf("below the onsets: on %g off %g, flags %v", below.Seconds, off.Seconds, below.Flags)
	}
	// Question 1 caps at the decode onset: a loose target reaches it.
	a := mustAnswer(t, RateForTTFT(fl, TTFTTarget{Seconds: 30, Percentile: 0.9}))
	relNear(t, "rate capped at the onset", a.Value, on.Decode, 1e-12)
	if !slices.Equal(a.Basis.BoundBy, []Bound{BoundDecodeOnset}) {
		t.Errorf("bound %v, want the decode onset", a.Basis.BoundBy)
	}
	// Without flow control the same target is met up to near the knee.
	if b := mustAnswer(t, RateForTTFT(workedFleet(), TTFTTarget{Seconds: 30, Percentile: 0.9})); !(b.Value > on.Decode) {
		t.Errorf("flow control off: %g r/s, not above the onset %g", b.Value, on.Decode)
	}

	// A slower prefill pod moves its onset below decode's: at or above it the
	// answer is flagged, and below it the answer is not.
	slow := fl
	slow.Prefill.VP = 12000 // knee 12 r/s, onset 0.854 * 12 = 10.25
	son := FlowControlOnsets(slow).(Onsets)
	relNear(t, "slow prefill onset", son.Prefill, 0.8541019662496845*12, 1e-6)
	if v := mustTTFT(t, TTFTAt(slow, 11, 0.9)); !v.HasFlag(AboveFlowControlOnset) {
		t.Errorf("above the prefill onset, not flagged: %v", v.Flags)
	}
	if v := mustTTFT(t, TTFTAt(slow, 9, 0.9)); v.HasFlag(AboveFlowControlOnset) {
		t.Errorf("below the prefill onset, flagged: %v", v.Flags)
	}
	// Flow control on with a threshold unknown is refused.
	unknown := fl
	unknown.FlowControl.KVThreshold = 0
	if u, ok := TTFTAt(unknown, 5, 0.9).(Unanswerable); !ok || u.Reason != FlowControlUnknown {
		t.Errorf("unknown thresholds: %#v", TTFTAt(unknown, 5, 0.9))
	}
	// Several prefill pods: the busiest at f, the rest sharing the remainder,
	// scored by each pod's mean waiting count rho^2 (Ca^2 + Cs^2)/(2(1 - rho))
	// (here Cs^2 = 1, Ca^2 = 1) and averaged. The onset is where that mean
	// reaches the threshold; found here by its own bisection.
	imb := fl
	imb.Prefill.VP, imb.Prefill.Replicas, imb.Prefill.Imbalance = 12000, 3, 1.8
	lq := func(rho float64) float64 { return rho * rho * 2 / (2 * (1 - rho)) }
	score := func(rate float64) float64 {
		rb := rate * 1000 / 12000 / 3 // the mean pod's utilization
		rw, ro := 1.8*rb, rb*(3-1.8)/2
		if rw >= 1 {
			return math.Inf(1)
		}
		return (lq(rw) + 2*lq(ro)) / 3 / 5
	}
	lo, hi := 0.0, 3*12.0/1.8
	for range 200 {
		mid := (lo + hi) / 2
		if score(mid) < 1 {
			lo = mid
		} else {
			hi = mid
		}
	}
	relNear(t, "imbalanced prefill onset", FlowControlOnsets(imb).(Onsets).Prefill, hi, 1e-9)

	// A KV threshold below the fill the chain reaches before its queue
	// holds 5: the KV term sets the decode onset, where the chain's mean
	// fill reaches the threshold.
	kv := fl
	kv.FlowControl.KVThreshold = 0.3
	kon := FlowControlOnsets(kv).(Onsets).Decode
	fillAt := func(rate float64) float64 { return DecodeAdmission(*kv.Decode, rate).(Admission).Basis.Fill }
	relNear(t, "KV-bound decode onset's fill", fillAt(kon), 0.3, 1e-9)
	if !(kon < on.Decode) {
		t.Errorf("KV-bound onset %g not below the queue-bound %g", kon, on.Decode)
	}

	// Off, the onsets are zero.
	if on := FlowControlOnsets(workedFleet()).(Onsets); on != (Onsets{}) {
		t.Errorf("flow control off: onsets %+v", on)
	}
}

// prefillOnly is one pod, no decode side, Poisson arrivals.
func prefillOnly(cv float64) TTFTFleet {
	p := workedPrefill()
	p.PromptCV = cv
	return TTFTFleet{Prefill: p}
}

// At cv 1, f 1, one pod and Poisson arrivals the pod is M/M/1, and the
// exponential tail is exact: wait plus own work is exponential at mu - lambda,
// so TTFT_q = a + ln(1/(1-q))/(mu - lambda), and the mean a + 1/(mu - lambda).
func TestTTFTIsMM1Sojourn(t *testing.T) {
	fl := prefillOnly(1)
	mu := fl.Prefill.VP / fl.Prefill.PromptTokens
	for _, lam := range []float64{0, 1, 5, 15, 25, 29, 29.9} {
		for _, q := range []float64{0.5, 0.9, 0.99, 0.999} {
			v := mustTTFT(t, TTFTAt(fl, lam, q))
			relNear(t, "M/M/1 quantile", v.Seconds, fl.Prefill.FixedLatency+math.Log(1/(1-q))/(mu-lam), 1e-9)
		}
		m := mustTTFT(t, TTFTAt(fl, lam, 0))
		relNear(t, "M/M/1 mean", m.Seconds, fl.Prefill.FixedLatency+1/(mu-lam), 1e-12)
		if !m.HasFlag(DecodeSideOmitted) {
			t.Errorf("no decode side, not flagged: %v", m.Flags)
		}
	}
}

// With f = 1 and one pod the mean engine wait is Pollaczek-Khinchine,
// lambda E[S^2] / (2(1 - rho)), with E[S^2] = E[S]^2 (1 + cv^2); at cv 0 the
// pod is M/D/1, whose wait (the model's exponential tail behind the atom)
// gives TTFT_q = a + S + theta ln(rho/(1-q)) when rho > 1 - q.
func TestEngineWaitIsPollaczekKhinchine(t *testing.T) {
	for _, cv := range []float64{0, 0.3, 1, 2.5} {
		fl := prefillOnly(cv)
		es := fl.Prefill.PromptTokens / fl.Prefill.VP
		for _, lam := range []float64{3, 15, 27} {
			v := mustTTFT(t, TTFTAt(fl, lam, 0))
			rho := lam * es
			relNear(t, "PK mean wait", v.Parts.EngineWait, lam*es*es*(1+cv*cv)/(2*(1-rho)), 1e-12)
			relNear(t, "mean", v.Seconds, fl.Prefill.FixedLatency+es+v.Parts.EngineWait, 1e-12)
		}
	}
	fl := prefillOnly(0)
	es := fl.Prefill.PromptTokens / fl.Prefill.VP
	for _, lam := range []float64{15, 25, 29} {
		rho := lam * es
		theta := es / (2 * (1 - rho))
		for _, q := range []float64{0.9, 0.99} {
			v := mustTTFT(t, TTFTAt(fl, lam, q))
			relNear(t, "M/D/1 quantile", v.Seconds, fl.Prefill.FixedLatency+es+theta*math.Log(rho/(1-q)), 1e-9)
		}
	}
}

// Cases with no closed form, against the independent reference (scipy quad
// over P(W+S>t) = (1-p) sf(t) + p [exp(-rt) + int r exp(-ru) sf(t-u) du], a
// different form from the code's incomplete-gamma closed form): prompt
// spreads either side of 1, bursty arrivals with an imbalance across four
// pods and two decode replicas, a KV transfer term, and the prefill side
// alone at cv 1 and cv 0.
func TestTTFTAgainstReference(t *testing.T) {
	cases := []struct {
		name                        string
		rate, q, a, vp, il, cv, ca2 float64
		f                           float64
		np, nd                      int
		xfer, wantSeconds, wantMean float64
	}{
		{"cv0.5", 9, 0.9, 0.011, 30000, 1000, 0.5, 1, 1, 1, 1, 0, 0.08909511332775925, 0.0561332049162314},
		{"cv2", 9, 0.95, 0.011, 30000, 1000, 2, 1, 1, 1, 1, 0, 0.30636124808950277, 0.08291891920194568},
		{"bursty-imbalanced", 20, 0.9, 0.011, 30000, 1000, 1, 2, 1.5, 4, 2, 0, 0.13239144522086338, 0.06437997302707862},
		{"transfer", 8, 0.99, 0.02, 20000, 2000, 0.8, 1, 1, 1, 1, 2e-6, 1.932741231477786, 0.45462260548736116},
		{"prefill-only", 25, 0.9, 0.011, 30000, 1000, 1, 1, 1, 1, 0, 0, 0.47151701859880935, 0.21100000000000005},
		{"constant-prompt", 25, 0.9, 0.011, 30000, 1000, 0, 1, 1, 1, 0, 0, 0.25635968695334327, 0.12766666666666668},
	}
	for _, c := range cases {
		fl := TTFTFleet{Prefill: Prefill{VP: c.vp, FixedLatency: c.a, Imbalance: c.f, Replicas: c.np,
			PromptTokens: c.il, PromptCV: c.cv, ArrivalCV2: c.ca2, TransferSecondsPerToken: c.xfer}}
		if c.nd > 0 {
			d := olsSweep()
			d.Replicas = c.nd
			fl.Decode = &d
		}
		v := mustTTFT(t, TTFTAt(fl, c.rate, c.q))
		relNear(t, c.name+" quantile", v.Seconds, c.wantSeconds, 2e-6)
		m := mustTTFT(t, TTFTAt(fl, c.rate, 0))
		relNear(t, c.name+" mean", m.Seconds, c.wantMean, 1e-9)
		if got := v.HasFlag(TransferOmitted); got != (c.xfer == 0) {
			t.Errorf("%s: TransferOmitted %v with transfer %g", c.name, got, c.xfer)
		}
	}
}

// The pooled figure is Allen-Cunneen: Erlang C (the test's own recursion,
// closed_test.go) times theta_p = E[S](Ca^2 + Cs^2) / (2 N (1 - rho)), and is
// never above the worst pod's figure.
func TestPooledIsAllenCunneen(t *testing.T) {
	fl := prefillOnly(1)
	fl.Prefill.Replicas, fl.Prefill.Imbalance, fl.Prefill.ArrivalCV2 = 4, 1.3, 1.5
	es := fl.Prefill.PromptTokens / fl.Prefill.VP
	for _, lam := range []float64{20, 60, 85} {
		m := mustTTFT(t, TTFTAt(fl, lam, 0))
		rho := lam * es / 4
		wq := erlangC(4, lam*es) * es * (1.5 + 1) / (2 * 4 * (1 - rho))
		relNear(t, "pooled mean", m.Pooled, fl.Prefill.FixedLatency+es+wq, 1e-12)
		for _, q := range []float64{0, 0.9, 0.99} {
			v := mustTTFT(t, TTFTAt(fl, lam, q))
			if !(v.Pooled <= v.Seconds) {
				t.Errorf("rate %g q %g: pooled %g above the worst pod's %g", lam, q, v.Pooled, v.Seconds)
			}
		}
	}
}

// The incomplete gamma against its closed forms: P(1, x) = 1 - e^-x,
// P(2, x) = 1 - e^-x (1 + x), P(1/2, x) = erf(sqrt x); and its quantile back.
func TestIncompleteGamma(t *testing.T) {
	for _, x := range []float64{1e-6, 0.01, 0.5, 1, 2.9, 3.1, 10, 40} {
		p1, q1 := gammaPQ(1, x)
		relNear(t, "P(1,x)", p1, -math.Expm1(-x), 1e-13)
		relNear(t, "Q(1,x)", q1, math.Exp(-x), 1e-12)
		_, q2 := gammaPQ(2, x)
		relNear(t, "Q(2,x)", q2, math.Exp(-x)*(1+x), 1e-12)
		ph, qh := gammaPQ(0.5, x)
		relNear(t, "P(1/2,x)", ph, math.Erf(math.Sqrt(x)), 1e-12)
		relNear(t, "Q(1/2,x)", qh, math.Erfc(math.Sqrt(x)), 1e-11)
		if x < 20 { // above, P rounds to 1 and its log to 0 in both
			relNear(t, "ln P(2,x)", lnGammaP(2, x), math.Log(-math.Expm1(-x)-x*math.Exp(-x)), 1e-10)
		}
	}
	for _, q := range []float64{0.01, 0.5, 0.9, 0.999} {
		relNear(t, "gamma(1) quantile", gammaQuantile(1, q), -math.Log1p(-q), 1e-12)
		for _, k := range []float64{0.04, 0.7, 3, 400} {
			p, _ := gammaPQ(k, gammaQuantile(k, q))
			relNear(t, "P at the quantile", p, q, 1e-10)
		}
	}
}

// The floor is the TTFT at no load: a + I_q/V_P + ITL(band.Lo), with I_q the
// prompt's q-quantile (for cv 1, -ln(1-q) E[I]).
func TestTTFTFloor(t *testing.T) {
	fl := workedFleet()
	d := *fl.Decode
	for _, q := range []float64{0.5, 0.9, 0.99} {
		v := mustTTFT(t, TTFTAt(fl, 0, q))
		want := 0.0126 + -math.Log1p(-q)*1000/30000 + d.Line.ITLAt(d.Band.Lo)
		relNear(t, "floor", v.Floor, want, 1e-11)
		relNear(t, "TTFT at no load", v.Seconds, want, 1e-11)
		if !v.HasFlag(BelowFittedBand) {
			t.Errorf("decode's first step below the band, not flagged: %v", v.Flags)
		}
		// A target at the floor is refused, just above it is answered.
		if u, ok := RateForTTFT(fl, TTFTTarget{Seconds: v.Floor, Percentile: q}).(Unanswerable); !ok || u.Reason != TargetBelowFloor {
			t.Errorf("q %g at the floor: %#v", q, RateForTTFT(fl, TTFTTarget{Seconds: v.Floor, Percentile: q}))
		}
		if a := mustAnswer(t, RateForTTFT(fl, TTFTTarget{Seconds: want * 1.01, Percentile: q})); !(a.Value > 0) {
			t.Errorf("q %g just above the floor: %g r/s", q, a.Value)
		}
	}
	// The mean's floor is the mean prompt's.
	m := mustTTFT(t, TTFTAt(fl, 0, 0))
	relNear(t, "mean floor", m.Floor, 0.0126+1000.0/30000+d.Line.ITLAt(d.Band.Lo), 1e-12)
}

// The knees: prefill N_p V_P / (f E[I]); TTFT refuses at it, as at decode's.
func TestTTFTSaturates(t *testing.T) {
	fl := workedFleet()
	fl.Prefill.VP, fl.Prefill.Replicas, fl.Prefill.Imbalance = 6000, 2, 1.25
	pk := mustAnswer(t, PrefillKnee(fl.Prefill))
	relNear(t, "prefill knee", pk.Value, 2*6000/(1.25*1000), 1e-12)
	for _, r := range []float64{pk.Value, pk.Value * 1.5} {
		if u, ok := TTFTAt(fl, r, 0.9).(Unanswerable); !ok || u.Reason != Saturated {
			t.Errorf("at %g r/s, prefill knee %g: %#v", r, pk.Value, TTFTAt(fl, r, 0.9))
		}
	}
	mustTTFT(t, TTFTAt(fl, pk.Value*(1-1e-9), 0.9))
	dk := mustAnswer(t, KneeRate(*workedFleet().Decode)).Value
	if u, ok := TTFTAt(workedFleet(), dk, 0.9).(Unanswerable); !ok || u.Reason != Saturated {
		t.Errorf("at the decode knee: %#v", TTFTAt(workedFleet(), dk, 0.9))
	}
	// Question 1 never answers at or above the lower knee.
	if a := mustAnswer(t, RateForTTFT(fl, TTFTTarget{Seconds: 100, Percentile: 0.9})); !(a.Value < pk.Value) {
		t.Errorf("rate %g at or above the prefill knee %g", a.Value, pk.Value)
	}
}

// The union bound is never below the independent figure, and the optimised
// split is never above the even split, beta = (1-q)/2.
func TestUnionBoundAgainstIndependentAndEvenSplit(t *testing.T) {
	fl := workedFleet()
	d := *fl.Decode
	knee := mustAnswer(t, KneeRate(d)).Value
	for _, frac := range []float64{0.3, 0.6, 0.85, 0.9, 0.95, 0.98} {
		for _, q := range []float64{0.5, 0.9, 0.95, 0.99} {
			rate := frac * knee
			v := mustTTFT(t, TTFTAt(fl, rate, q))
			if !(v.Seconds >= v.Independent*(1-1e-9)) {
				t.Errorf("%.2f of the knee, q %g: union %g below independent %g", frac, q, v.Seconds, v.Independent)
			}
			pl, _ := fl.plan()
			rho := pl.pf.utilization(rate)
			b1 := block{wait: expWait(rho, 1/pl.pf.waitScale(rho)), own: pl.pf.own()}
			adm := DecodeAdmission(d, rate).(Admission)
			even := 0.0126 + b1.quantile((1-q)/2) + adm.WaitQuantile(1-(1-q)/2) + v.Parts.FirstStep
			if !(v.Seconds <= even*(1+1e-12)) {
				t.Errorf("%.2f of the knee, q %g: optimised %g above the even split %g", frac, q, v.Seconds, even)
			}
		}
	}
}

// PrefillFromEstimate carries P1's figures and its refusals.
func TestPrefillFromEstimate(t *testing.T) {
	est := prefill.Estimate{VP: 30000, VPSource: prefill.SourcePeakUnderQueue, VPLowerBound: true, A: 0.011, F: 1.4}
	p, u := PrefillFromEstimate(est)
	if u != nil {
		t.Fatal(u)
	}
	if p.VP != 30000 || !p.VPLowerBound || p.FixedLatency != 0.011 || p.Imbalance != 1.4 || p.VPAssumed {
		t.Errorf("mapped %+v", p)
	}
	p.Replicas, p.PromptTokens, p.PromptCV = 4, 1000, 1
	if v := mustTTFT(t, TTFTAt(TTFTFleet{Prefill: p}, 10, 0.9)); !v.HasFlag(VPLowerBound) {
		t.Errorf("a lower-bound V_P, not flagged: %v", v.Flags)
	}

	refusedF := est
	refusedF.F, refusedF.FRefused = 0, prefill.TooFewImbalanceMinutes
	p, _ = PrefillFromEstimate(refusedF)
	if p.Imbalance != 0 {
		t.Errorf("refused f mapped to %g", p.Imbalance)
	}
	p.Replicas, p.PromptTokens = 3, 1000
	if u, ok := TTFTAt(TTFTFleet{Prefill: p}, 1, 0.9).(Unanswerable); !ok || u.Reason != NoImbalance {
		t.Errorf("three pods, no f: %#v", TTFTAt(TTFTFleet{Prefill: p}, 1, 0.9))
	}
	p.Replicas = 1
	mustTTFT(t, TTFTAt(TTFTFleet{Prefill: p}, 1, 0.9))

	for name, c := range map[string]struct {
		est  prefill.Estimate
		want Reason
	}{
		"V_P":    {prefill.Estimate{VPRefused: prefill.NoPrefillCapacity, A: 0.01}, NoPrefillCapacity},
		"SGLang": {prefill.Estimate{VPRefused: prefill.SGLangRefused, A: 0.01}, NoPrefillCapacity},
		"a":      {prefill.Estimate{VP: 30000, ARefused: prefill.TooFewLowLoadMinutes}, NoFixedLatency},
	} {
		if _, u := PrefillFromEstimate(c.est); u == nil || u.Reason != c.want {
			t.Errorf("%s: %v, want %s", name, u, c.want)
		}
	}
}

// ttftReasonCases are the TTFT model's cases for
// TestProperty_EveryReasonHasACase.
func ttftReasonCases(t *testing.T) map[Reason]Result {
	t.Helper()
	with := func(f func(*TTFTFleet)) TTFTFleet {
		fl := workedFleet()
		d := *fl.Decode
		fl.Decode = &d
		f(&fl)
		return fl
	}
	return map[Reason]Result{
		NoPrefillCapacity: TTFTAt(with(func(fl *TTFTFleet) { fl.Prefill.VP = 0 }), 1, 0.9),
		NoFixedLatency:    TTFTAt(with(func(fl *TTFTFleet) { fl.Prefill.FixedLatency = 0 }), 1, 0.9),
		NoImbalance:       TTFTAt(with(func(fl *TTFTFleet) { fl.Prefill.Replicas = 2 }), 1, 0.9),
		NoPrefillReplicas: TTFTAt(with(func(fl *TTFTFleet) { fl.Prefill.Replicas = 0 }), 1, 0.9),
		FlowControlUnknown: TTFTAt(with(func(fl *TTFTFleet) {
			fl.FlowControl = FlowControl{State: FlowControlOn, QueueThreshold: 5}
		}), 1, 0.9),
		AboveDecodeOnset: TTFTAt(with(func(fl *TTFTFleet) {
			fl.FlowControl = FlowControl{State: FlowControlOn, QueueThreshold: 5, KVThreshold: 0.8}
		}), 12.6, 0.9),
	}
}

// Every TTFT refusal for a fleet input, in check order.
func TestTTFTRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		set  func(*TTFTFleet)
		want Reason
	}{
		"too many prefill pods":  {func(fl *TTFTFleet) { fl.Prefill.Replicas, fl.Prefill.Imbalance = maxPrefillReplicas+1, 1 }, InvalidInput},
		"f below 1":              {func(fl *TTFTFleet) { fl.Prefill.Imbalance = 0.5 }, InvalidInput},
		"f above the pods":       {func(fl *TTFTFleet) { fl.Prefill.Imbalance = 1.5 }, InvalidInput},
		"no prompt":              {func(fl *TTFTFleet) { fl.Prefill.PromptTokens = 0 }, NoTraffic},
		"negative cv":            {func(fl *TTFTFleet) { fl.Prefill.PromptCV = -1 }, NoTraffic},
		"cv above 10":            {func(fl *TTFTFleet) { fl.Prefill.PromptCV = 11 }, NoTraffic},
		"negative Ca^2":          {func(fl *TTFTFleet) { fl.Prefill.ArrivalCV2 = -1 }, InvalidInput},
		"negative transfer":      {func(fl *TTFTFleet) { fl.Prefill.TransferSecondsPerToken = -1 }, InvalidInput},
		"bad flow-control state": {func(fl *TTFTFleet) { fl.FlowControl.State = 7 }, InvalidInput},
		"decode refused":         {func(fl *TTFTFleet) { fl.Decode.Replicas = 0 }, NoDecodeReplicas},
	} {
		fl := workedFleet()
		d := *fl.Decode
		fl.Decode = &d
		c.set(&fl)
		for q, r := range map[string]Result{
			"TTFTAt": TTFTAt(fl, 1, 0.9), "RateForTTFT": RateForTTFT(fl, TTFTTarget{Seconds: 0.2, Percentile: 0.9}),
			"FlowControlOnsets": FlowControlOnsets(fl),
		} {
			if u, ok := r.(Unanswerable); !ok || u.Reason != c.want {
				t.Errorf("%s, %s: %#v, want %s", name, q, r, c.want)
			}
		}
	}
	fl := workedFleet()
	for name, r := range map[string]Result{
		"percentile 1":        TTFTAt(fl, 1, 1),
		"negative percentile": TTFTAt(fl, 1, -0.5),
		"negative rate":       TTFTAt(fl, -1, 0.9),
		"zero target":         RateForTTFT(fl, TTFTTarget{Seconds: 0, Percentile: 0.9}),
		"target percentile 1": RateForTTFT(fl, TTFTTarget{Seconds: 0.2, Percentile: 1}),
	} {
		if !isInvalid(r) {
			t.Errorf("%s: %#v, want InvalidInput", name, r)
		}
	}
}

package whatif

import (
	"fmt"
	"testing"
	"time"
)

// noTermCase is a fleet priced with no decode term (pl.term nil): no decode
// line, or the legacy sum. Those are the prices whose old-sum floor is never
// read.
type noTermCase struct {
	name   string
	fleet  func() TTFTFleet
	target TTFTTarget
	rate   float64
}

func noTermCases() []noTermCase {
	measured := func() TTFTFleet {
		fl := prefillOnly(2)
		fl.Prefill.Replicas, fl.Prefill.Imbalance = 3, 1.2
		fl.DecodeTTFT = DecodeTTFT{Seconds: 0.03, PromptTokens: 1000}
		return fl
	}
	legacy := func() TTFTFleet {
		fl := workedFleet()
		fl.LegacyDecodeSum = true
		return fl
	}
	return []noTermCase{
		{"prefill only, cv 1, p90", func() TTFTFleet { return prefillOnly(1) }, TTFTTarget{Seconds: 0.1, Percentile: 0.9}, 12},
		{"prefill only, cv 0.5, p99", func() TTFTFleet { return prefillOnly(0.5) }, TTFTTarget{Seconds: 0.2, Percentile: 0.99}, 20},
		{"prefill only, cv 1, mean", func() TTFTFleet { return prefillOnly(1) }, TTFTTarget{Seconds: 0.08}, 12},
		{"decode TTFT measured, cv 2, p95", measured, TTFTTarget{Seconds: 0.3, Percentile: 0.95}, 30},
		{"legacy decode sum, p90", legacy, TTFTTarget{Seconds: 0.2, Percentile: 0.9}, 8},
	}
}

// describe is a result in full, every float at the shortest form that reads
// back to the same bits, pointers followed.
func describe(r Result) string {
	switch v := r.(type) {
	case Answer:
		basis := v.Basis
		basis.TTFT = nil
		return fmt.Sprintf("answer %v flags %v band %s basis %+v ttft %s sensitivity %s",
			v.Value, v.Flags, followed(v.Band), basis, followed(v.Basis.TTFT), followed(v.Sensitivity))
	case TTFT:
		s := v.Sensitivity
		v.Sensitivity = nil
		return fmt.Sprintf("ttft %+v sensitivity %s", v, followed(s))
	default:
		return fmt.Sprintf("%T %+v", r, r)
	}
}

// followed is *p, or nil.
func followed[T any](p *T) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprintf("%+v", *p)
}

// noTermFigures pins every no-term figure as the price gave it while it still
// worked out the old-sum floor it then threw away (llm-scaler/main e4d247645):
// skipping that floor must move no figure, at any bit.
var noTermFigures = map[string][2]string{
	"prefill only, cv 1, p90": {
		`answer 3.654632803270488 flags [UnvalidatedTTFT AssumedVP AssumedPromptSpread TransferOmitted DecodeTTFTUnmeasured DecodeSideOmitted] band nil basis {Fill:0 KneeRate:0 Floor:0 Uncapped:0 BoundBy:[TTFT] Tier: TTFT:<nil>} ttft {Floor:0.0893528364331349 KneeRate:30 PrefillOnlyRate:3.654632803270488} sensitivity nil`,
		`ttft {Seconds:0.1405213940552308 Independent:0.1405213940552308 Pooled:0.1405213940552306 Floor:0.0893528364331349 DecodeShare:0 Parts:{EngineWait:0.022222222222222227 OwnPrefill:0.04593333333333333 DecodeWait:0 FirstStep:0 DecodeEngine:0 RouterHop:0} PrefillUtilization:0.4 VPMarginApplied:0 WaitScale:0.055555555555555754 WaitOffset:0 DecodeWaitProbability:0 PrefillKnee:30 DecodeKnee:0 Onsets:{Prefill:0 Decode:0} Tier: Flags:[UnvalidatedTTFT AssumedVP AssumedPromptSpread TransferOmitted DecodeTTFTUnmeasured DecodeSideOmitted] Sensitivity:<nil>} sensitivity nil`,
	},
	"prefill only, cv 0.5, p99": {
		`answer 14.327862394038675 flags [UnvalidatedTTFT AssumedVP AssumedPromptSpread TransferOmitted DecodeTTFTUnmeasured DecodeSideOmitted] band nil basis {Fill:0 KneeRate:0 Floor:0 Uncapped:0 BoundBy:[TTFT] Tier: TTFT:<nil>} ttft {Floor:0.09630931262359675 KneeRate:30 PrefillOnlyRate:14.327862394038675} sensitivity nil`,
		`ttft {Seconds:0.30511240461331385 Independent:0.30511240461331385 Pooled:0.30511240461331385 Floor:0.09630931262359675 DecodeShare:0 Parts:{EngineWait:0.04166666666666666 OwnPrefill:0.04593333333333333 DecodeWait:0 FirstStep:0 DecodeEngine:0 RouterHop:0} PrefillUtilization:0.6666666666666666 VPMarginApplied:0 WaitScale:0.057654812897917726 WaitOffset:0.014376288549903788 DecodeWaitProbability:0 PrefillKnee:30 DecodeKnee:0 Onsets:{Prefill:0 Decode:0} Tier: Flags:[UnvalidatedTTFT AssumedVP AssumedPromptSpread TransferOmitted DecodeTTFTUnmeasured DecodeSideOmitted] Sensitivity:<nil>} sensitivity nil`,
	},
	"prefill only, cv 1, mean": {
		`answer 15.163204747764212 flags [UnvalidatedTTFT AssumedVP AssumedPromptSpread TransferOmitted DecodeTTFTUnmeasured DecodeSideOmitted] band nil basis {Fill:0 KneeRate:0 Floor:0 Uncapped:0 BoundBy:[TTFT] Tier: TTFT:<nil>} ttft {Floor:0.04593333333333333 KneeRate:30 PrefillOnlyRate:15.163204747764212} sensitivity nil`,
		`ttft {Seconds:0.06815555555555555 Independent:0.06815555555555555 Pooled:0.06815555555555555 Floor:0.04593333333333333 DecodeShare:0 Parts:{EngineWait:0.022222222222222227 OwnPrefill:0.04593333333333333 DecodeWait:0 FirstStep:0 DecodeEngine:0 RouterHop:0} PrefillUtilization:0.4 VPMarginApplied:0 WaitScale:0.055555555555555754 WaitOffset:0 DecodeWaitProbability:0 PrefillKnee:30 DecodeKnee:0 Onsets:{Prefill:0 Decode:0} Tier: Flags:[UnvalidatedTTFT AssumedVP AssumedPromptSpread TransferOmitted DecodeTTFTUnmeasured DecodeSideOmitted] Sensitivity:<nil>} sensitivity nil`,
	},
	"decode TTFT measured, cv 2, p95": {
		`answer 11.90767915572053 flags [UnvalidatedTTFT AssumedVP AssumedPromptSpread DecodeEngineMeasured] band nil basis {Fill:0 KneeRate:0 Floor:0 Uncapped:0 BoundBy:[TTFT] Tier: TTFT:<nil>} ttft {Floor:0.20394881832596726 KneeRate:75 PrefillOnlyRate:11.90767915572053} sensitivity nil`,
		`ttft {Seconds:0.448735842528878 Independent:0.448735842528878 Pooled:0.21294591189852957 Floor:0.20394881832596726 DecodeShare:0 Parts:{EngineWait:0.055555555555555546 OwnPrefill:0.04593333333333333 DecodeWait:0 FirstStep:0 DecodeEngine:0.03 RouterHop:0} PrefillUtilization:0.39999999999999997 VPMarginApplied:0 WaitScale:0.16590472818875196 WaitOffset:0 DecodeWaitProbability:0 PrefillKnee:75 DecodeKnee:0 Onsets:{Prefill:0 Decode:0} Tier: Flags:[UnvalidatedTTFT AssumedVP AssumedPromptSpread DecodeEngineMeasured] Sensitivity:<nil>} sensitivity nil`,
	},
	"legacy decode sum, p90": {
		`answer 11.638534256572129 flags [UnvalidatedTTFT AssumedVP AssumedPromptSpread TransferOmitted FlowControlInferredOff HeldByDecodeAdmission] band nil basis {Fill:0.5071772220374592 KneeRate:0 Floor:0 Uncapped:0 BoundBy:[TTFT decode admission] Tier: TTFT:<nil>} ttft {Floor:0.09157553643313489 KneeRate:13.312564951975148 PrefillOnlyRate:13.312564951975148} sensitivity nil`,
		`ttft {Seconds:0.11988559270651773 Independent:0.11988557446112186 Pooled:0.11988559270651751 Floor:0.09157553643313489 DecodeShare:1.0681688774185583e-07 Parts:{EngineWait:0.01212121212121212 OwnPrefill:0.04593333333333333 DecodeWait:2.010646245409999e-08 FirstStep:0.00262258538089853 DecodeEngine:0 RouterHop:0} PrefillUtilization:0.26666666666666666 VPMarginApplied:0 WaitScale:0.04545454545454584 WaitOffset:0 DecodeWaitProbability:1.0681688774185583e-07 PrefillKnee:30 DecodeKnee:13.312564951975148 Onsets:{Prefill:0 Decode:0} Tier: Flags:[UnvalidatedTTFT AssumedVP AssumedPromptSpread TransferOmitted FlowControlInferredOff] Sensitivity:<nil>} sensitivity nil`,
	},
}

// With no decode term, the price reads no old-sum floor, so skipping it moves
// nothing: RateForTTFT and TTFTAt answer, to the bit, what they answered while
// the floor was still worked out and thrown away.
func TestTTFT_NoTermFiguresUnmovedBySkippedFloor(t *testing.T) {
	for _, c := range noTermCases() {
		got := [2]string{describe(rateForTTFT(t, c.fleet(), c.target)), describe(ttftAt(t, c.fleet(), c.rate, c.target.Percentile))}
		want, ok := noTermFigures[c.name]
		if !ok {
			t.Errorf("%s: no pinned figures; got\n%q", c.name, got)
			continue
		}
		for i, what := range []string{"RateForTTFT", "TTFTAt"} {
			if got[i] != want[i] {
				t.Errorf("%s, %s:\n got %s\nwant %s", c.name, what, got[i], want[i])
			}
		}
	}
}

// noTermRateBudget bounds a RateForTTFT with no decode line. It takes 0.1 to
// 3 ms on a developer machine; while each price worked out the unused old-sum
// floor of a zero own time it took 93 to 100 ms.
const noTermRateBudget = 20 * time.Millisecond

// A RateForTTFT with no decode line (the SLO analyzer's prefill-only shape) is
// cheap: no price works out a floor it does not read. The legacy sum is left
// out: its decode admission chain costs tens of ms on its own. The fastest of
// five calls is the figure, so a loaded machine slows it only if it slows
// every call.
func TestTTFT_NoTermRateForTTFTIsCheap(t *testing.T) {
	timed := 0
	for _, c := range noTermCases() {
		fl := c.fleet()
		if fl.Decode != nil {
			continue
		}
		fastest := time.Duration(1<<63 - 1)
		for range 5 {
			start := time.Now()
			rateForTTFT(t, fl, c.target)
			fastest = min(fastest, time.Since(start))
		}
		t.Logf("%s: fastest RateForTTFT %v, budget %v", c.name, fastest, noTermRateBudget)
		if fastest > noTermRateBudget {
			t.Errorf("%s: RateForTTFT took %v at the fastest of 5, budget %v", c.name, fastest, noTermRateBudget)
		}
		timed++
	}
	if timed != 4 {
		t.Fatalf("timed %d fleets with no decode line, want 4", timed)
	}
}

// BenchmarkRateForTTFTNoTerm is a RateForTTFT with no decode term, the SLO
// analyzer's prefill-only (S1) shape.
func BenchmarkRateForTTFTNoTerm(b *testing.B) {
	fl := prefillOnly(1)
	tg := TTFTTarget{Seconds: 0.1, Percentile: 0.9}
	for b.Loop() {
		RateForTTFT(fl, tg)
	}
}

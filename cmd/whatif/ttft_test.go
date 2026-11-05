package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/learnreplay"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/whatifsource/promtest"
)

// The prefill-stress goldens the TTFT half is read from. On these recordings
// no run gives both of P1's figures: run 4 (shortRun) learns a and refuses
// V_P, run 2 (saturatedRun) learns V_P and refuses a.
const (
	shortRun     = "../../internal/engines/analyzers/learnreplay/testdata/goldens/ttft-open-loop-short/recording.jsonl.gz"
	saturatedRun = "../../internal/engines/analyzers/learnreplay/testdata/goldens/ttft-saturated/recording.jsonl.gz"
	threePodsRun = "../../internal/engines/analyzers/learnreplay/testdata/goldens/ttft-three-pods/recording.jsonl.gz"
)

// workedTTFT is the page's worked example with its TTFT flags.
func workedTTFT(args ...string) []string {
	return slices.Concat(args, []string{"--recording", olsSweep, "--model", "model-02", "--prefill", "1", "--decode", "1",
		"--assume-vp", "30000"}, workedShape)
}

// The golden outputs of the TTFT half: both questions, in text and in JSON,
// answered and refused. go test ./cmd/whatif -run TestTTFTGolden -update
// rewrites them.
func TestTTFTGolden(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"ttft-replicas-worked", slices.Concat(workedTTFT("replicas-to-slo", "--ttft-percentile", "90"),
			[]string{"--tpot-percentile", "90", "--assume-tpot-ratio", "1.3"}), 0},
		{"ttft-replicas-worked-json", slices.Concat(workedTTFT("replicas-to-slo", "--ttft-percentile", "90"), []string{"-o", "json"}), 0},
		{"ttft-rate-worked", workedTTFT("slo-to-rate", "--tpot", "4ms", "--ttft", "200ms@p95"), 0},
		{"ttft-rate-worked-json", slices.Concat(workedTTFT("slo-to-rate", "--tpot", "4ms", "--ttft", "200ms@p95"), []string{"-o", "json"}), 0},
		// Run 4: a learned, V_P assumed, no decode line, so the decode
		// engine's TTFT in its place. Decode's TPOT refuses, and so exit 3.
		{"ttft-replicas-short", []string{"replicas-to-slo", "--recording", shortRun, "--assume-vp", "130000"}, 3},
		{"ttft-replicas-short-json", []string{"replicas-to-slo", "--recording", shortRun, "--assume-vp", "130000", "-o", "json"}, 3},
		// No TPOT asked, so nothing refuses.
		{"ttft-rate-short", []string{"slo-to-rate", "--recording", shortRun, "--assume-vp", "130000", "--ttft", "1s@p90"}, 0},
		// Saturated: at the knee, a refusal and not a number.
		{"ttft-replicas-saturated", []string{"replicas-to-slo", "--recording", shortRun, "--assume-vp", "130000",
			"--rate", "1000"}, 3},
		{"ttft-rate-below-floor", []string{"slo-to-rate", "--recording", shortRun, "--assume-vp", "130000", "--ttft", "1ms"}, 3},
		// Run 4 refuses V_P, and run 2 a: each names the input.
		{"ttft-no-vp", []string{"replicas-to-slo", "--recording", shortRun}, 3},
		{"ttft-no-vp-json", []string{"slo-to-rate", "--recording", shortRun, "--ttft", "1s", "-o", "json"}, 3},
		{"ttft-no-a", []string{"replicas-to-slo", "--recording", saturatedRun, "--ttft-percentile", "95"}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { checkGolden(t, c.name, c.code, c.args) })
	}
}

// The text carries the page's words, and the JSON its flags and its note: on
// both questions, UNVALIDATED always and V_P ASSUMED when --assume-vp
// supplied V_P.
func TestTTFT_SaysUnvalidatedAndAssumedInTextAndJSON(t *testing.T) {
	for _, q := range [][]string{workedTTFT("replicas-to-slo", "--ttft-percentile", "90"), workedTTFT("slo-to-rate", "--ttft", "1s@p90")} {
		text, code := runCmd(t, q...)
		require.Equal(t, 0, code, text)
		assert.Contains(t, text, "UNVALIDATED; V_P ASSUMED")
		assert.Contains(t, text, "reads low at light load, measured up to about 34% under the router's p90, and is not a bound")
		assert.Contains(t, text, "UnvalidatedTTFT")
		assert.Contains(t, text, "AssumedVP")
		// Each assumed input is named.
		for _, name := range []string{"V_P 30000 tok/s (--assume-vp)", "prompt cv 2", "Poisson arrivals", "flow control off (no series)"} {
			assert.Contains(t, text, name)
		}

		out, code := runCmd(t, slices.Concat(q, []string{"-o", "json"})...)
		require.Equal(t, 0, code, out)
		w := decodeOutput(t, out)
		assert.Contains(t, w.Note, "UNVALIDATED")
		assert.Contains(t, w.Note, "V_P ASSUMED")
		assert.Contains(t, w.Note, "is not a bound")
		i := slices.IndexFunc(w.Roles, func(r wireRole) bool { return r.Role == rolePrefill })
		require.GreaterOrEqual(t, i, 0, out)
		r := w.Roles[i].Results.TTFT
		if r == nil {
			r = w.Roles[i].Results.Rate
		}
		require.NotNil(t, r.Answer, out)
		for _, fl := range []whatif.Flag{whatif.UnvalidatedTTFT, whatif.AssumedVP, whatif.AssumedPromptSpread, assumedPoisson} {
			assert.Contains(t, r.Answer.Flags, string(fl))
		}
		assert.Equal(t, "assumed", w.Roles[i].TTFT.VPSource)
		assert.Contains(t, w.Roles[i].TTFT.Assumed, "V_P 30000 tok/s (--assume-vp)")
	}
}

// Without --assume-vp a TTFT note says nothing of an assumed V_P.
func TestTTFT_NoteIsSilentOnALearnedVP(t *testing.T) {
	fleets := []*pricedFleet{{}, {}}
	assert.Equal(t, ttftNote, footerNote(fleets))
	fleets[1].assumedVP = true
	assert.Equal(t, ttftNote+"; V_P ASSUMED", footerNote(fleets))
}

// The asked-for TTFT is priced from the same fleet the page's worked example
// computes by hand. The page assumes cv 1, and a 1.6 ms above P1's: the
// figures are the page's, read 1.6 ms lower as it says they would.
func TestTTFT_TheWorkedExampleFromTheRecording(t *testing.T) {
	st := replayed(t, olsSweep, "model-02")
	f := answerFlags{question: "replicas-to-slo", set: map[string]bool{"assume-vp": true, "prefill": true, "decode": true,
		"assume-input-tokens": true, "assume-output-tokens": true}, assumeVP: 30000, prefill: 1, decode: 1,
		inputTokens: 1000, outputTokens: 1000}
	variants, err := f.decodeVariants(st)
	require.NoError(t, err)
	require.Len(t, variants, 1)
	fleets, err := f.ttftFleets(st, ttftQuestion{q: 0.9}, variants)
	require.NoError(t, err)
	require.Nil(t, fleets[0].refused)
	fl := fleets[0].fleet
	require.NotNil(t, fl.Decode, "the worked example prices decode by its line")
	near(t, "a", fl.Prefill.FixedLatency, 0.01104, 0.0001)
	assert.False(t, fl.Prefill.VPMargin != 0, "an assumed V_P carries no margin")
	fl.Prefill.PromptCV = 1

	got, ok := whatif.TTFTAt(fl, 11.32, 0.9).(whatif.TTFT)
	require.True(t, ok)
	near(t, "TTFT p90", got.Seconds, 0.160-0.0016, 0.001)
	near(t, "independent", got.Independent, 0.154-0.0016, 0.001)
	near(t, "floor", got.Floor, 0.0916-0.0016, 0.001)
	near(t, "rho", got.PrefillUtilization, 0.377, 0.001)
	got95, ok := whatif.TTFTAt(fl, 11.32, 0.95).(whatif.TTFT)
	require.True(t, ok)
	near(t, "TTFT p95", got95.Seconds, 0.231-0.0016, 0.001)

	rate, ok := whatif.RateForTTFT(fl, whatif.TTFTTarget{Seconds: 0.2, Percentile: 0.95}).(whatif.Answer)
	require.True(t, ok)
	near(t, "the rate at 200 ms p95", rate.Value, 11.10, 0.05)
}

// replayed is the state at the recording's last cycle.
func replayed(t *testing.T, path, model string) *learnreplay.State {
	t.Helper()
	f := answerFlags{recording: path, model: model, cycle: "last", window: captureFlags{}}
	st, err := f.readState(context.Background())
	require.NoError(t, err)
	return st
}

// learnedState is a state whose one prefill variant has both of P1's figures,
// which no committed recording does at one cycle.
func learnedState() *learnreplay.State {
	return &learnreplay.State{Namespace: "ns", ModelID: "m", Cycle: 40, Prefill: []learnreplay.PrefillState{{
		Variant: "pre", Pods: 1, EstimateRead: true, PromptTokens: 2000, PromptFromCycle: 21, PromptToCycle: 40,
		Estimate: prefill.Estimate{VP: 100000, VPSource: prefill.SourceBusyMinutes, VPPromptTokens: 2000, A: 0.01},
	}}}
}

// fleetOf reads the fleet with flags set by name, as check would mark them.
func fleetOf(t *testing.T, st *learnreplay.State, f answerFlags, set ...string) *pricedFleet {
	t.Helper()
	f.set = map[string]bool{}
	for _, name := range set {
		f.set[name] = true
	}
	ps, err := f.prefillState(st)
	require.NoError(t, err)
	return f.fleetFor(st, ps, ttftQuestion{q: 0.9}, nil)
}

// V_P's order is the page's: learned, else --assume-vp, else a refusal. It
// never defaults, and an --assume-vp a learned V_P leaves unused says so.
func TestTTFT_VPOrder(t *testing.T) {
	t.Run("learned", func(t *testing.T) {
		pf := fleetOf(t, learnedState(), answerFlags{})
		require.Nil(t, pf.refused)
		assert.Equal(t, 100000.0, pf.fleet.Prefill.VP)
		assert.False(t, pf.fleet.Prefill.VPAssumed)
		assert.Equal(t, whatif.DefaultVPMargin, pf.fleet.Prefill.VPMargin)
		assert.Equal(t, "learned", pf.in.VPSource)
		assert.Equal(t, "UNVALIDATED", pf.verdict())
	})
	t.Run("learned beats --assume-vp, and says so", func(t *testing.T) {
		pf := fleetOf(t, learnedState(), answerFlags{assumeVP: 5000}, "assume-vp")
		require.Nil(t, pf.refused)
		assert.Equal(t, 100000.0, pf.fleet.Prefill.VP)
		assert.False(t, pf.assumedVP)
		require.NotNil(t, pf.in.AssumeVPNotUsed)
		assert.Equal(t, 5000.0, *pf.in.AssumeVPNotUsed)
		assert.Contains(t, pf.inputsText, "--assume-vp 5000 not used")
	})
	refusedVP := func() *learnreplay.State {
		st := learnedState()
		st.Prefill[0].Estimate.VP, st.Prefill[0].Estimate.VPRefused = 0, prefill.NoPrefillCapacity
		return st
	}
	t.Run("assumed where P1 refused it", func(t *testing.T) {
		pf := fleetOf(t, refusedVP(), answerFlags{assumeVP: 5000}, "assume-vp")
		require.Nil(t, pf.refused)
		assert.Equal(t, 5000.0, pf.fleet.Prefill.VP)
		assert.True(t, pf.fleet.Prefill.VPAssumed)
		assert.Zero(t, pf.fleet.Prefill.VPMargin)
		assert.Equal(t, "UNVALIDATED; V_P ASSUMED", pf.verdict())
	})
	t.Run("refused with neither", func(t *testing.T) {
		pf := fleetOf(t, refusedVP(), answerFlags{})
		require.NotNil(t, pf.refused)
		assert.Equal(t, whatif.NoPrefillCapacity, pf.refused.Reason)
		assert.Contains(t, pf.refused.Detail, "--assume-vp")
		assert.Equal(t, 3, whatif.ExitCode(*pf.refused))
	})
	t.Run("no estimate at the cycle", func(t *testing.T) {
		st := learnedState()
		st.Prefill[0] = learnreplay.PrefillState{Variant: "pre", Pods: 1}
		pf := fleetOf(t, st, answerFlags{})
		require.NotNil(t, pf.refused)
		assert.Equal(t, whatif.NoPrefillCapacity, pf.refused.Reason)
		assert.Contains(t, pf.refused.Detail, "no P1 estimate")
		// --assume-vp gives V_P, not a.
		pf = fleetOf(t, st, answerFlags{assumeVP: 5000}, "assume-vp")
		require.NotNil(t, pf.refused)
		assert.Equal(t, whatif.NoFixedLatency, pf.refused.Reason)
	})
	t.Run("a has no assumption", func(t *testing.T) {
		st := learnedState()
		st.Prefill[0].Estimate.A, st.Prefill[0].Estimate.ARefused = 0, prefill.TooFewLowLoadMinutes
		pf := fleetOf(t, st, answerFlags{})
		require.NotNil(t, pf.refused)
		assert.Equal(t, whatif.NoFixedLatency, pf.refused.Reason)
	})
}

// f at a count, by S1's decision 1.
func TestTTFT_ImbalanceAt(t *testing.T) {
	learned := prefill.Estimate{FByPods: map[int]float64{3: 1.3}}
	for _, c := range []struct {
		name    string
		ps      learnreplay.PrefillState
		pods    int
		want    float64
		assumed bool
	}{
		{"one pod is exact", learnreplay.PrefillState{Pods: 1}, 1, 0, false},
		{"learned at the count", learnreplay.PrefillState{Pods: 3, Estimate: learned}, 3, 1.3, false},
		{"learned at the count, the raw share unread", learnreplay.PrefillState{Pods: 3, Estimate: learned, RawShare: 2.5}, 3, 1.3, false},
		{"raw share above the floor", learnreplay.PrefillState{Pods: 3, RawShare: 2}, 3, 2, true},
		{"raw share below the floor", learnreplay.PrefillState{Pods: 3, RawShare: 1.1}, 3, 1.5, true},
		{"no computed-token rate", learnreplay.PrefillState{Pods: 3}, 3, 1.5, true},
		{"capped at the pods", learnreplay.PrefillState{Pods: 4, RawShare: 3.9}, 2, 2, true},
		{"a learned f is not carried up unchanged", learnreplay.PrefillState{Pods: 3, Estimate: learned}, 5, 1.5, true},
		{"a learned f is not carried down unchanged", learnreplay.PrefillState{Pods: 3, Estimate: learned}, 2, 1.5, true},
		{"one pod's exact 1 does not reach two", learnreplay.PrefillState{Pods: 1, RawShare: 1}, 2, 1.5, true},
		{"down to one pod is exact", learnreplay.PrefillState{Pods: 3, Estimate: learned}, 1, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, assumed, _ := imbalanceAt(&c.ps, c.pods)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.assumed, assumed)
		})
	}
}

// An assumed f is named, and P3 flags it.
func TestTTFT_AssumedImbalanceIsNamedAndFlagged(t *testing.T) {
	pf := fleetOf(t, learnedState(), answerFlags{prefill: 3}, "prefill")
	require.Nil(t, pf.refused)
	assert.Equal(t, 3, pf.fleet.Prefill.Replicas)
	assert.Equal(t, 1.5, pf.fleet.Prefill.Imbalance)
	assert.Contains(t, pf.in.Assumed, "f 1.5 on 3 pods (none learned at that count)")
	got := pf.priced(func(fl whatif.TTFTFleet) whatif.Result { return whatif.TTFTAt(fl, 1, 0.9) })
	require.IsType(t, whatif.TTFT{}, got)
	assert.Contains(t, got.(whatif.TTFT).Flags, whatif.AssumedImbalance)
}

// Flow control is on where its series exist, at the upstream thresholds,
// named as assumed; else off, inferred.
func TestTTFT_FlowControl(t *testing.T) {
	st := learnedState()
	pf := fleetOf(t, st, answerFlags{})
	assert.Equal(t, whatif.FlowControlNoSeries, pf.fleet.FlowControl.State)
	assert.Contains(t, pf.in.Assumed, "flow control off (no series)")

	st.FlowControlSeries = true
	pf = fleetOf(t, st, answerFlags{})
	assert.Equal(t, whatif.FlowControlOn, pf.fleet.FlowControl.State)
	assert.Equal(t, 5.0, pf.fleet.FlowControl.QueueThreshold)
	assert.Equal(t, 0.8, pf.fleet.FlowControl.KVThreshold)
	assert.Contains(t, pf.in.Assumed, "flow-control thresholds 5 and 0.8 (upstream defaults)")
}

// The decode side: a line that prices, else the engine's measured TTFT, else
// nothing, which P3 flags as left out.
func TestTTFT_DecodeSide(t *testing.T) {
	st := replayed(t, olsSweep, "model-02")
	f := answerFlags{set: map[string]bool{"assume-vp": true}, assumeVP: 30000}
	variants, err := f.decodeVariants(st)
	require.NoError(t, err)
	ps, err := f.prefillState(st)
	require.NoError(t, err)

	pf := f.fleetFor(st, ps, ttftQuestion{q: 0.9}, &variants[0])
	require.Nil(t, pf.refused, "%v", pf.refused)
	assert.Equal(t, "line", pf.in.DecodeSide)
	require.NotNil(t, pf.fleet.Decode)

	// No line: the engine's TTFT, flagged; the KV pull is in it.
	noLine := variants[0]
	noLine.decode.Line.A, noLine.decode.Line.B = 0, 0
	noLine.state.DecodeTTFT.EngineTTFT = whatif.DecodeTTFT{Seconds: 0.03, PromptTokens: 1000}
	noLine.state.DecodeTTFT.TransferSeconds = 0.01
	pf = f.fleetFor(st, ps, ttftQuestion{q: 0.9}, &noLine)
	assert.Nil(t, pf.fleet.Decode)
	assert.Equal(t, "decode engine", pf.in.DecodeSide)
	assert.Equal(t, "decode engine", pf.in.TransferSource)
	assert.Zero(t, pf.in.TransferSecondsPerToken)
	got := pf.priced(func(fl whatif.TTFTFleet) whatif.Result { return whatif.TTFTAt(fl, 1, 0.9) })
	require.IsType(t, whatif.TTFT{}, got)
	assert.Contains(t, got.(whatif.TTFT).Flags, whatif.DecodeEngineMeasured)

	// Neither: left out, and flagged. The transfer measured still counts.
	noLine.state.DecodeTTFT.EngineTTFT = whatif.DecodeTTFT{}
	pf = f.fleetFor(st, ps, ttftQuestion{q: 0.9}, &noLine)
	assert.Equal(t, "omitted", pf.in.DecodeSide)
	assert.Equal(t, "measured", pf.in.TransferSource)
	got = pf.priced(func(fl whatif.TTFTFleet) whatif.Result { return whatif.TTFTAt(fl, 1, 0.9) })
	require.IsType(t, whatif.TTFT{}, got)
	assert.Contains(t, got.(whatif.TTFT).Flags, whatif.DecodeSideOmitted)
	assert.Contains(t, got.(whatif.TTFT).Flags, whatif.DecodeTTFTUnmeasured)
}

// The prompt: assumed, else what the prefill pods computed, else the decode
// variant's; an unread one is the library's NoTraffic.
func TestTTFT_PromptSource(t *testing.T) {
	pf := fleetOf(t, learnedState(), answerFlags{})
	assert.Equal(t, 2000.0, pf.fleet.Prefill.PromptTokens)
	assert.Equal(t, "measured", pf.in.PromptSource)
	assert.Equal(t, 21, pf.in.PromptFromCycle)

	pf = fleetOf(t, learnedState(), answerFlags{inputTokens: 3000}, "assume-input-tokens")
	assert.Equal(t, 3000.0, pf.fleet.Prefill.PromptTokens)
	assert.Contains(t, pf.in.Assumed, "prompt 3000 tokens (--assume-input-tokens)")

	st := learnedState()
	st.Prefill[0].PromptTokens = 0
	pf = fleetOf(t, st, answerFlags{})
	got := pf.priced(func(fl whatif.TTFTFleet) whatif.Result { return whatif.TTFTAt(fl, 1, 0.9) })
	require.IsType(t, whatif.Unanswerable{}, got)
	assert.Equal(t, whatif.NoTraffic, got.(whatif.Unanswerable).Reason)
}

// Aggregated TTFT is M1: refused, and the refusal says so, whether the
// recording holds the aggregated variant alone or beside a prefill one.
func TestTTFT_AggregatedIsRefused(t *testing.T) {
	st := &learnreplay.State{Namespace: "ns", ModelID: "m", Cycle: 3,
		Variants: []learnreplay.VariantState{{Variant: "agg", Role: "both"}}}
	f := answerFlags{set: map[string]bool{"assume-vp": true}, assumeVP: 30000}
	fleets, err := f.ttftFleets(st, ttftQuestion{q: 0.9}, []pricedVariant{{state: st.Variants[0]}})
	require.NoError(t, err)
	require.NotNil(t, fleets[0].refused)
	assert.Equal(t, whatif.NoPrefillReplicas, fleets[0].refused.Reason)
	assert.Contains(t, fleets[0].refused.Detail, "aggregated TTFT is not built")

	// --variant left the aggregated variant out of what is priced.
	assert.Contains(t, noPrefillRole(st).Detail, "aggregated TTFT is not built")
	assert.Contains(t, noPrefillRole(&learnreplay.State{Namespace: "ns", ModelID: "m", Cycle: 3}).Detail, "no prefill variant at cycle 3")

	mixed := learnedState()
	mixed.Variants = st.Variants
	fleets, err = f.ttftFleets(mixed, ttftQuestion{q: 0.9}, []pricedVariant{{state: st.Variants[0]}})
	require.NoError(t, err)
	require.NotNil(t, fleets[0].refused)
	assert.Contains(t, fleets[0].refused.Detail, "aggregated TTFT is not built")

	// Through the command, from a window of aggregated pods.
	srv := sweepServer(t)
	window := sweepWindow(srv.URL)
	i := slices.Index(window, "decode=app=m-dec")
	require.GreaterOrEqual(t, i, 0)
	window[i] = "both=app=m-dec"
	out, code := runCmd(t, slices.Concat([]string{"slo-to-rate"}, window, []string{"--ttft", "1s", "--assume-vp", "30000"})...)
	require.Equal(t, 3, code, out)
	assert.Contains(t, out, "aggregated TTFT is not built")
}

// Several prefill variants are one to name: the CLI prices one fleet.
func TestTTFT_SeveralPrefillVariants(t *testing.T) {
	st := learnedState()
	st.Prefill = append(st.Prefill, learnreplay.PrefillState{Variant: "pre-b"})
	f := answerFlags{set: map[string]bool{}}
	_, err := f.prefillState(st)
	require.Error(t, err)
	assert.Equal(t, 2, exitCode(err))
	assert.Contains(t, err.Error(), "pre, pre-b")

	f.prefillVariant = "pre-b"
	ps, err := f.prefillState(st)
	require.NoError(t, err)
	assert.Equal(t, "pre-b", ps.Variant)

	f.prefillVariant = "nope"
	_, err = f.prefillState(st)
	require.Error(t, err)
	assert.Equal(t, 2, exitCode(err))
}

// Every usage error is exit 2, whatever the recording would have said, and
// with -o json one error object.
func TestTTFT_UsageErrors(t *testing.T) {
	q2 := []string{"replicas-to-slo", "--recording", shortRun}
	q1 := []string{"slo-to-rate", "--recording", shortRun, "--tpot", "4ms"}
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"a percentile of 0", append(slices.Clone(q2), "--ttft-percentile", "0"), "no percentile"},
		{"a percentile of 100", append(slices.Clone(q2), "--ttft-percentile", "100"), "below 100"},
		{"a NaN percentile", append(slices.Clone(q2), "--ttft-percentile", "NaN"), "below 100"},
		{"a percentile that is no number", append(slices.Clone(q2), "--ttft-percentile", "x"), "--ttft-percentile"},
		{"--assume-vp 0", append(slices.Clone(q2), "--assume-vp", "0"), "--assume-vp"},
		{"--assume-vp NaN", append(slices.Clone(q2), "--assume-vp", "NaN"), "--assume-vp"},
		{"--assume-vp Inf", append(slices.Clone(q2), "--assume-vp", "Inf"), "--assume-vp"},
		{"--assume-vp below 0", append(slices.Clone(q2), "--assume-vp", "-1"), "--assume-vp"},
		{"--prefill below 0", append(slices.Clone(q2), "--prefill", "-1"), "--prefill"},
		{"a TTFT target on question 2", append(slices.Clone(q2), "--ttft", "1s"), "for slo-to-rate"},
		{"an unknown variant", append(slices.Clone(q2), "--prefill-variant", "nope"), "no prefill variant"},
		{"a TTFT flag with no target", append(slices.Clone(q1), "--assume-vp", "1000"), "need a --ttft target"},
		{"--prefill with no target", append(slices.Clone(q1), "--prefill", "2"), "need a --ttft target"},
		{"a target that is no duration", append(slices.Clone(q1), "--ttft", "fast"), "--ttft"},
		{"a target of 0", append(slices.Clone(q1), "--ttft", "0s"), "above 0"},
		{"a target below 0", append(slices.Clone(q1), "--ttft", "-1s"), "above 0"},
		{"two percentiles", append(slices.Clone(q1), "--ttft", "1s@p90", "--ttft-percentile", "95"), "give one"},
		{"a percentile written wrongly", append(slices.Clone(q1), "--ttft", "1s@90"), "@p90"},
		{"a target percentile of 100", append(slices.Clone(q1), "--ttft", "1s@p100"), "below 100"},
		{"a per-bucket answer", append(slices.Clone(q1), "--input-bucket", "huge"), "the planner's M1"},
		{"a headroom above 1", append(slices.Clone(q1), "--ttft", "1s", "--headroom", "1.5"), "--headroom"},
		{"a headroom of 0", append(slices.Clone(q2), "--assume-vp", "1000", "--headroom", "0"), "--headroom"},
		{"no target at all", []string{"slo-to-rate", "--recording", shortRun}, "--tpot is required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := run(context.Background(), c.args, io.Discard, io.Discard)
			require.Error(t, err)
			assert.Equal(t, 2, exitCode(err), "%v", err)
			assert.Contains(t, err.Error(), c.want)

			out, code := runCmd(t, slices.Concat(c.args, []string{"-o", "json"})...)
			assert.Equal(t, 2, code, out)
			w := decodeOutput(t, out)
			require.NotNil(t, w.Error, out)
			assert.Contains(t, w.Error.Message, c.want)
		})
	}
}

// A saturated fleet is a refusal on both exit paths, never a number.
func TestTTFT_SaturatedIsARefusal(t *testing.T) {
	out, code := runCmd(t, "replicas-to-slo", "--recording", shortRun, "--assume-vp", "130000", "--rate", "1000", "-o", "json")
	require.Equal(t, 3, code, out)
	w := decodeOutput(t, out)
	i := slices.IndexFunc(w.Roles, func(r wireRole) bool { return r.Role == rolePrefill })
	require.GreaterOrEqual(t, i, 0, out)
	r := w.Roles[i].Results.TTFT
	require.NotNil(t, r)
	assert.Nil(t, r.Answer, "a number at the knee")
	require.NotNil(t, r.Refused)
	assert.Equal(t, string(whatif.Saturated), r.Refused.Reason)

	text, code := runCmd(t, "replicas-to-slo", "--recording", shortRun, "--assume-vp", "130000", "--rate", "1000")
	require.Equal(t, 3, code, text)
	assert.Contains(t, text, "refused: Saturated")
	assert.NotContains(t, text, " ms   ", "a TTFT row at the knee")
}

// Question 2's operating rate is the fleet's: the prefill knee sets it where
// it is the lower, and the TPOT is read at that rate.
func TestTTFT_ThePrefillKneeBindsTheOperatingRate(t *testing.T) {
	low := workedTTFT("replicas-to-slo")
	low[slices.Index(low, "30000")] = "3000" // a prefill knee of 3 r/s
	out, code := runCmd(t, slices.Concat(low, []string{"-o", "json"})...)
	require.Equal(t, 0, code, out)
	w := decodeOutput(t, out)
	require.Len(t, w.Roles, 2)
	rate := w.Roles[0].Results.OperatingRate.Answer
	require.NotNil(t, rate, out)
	assert.InDelta(t, 0.85*3, rate.Value, 1e-9)
	assert.Nil(t, rate.Band, "a rate the prefill knee set has no band")
	require.NotNil(t, w.Roles[1].TTFT.Rate)
	assert.InDelta(t, 0.85*3, *w.Roles[1].TTFT.Rate, 1e-9)
	assert.Equal(t, "headroom x prefill knee", w.Roles[1].TTFT.RateSource)
	text, _ := runCmd(t, low...)
	assert.Contains(t, text, "0.85 x prefill knee (scaleUpThreshold)")

	high := workedTTFT("replicas-to-slo")
	out, code = runCmd(t, slices.Concat(high, []string{"-o", "json"})...)
	require.Equal(t, 0, code, out)
	w = decodeOutput(t, out)
	assert.Equal(t, "headroom x decode knee", w.Roles[1].TTFT.RateSource)
}

// The fleet row is the smallest of the role rates and the operating point of
// the lower knee, each named; on a tie, every one, the cap first.
func TestFleetRate(t *testing.T) {
	q := ttftQuestion{q: 0.95}
	ttft := func(v, knee float64) whatif.Answer {
		return whatif.Answer{Value: v, Flags: []whatif.Flag{whatif.UnvalidatedTTFT},
			Basis: whatif.Basis{TTFT: &whatif.TTFTBasis{KneeRate: knee}}}
	}
	decode := whatif.Answer{Value: 9, Flags: []whatif.Flag{whatif.HeldLine}, Basis: whatif.Basis{BoundBy: []whatif.Bound{whatif.BoundTPOT}}}
	for _, c := range []struct {
		name   string
		decode whatif.Result
		ttft   whatif.Answer
		want   float64
		by     []string
	}{
		{"the target", nil, ttft(4, 10), 4, []string{"prefill TTFT p95"}},
		{"the cap", nil, ttft(9.5, 10), 8.5, []string{"operating-point cap"}},
		{"decode", decode, ttft(9.5, 20), 9, []string{"decode TPOT"}},
		{"a tie", nil, ttft(8.5, 10), 8.5, []string{"operating-point cap", "prefill TTFT p95"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := fleetRate(q, 0.85, c.decode, c.ttft).(whatif.Answer)
			require.True(t, ok)
			assert.Equal(t, c.want, got.Value)
			var by []string
			for _, b := range got.Basis.BoundBy {
				by = append(by, string(b))
			}
			assert.Equal(t, c.by, by)
			assert.True(t, got.HasFlag(whatif.UnvalidatedTTFT))
		})
	}
	u := whatif.Unanswerable{Reason: whatif.NoLine}
	assert.Equal(t, u, fleetRate(q, 0.85, u, ttft(4, 10)))
	assert.Equal(t, u, fleetRate(q, 0.85, decode, u))
}

// The percentile of question 2: the flag, else the policy's, else 90.
func TestTTFT_PercentileOrder(t *testing.T) {
	st := replayed(t, olsSweep, "model-02")
	f := answerFlags{set: map[string]bool{"assume-vp": true}}
	q, err := f.replicasTTFT(st)
	require.NoError(t, err)
	require.NotNil(t, q)
	assert.Equal(t, 0.9, q.q)

	f = answerFlags{set: map[string]bool{"ttft-percentile": true}, ttftPercentile: "p99.5"}
	q, err = f.replicasTTFT(st)
	require.NoError(t, err)
	assert.InDelta(t, 0.995, q.q, 1e-12)
	assert.Equal(t, "TTFT p99.5", q.label())

	f = answerFlags{set: map[string]bool{}}
	q, err = f.replicasTTFT(st)
	require.NoError(t, err)
	assert.Nil(t, q, "no TTFT flag asks no TTFT")
}

// Every TTFT flag asks about TTFT on question 2.
func TestTTFT_EachFlagAsks(t *testing.T) {
	for _, flag := range [][]string{{"--ttft-percentile", "90"}, {"--assume-vp", "30000"}, {"--prefill", "1"},
		{"--prefill-variant", "variant-02"}} {
		text, _ := runCmd(t, slices.Concat([]string{"replicas-to-slo", "--recording", shortRun}, flag)...)
		assert.Contains(t, text, "TTFT p90", "%v", flag)
		assert.Contains(t, text, "UNVALIDATED", "%v", flag)
	}
}

// A P/D window for the mode equivalence: the sweep's decode variant and one
// prefill pod. Its prompt alternates, so P1 fits a from its low-load minutes.
const (
	pdPromptHold = 20
	pdFixed      = 0.010
	pdTokenCost  = 0.00001
	pdRate       = 0.5
)

func pdPrompt(i int) float64 {
	if (i/pdPromptHold)%2 == 0 {
		return 1000
	}
	return 4000
}

func pdService(i int) float64 { return pdFixed + pdPrompt(i)*pdTokenCost }

// pdServer is sweepServer with the prefill pod. kv, rates and service time are
// the series a prefill row reads.
func pdServer(t *testing.T) *promtest.Server {
	t.Helper()
	srv := sweepServer(t)
	reg := source.NewSourceRegistry()
	require.NoError(t, reg.Register("prometheus", source.NewNoOpSource()))
	registration.RegisterSaturationQueries(reg)
	registration.RegisterQueueingModelQueries(reg)
	registration.RegisterSLOQueries(reg)
	registration.RegisterFlowControlPoolQueries(reg)
	registration.RegisterAlwaysOnQueries(reg)
	q := func(name string) string {
		name = registration.EngineQuery(inferenceengine.EngineVLLM, name)
		s, err := reg.Get("prometheus").QueryList().Build(name, map[string]string{source.ParamNamespace: sweepNS})
		require.NoError(t, err, name)
		return s
	}
	labels := map[string]string{"model_name": "m", "pod": "m-pre-7c9d-0", "instance": "10.0.1.1:8000", "namespace": sweepNS}
	add := func(name string, f func(i int) float64) {
		srv.Add(q(name), promtest.Series{Labels: labels, Value: func(ts time.Time) (float64, bool) {
			i, ok := sweepStepOf(ts)
			if !ok {
				return 0, false
			}
			return f(i), true
		}})
	}
	c := func(x float64) func(int) float64 { return func(int) float64 { return x } }
	add(registration.QueryKvCacheUsage, c(0.01))
	add(registration.QueryMaxRankKvCacheUsage, c(0.01))
	add(registration.QueryQueueLength, c(0))
	add(registration.QueryMaxRankQueueLength, c(0))
	add(registration.QueryCapacityQueueLength, c(0))
	add(registration.QueryMaxRankCapacityQueueLength, c(0))
	add(registration.QueryAvgOutputTokens, c(1))
	add(registration.QueryAvgOutputTokensRecent, c(1))
	add(registration.QueryAvgInputTokens, pdPrompt)
	add(registration.QueryAvgInputTokensRecent, pdPrompt)
	add(registration.QueryPrefixCacheHitRate, c(0))
	add(registration.QueryRunningRequests, c(0))
	add(registration.QueryRequestRate, c(pdRate))
	add(registration.QueryPrefillComputedTokenRate, func(i int) float64 { return pdRate * pdPrompt(i) })
	add(registration.QueryAvgServiceTime, pdService)
	add(registration.QueryAvgTTFT, func(i int) float64 { return pdService(i) + 0.002 })
	add(registration.QueryKvUsageInstant, c(0.01))
	add(registration.QueryKvUsageMean, c(0.01))
	add(registration.QueryPreemptionRate, c(0))
	return srv
}

// pdWindow is the --prometheus flags of the P/D window.
func pdWindow(prom string) []string {
	window := sweepWindow(prom)
	return slices.Insert(window, slices.Index(window, "--from"), "--role-of", "prefill=app=m-pre")
}

// The TTFT answer from --prometheus is the one capture, generate and
// --recording give, byte for byte, in text and in JSON, as every other flag's
// is. The window must answer: a refusal on both paths would hold the identity
// and say nothing.
func TestPrometheus_TheTTFTAnswerIsTheFilesAnswer(t *testing.T) {
	srv := pdServer(t)
	window := pdWindow(srv.URL)
	for _, c := range []struct {
		name string
		args []string
		code int
	}{
		{"replicas-to-slo", []string{"replicas-to-slo", "--assume-vp", "100000"}, 0},
		{"replicas-to-slo at a rate", []string{"replicas-to-slo", "--assume-vp", "100000", "--rate", "2", "--ttft-percentile", "95"}, 0},
		{"replicas-to-slo, more pods", []string{"replicas-to-slo", "--assume-vp", "100000", "--prefill", "3"}, 0},
		{"slo-to-rate", []string{"slo-to-rate", "--assume-vp", "100000", "--tpot", "15ms", "--ttft", "2s@p90"}, 0},
		{"slo-to-rate, TTFT alone", []string{"slo-to-rate", "--assume-vp", "100000", "--ttft", "2s"}, 0},
		{"refused: no V_P", []string{"replicas-to-slo"}, 0},
		{"refused: saturated", []string{"replicas-to-slo", "--assume-vp", "100", "--rate", "5"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, output := range []string{outputJSON, outputText} {
				q := slices.Concat(c.args, []string{"-o", output})
				files, fileCode := filesAnswer(t, window, q)
				prom, promCode := runCmd(t, slices.Concat(q[:1], window, q[1:])...)
				require.Equal(t, fileCode, promCode, "exit codes differ\nfiles:\n%s\nprometheus:\n%s", files, prom)
				if output == outputText {
					assert.Equal(t, files, withoutSourceLine(t, prom), "the text differs")
					continue
				}
				if d := sameAnswer(t, files, prom); d != "" {
					t.Errorf("the --prometheus answer is not the files' answer:\n%s", d)
				}
			}
		})
	}

	// The window answers TTFT, from a learned a.
	out, code := runCmd(t, slices.Concat([]string{"slo-to-rate"}, window, []string{"--assume-vp", "100000", "--ttft", "2s@p90", "-o", "json"})...)
	require.Equal(t, 0, code, out)
	w := decodeOutput(t, out)
	i := slices.IndexFunc(w.Roles, func(r wireRole) bool { return r.Role == rolePrefill })
	require.GreaterOrEqual(t, i, 0, out)
	require.NotNil(t, w.Roles[i].Results.Rate.Answer, "no answer to compare:\n%s", out)
	assert.Equal(t, "learned", w.Roles[i].TTFT.FixedLatencySource)
	near(t, "a", w.Roles[i].TTFT.FixedLatencySeconds, pdFixed+0.002, 0.001)
	assert.Positive(t, w.Roles[i].TTFT.PromptTokens)
}

// A fleet refused before pricing prints no input it did not read.
func TestTTFT_ARefusedFleetPrintsNoFigure(t *testing.T) {
	st := learnedState()
	st.Prefill = nil
	pf := fleetOf(t, st, answerFlags{})
	require.NotNil(t, pf.refused)
	var b strings.Builder
	pf.writeInputs(&b, "", "")
	assert.Empty(t, b.String())
	assert.Zero(t, pf.in.VP)
}

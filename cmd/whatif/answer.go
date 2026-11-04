package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/learnreplay"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// The output formats -o takes.
const (
	outputText = "text"
	outputJSON = "json"
)

// answerFlags are the flags both questions take: where to read the fleet,
// and what to assume about it.
type answerFlags struct {
	recording, cycle, model, namespace, variant, output string
	decode                                              int
	batchCap                                            int64
	inputTokens, outputTokens                           float64
	headroom, tpotRatio                                 float64
	seed                                                uint64
	// ttft, prefill and assumeVP are the TTFT half's flags, refused until
	// the TTFT model lands (the planner's P3).
	ttft, ttftPercentile, assumeVP string
	prefill                        int
	set                            map[string]bool
}

func (f *answerFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.recording, "recording", "", "a learner recording (.jsonl or .jsonl.gz) to answer from (required)")
	fs.StringVar(&f.cycle, "cycle", "last", "the model's cycle to answer at: a number from 1, or last")
	fs.StringVar(&f.model, "model", "", "the model to answer for; empty takes the recording's only model")
	fs.StringVar(&f.namespace, "namespace", "", "the model's namespace; empty matches any")
	fs.StringVar(&f.variant, "variant", "", "price this decode variant only; empty prices every decode variant")
	fs.IntVar(&f.decode, "decode", 0, "decode replicas to price; 0 takes the recorded count")
	fs.Int64Var(&f.batchCap, "batch-cap", 0, "batch cap per engine rank; 0 takes the one the analyzer priced at")
	fs.Float64Var(&f.inputTokens, "assume-input-tokens", 0, "mean prompt length to price at, in place of the recorded one (flagged)")
	fs.Float64Var(&f.outputTokens, "assume-output-tokens", 0, "mean output length to price at, in place of the recorded one (flagged)")
	fs.Float64Var(&f.headroom, "headroom", 0, "the operating point as a fraction of the knee; default the policy's scaleUpThreshold")
	fs.Float64Var(&f.tpotRatio, "assume-tpot-ratio", 0, "R_q, a TPOT percentile over its mean, assumed rather than learned (flagged)")
	fs.Uint64Var(&f.seed, "seed", whatif.DefaultResampling().Seed, "the seed of the bands' bootstrap")
	fs.StringVar(&f.output, "o", outputText, "output: text or json")
	fs.StringVar(&f.ttft, "ttft", "", "TTFT target: not modelled yet")
	fs.StringVar(&f.ttftPercentile, "ttft-percentile", "", "TTFT percentile: not modelled yet")
	fs.StringVar(&f.assumeVP, "assume-vp", "", "prefill token throughput: not modelled yet")
	fs.IntVar(&f.prefill, "prefill", 0, "prefill replicas: not modelled yet")
}

// check validates the flags both questions share once they are parsed.
func (f *answerFlags) check(fs *flag.FlagSet) error {
	f.set = map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { f.set[fl.Name] = true })
	for _, name := range []string{"ttft", "ttft-percentile", "assume-vp", "prefill"} {
		if f.set[name] {
			return usagef("--%s: TTFT and prefill are not modelled yet (the planner's P3); this build answers decode only", name)
		}
	}
	switch {
	case f.recording == "":
		return usagef("--recording is required")
	case f.output != outputText && f.output != outputJSON:
		return usagef("-o %q, want text or json", f.output)
	case f.decode < 0:
		return usagef("--decode %d, want 0 (the recorded count) or more", f.decode)
	case f.batchCap < 0:
		return usagef("--batch-cap %d, want 0 (the priced cap) or more", f.batchCap)
	case f.set["assume-input-tokens"] && !(f.inputTokens >= 0 && finite(f.inputTokens)):
		return usagef("--assume-input-tokens %g, want a finite length of at least 0", f.inputTokens)
	case f.set["assume-output-tokens"] && !(f.outputTokens > 0 && finite(f.outputTokens)):
		return usagef("--assume-output-tokens %g, want a finite length above 0", f.outputTokens)
	}
	if _, err := f.cycleNumber(); err != nil {
		return err
	}
	return nil
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// cycleNumber is --cycle as a StateQuery's Cycle: 0 for last.
func (f *answerFlags) cycleNumber() (int, error) {
	if f.cycle == "last" {
		return 0, nil
	}
	n, err := strconv.Atoi(f.cycle)
	if err != nil || n < 1 {
		return 0, usagef("--cycle %q, want a cycle number from 1, or last", f.cycle)
	}
	return n, nil
}

// readState replays the recording to the cycle asked for. Every failure is
// an input error: the file, or what the flags ask of it.
func (f *answerFlags) readState(ctx context.Context) (*learnreplay.State, error) {
	data, err := os.ReadFile(f.recording)
	if err != nil {
		return nil, usageError{err}
	}
	if data, err = gunzipped(data); err != nil {
		return nil, usagef("%s: %v", f.recording, err)
	}
	cycle, _ := f.cycleNumber()
	st, err := learnreplay.StateAt(ctx, data, learnreplay.Options{},
		learnreplay.StateQuery{Namespace: f.namespace, ModelID: f.model, Cycle: cycle})
	if err != nil {
		return nil, usagef("%s: %v", f.recording, err)
	}
	return st, nil
}

// pricedVariant is one decode variant as a question prices it: the state
// read from the recording, the Decode built from it, and the flags every
// answer on it carries.
type pricedVariant struct {
	state    learnreplay.VariantState
	decode   whatif.Decode
	shape    shape.Shape
	assumed  bool
	flags    []whatif.Flag
	readings []itl.Observation
	refit    whatif.Refit
	lines    whatif.LineResamples
}

// decodeVariants is the variants a question prices: every variant that is not
// prefill (decode, and aggregated, whose line prices decode too), or the one
// --variant names.
func (f *answerFlags) decodeVariants(st *learnreplay.State) ([]pricedVariant, error) {
	lo, hi := st.Policy.ITLBand()
	band := whatif.FillBand{Lo: lo, Hi: hi}
	var out []pricedVariant
	for _, vs := range st.Variants {
		if domain.CanonicalRole(vs.Role) == domain.RolePrefill {
			continue
		}
		if f.variant != "" && vs.Variant != f.variant {
			continue
		}
		pv := pricedVariant{state: vs}
		il, ol := vs.InputTokens, vs.OutputTokens
		if f.set["assume-input-tokens"] {
			il, pv.assumed = f.inputTokens, true
		}
		if f.set["assume-output-tokens"] {
			ol, pv.assumed = f.outputTokens, true
		}
		pv.shape = shape.New(il, ol, vs.PrefixHitRate)
		replicas := vs.Replicas
		if f.decode > 0 {
			replicas = f.decode
		}
		perRank := vs.BatchCap
		if f.batchCap > 0 {
			perRank = f.batchCap
		}
		pv.decode = whatif.Decode{
			Line: vs.Line, Band: band, CapacityTokens: vs.CapacityTokens,
			KVPerRequest: pv.shape.KVreq, OutputTokens: pv.shape.AvgOutputTokens,
			ReplicaCap: perRank * int64(max(vs.Ranks, 1)), Replicas: replicas, Tier: vs.Tier,
		}
		if pv.assumed {
			pv.flags = append(pv.flags, whatif.AssumedShape)
		}
		switch vs.Tier {
		case whatif.TierBorrowed:
			pv.flags = append(pv.flags, whatif.BorrowedLine)
		case whatif.TierHeld:
			pv.flags = append(pv.flags, whatif.HeldLine)
		}
		pv.readings, pv.refit = bandReadings(vs)
		pv.lines = whatif.BootstrapLines(pv.readings, pv.refit, f.resampling())
		out = append(out, pv)
	}
	if f.variant != "" && len(out) == 0 {
		return nil, usagef("--variant %q: no decode variant of that name at this cycle", f.variant)
	}
	return out, nil
}

// resampling is the bands' bootstrap: the page's, from --seed.
func (f *answerFlags) resampling() whatif.Resampling {
	r := whatif.DefaultResampling()
	r.Seed = f.seed
	return r
}

// bandReadings is the readings a variant's line is refitted from, and how:
// an ols line from the steady readings its full fit took, a pinned-B line
// from the steady ones while there are enough, as the tracker fits it, and
// otherwise every reading. A held or borrowed line has no fit to rerun.
func bandReadings(vs learnreplay.VariantState) ([]itl.Observation, whatif.Refit) {
	fit := whatif.Refit{Tier: vs.Tier, PinnedB: vs.Line.B}
	switch vs.Tier {
	case whatif.TierOLS:
		return vs.SteadyReadings, fit
	case whatif.TierPinnedB:
		if len(vs.SteadyReadings) >= itl.DefaultMinSamples {
			return vs.SteadyReadings, fit
		}
		return vs.Readings, fit
	}
	return nil, fit
}

// observedFillMax is the highest fill among the line's readings, ok false
// with none.
func (pv pricedVariant) observedFillMax() (float64, bool) {
	if len(pv.readings) == 0 {
		return 0, false
	}
	m := pv.readings[0].K
	for _, o := range pv.readings[1:] {
		m = max(m, o.K)
	}
	return m, true
}

// qualify adds the variant's own flags to an answer, and ExtrapolatedFill
// when its fill lies above every reading.
func (pv pricedVariant) qualify(r whatif.Result) whatif.Result {
	a, ok := r.(whatif.Answer)
	if !ok {
		return r
	}
	flags := slices.Clone(a.Flags)
	for _, fl := range pv.flags {
		if !slices.Contains(flags, fl) {
			flags = append(flags, fl)
		}
	}
	if top, ok := pv.observedFillMax(); ok && a.Basis.Fill > top && !slices.Contains(flags, whatif.ExtrapolatedFill) {
		flags = append(flags, whatif.ExtrapolatedFill)
	}
	a.Flags = flags
	return a
}

// band bands an answer by rerunning value on every resampled line.
func (pv pricedVariant) band(r whatif.Result, value func(whatif.Decode) whatif.Result) whatif.Result {
	a, ok := r.(whatif.Answer)
	if !ok {
		return r
	}
	return whatif.BandAnswer(a, pv.lines, func(line itl.Model) (float64, bool) {
		d := pv.decode
		d.Line = line
		got, ok := value(d).(whatif.Answer)
		return got.Value, ok
	})
}

// headroomFor is the operating point's fraction of the knee: --headroom, else
// the policy's scaleUpThreshold, else the default.
func (f *answerFlags) headroomFor(p *config.ScalingPolicy) float64 {
	switch {
	case f.set["headroom"]:
		return f.headroom
	case p != nil && p.ScaleUpThreshold > 0:
		return p.ScaleUpThreshold
	}
	return config.DefaultScaleUpThreshold
}

// tpotRatioFor resolves R_q for a percentile q: --assume-tpot-ratio, else
// learned from the recording's TPOT windows. A refusal comes back as the
// Result the percentile answers with.
func (f *answerFlags) tpotRatioFor(st *learnreplay.State, q float64) (whatif.TPOTRatio, []whatif.Flag, *whatif.Unanswerable) {
	if f.set["assume-tpot-ratio"] {
		return whatif.TPOTRatio{Value: f.tpotRatio, Assumed: true}, nil, nil
	}
	if len(st.TPOTWindows) == 0 {
		return whatif.TPOTRatio{}, nil, &whatif.Unanswerable{Reason: whatif.NoTPOTHistogram,
			Detail: "the recording carries no TPOT histogram; pass --assume-tpot-ratio for a flagged answer"}
	}
	return whatif.TPOTRatioFromWindows(st.TPOTWindows, q)
}

// placeholderRatio prices a percentile whose R_q was refused, so that the
// library still judges the rest of the question (withRatioRefusal).
var placeholderRatio = whatif.TPOTRatio{Value: 1}

// withRatioRefusal is r, priced with placeholderRatio, once R_q was refused:
// the refusal, unless r is InvalidInput, since a question asked wrongly
// outranks what the data says (whatif.ExitCode).
func withRatioRefusal(r whatif.Result, refused *whatif.Unanswerable) whatif.Result {
	if refused == nil {
		return r
	}
	if u, ok := r.(whatif.Unanswerable); ok && u.Reason == whatif.InvalidInput {
		return r
	}
	return *refused
}

// ratioNote says where R_q came from, for the human output.
func (f *answerFlags) ratioNote(st *learnreplay.State, r whatif.TPOTRatio) string {
	switch {
	case r.Assumed && len(st.TPOTWindows) == 0:
		return fmt.Sprintf("R_q %s ASSUMED (no TPOT histogram)", num(r.Value))
	case r.Assumed:
		return fmt.Sprintf("R_q %s ASSUMED (the recording's %d TPOT windows not used)", num(r.Value), len(st.TPOTWindows))
	}
	return fmt.Sprintf("R_q %s from %d TPOT windows", num(r.Value), len(st.TPOTWindows))
}

// num prints a ratio or fill with no trailing zeros.
func num(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }

// parsePercent reads a percentile given in percent ("90", "99.9") as a
// fraction; the library refuses one outside (0, 1).
func parsePercent(s string) (float64, error) {
	p, err := strconv.ParseFloat(strings.TrimPrefix(s, "p"), 64)
	if err != nil {
		return 0, err
	}
	return p / 100, nil
}

// footer is the line every human answer ends with.
func footer(st *learnreplay.State) string {
	return fmt.Sprintf("estimate from cycles 1-%d, not a guarantee; see basis (-o json)", st.Cycle)
}

// gunzipped is data, unzipped when it is gzip.
func gunzipped(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
		return data, nil
	}
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

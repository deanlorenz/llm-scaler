package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
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
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learnrec"
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
	fs.StringVar(&f.ttft, "ttft", "", "TTFT target: not supported in this build")
	fs.StringVar(&f.ttftPercentile, "ttft-percentile", "", "TTFT percentile: not supported in this build")
	fs.StringVar(&f.assumeVP, "assume-vp", "", "prefill token throughput: not supported in this build")
	fs.IntVar(&f.prefill, "prefill", 0, "prefill replicas: not supported in this build")
}

// check validates the flags both questions share once they are parsed.
func (f *answerFlags) check(fs *flag.FlagSet) error {
	f.set = map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { f.set[fl.Name] = true })
	for _, name := range []string{"ttft", "ttft-percentile", "assume-vp", "prefill"} {
		if f.set[name] {
			return usagef("--%s: TTFT and prefill are not supported in this build", name)
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

// readState replays the recording to the cycle asked for. A file that cannot
// be read or is no recording, and a model or a cycle the recording does not
// hold, are input errors; a recording that fails inside the analyzers, or a
// replay cancelled, is an internal one.
func (f *answerFlags) readState(ctx context.Context) (*learnreplay.State, error) {
	data, err := os.ReadFile(f.recording)
	if err != nil {
		return nil, usageError{err}
	}
	if data, err = gunzipped(data); err != nil {
		return nil, usagef("%s: %v", f.recording, err)
	}
	rec, err := learnrec.Read(data)
	if err != nil {
		return nil, usagef("%s: not a learner recording: %v", f.recording, err)
	}
	if len(rec.Events) == 0 || rec.Events[0].Kind != learnrec.KindStart {
		return nil, usagef("%s: not a learner recording: no start line", f.recording)
	}
	cycle, _ := f.cycleNumber()
	st, err := learnreplay.StateAt(ctx, data, learnreplay.Options{},
		learnreplay.StateQuery{Namespace: f.namespace, ModelID: f.model, Cycle: cycle})
	var q *learnreplay.QueryError
	switch {
	case errors.As(err, &q):
		return nil, usagef("%s: %v", f.recording, err)
	case err != nil:
		return nil, fmt.Errorf("%s: %w", f.recording, err)
	}
	return st, nil
}

// pricedVariant is one decode variant as a question prices it: the state
// read from the recording, the Decode built from it, and the flags every
// answer on it carries.
type pricedVariant struct {
	state  learnreplay.VariantState
	cycle  int
	decode whatif.Decode
	shape  shape.Shape
	// assumedIL and assumedOL say which lengths the flags assumed.
	assumedIL, assumedOL bool
	flags                []whatif.Flag
	lines                whatif.LineResamples
}

// assumed reports a shape the flags assumed, in part or whole.
func (pv pricedVariant) assumed() bool { return pv.assumedIL || pv.assumedOL }

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
		pv := pricedVariant{state: vs, cycle: st.Cycle}
		il, ol := vs.Shape.InputTokens, vs.Shape.OutputTokens
		if f.set["assume-input-tokens"] {
			il, pv.assumedIL = f.inputTokens, true
		}
		if f.set["assume-output-tokens"] {
			ol, pv.assumedOL = f.outputTokens, true
		}
		pv.shape = shape.New(il, ol, vs.Shape.PrefixHitRate)
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
		if pv.assumed() {
			pv.flags = append(pv.flags, whatif.AssumedShape)
		}
		switch vs.Tier {
		case whatif.TierBorrowed:
			pv.flags = append(pv.flags, whatif.BorrowedLine)
		case whatif.TierHeld:
			pv.flags = append(pv.flags, whatif.HeldLine)
		}
		if vs.Refitted {
			pv.lines = whatif.BootstrapLines(vs.Readings, whatif.Refit{Tier: vs.Tier, Line: vs.Line,
				Baseline: vs.Baseline, MinSettled: vs.MinSettled}, f.resampling())
		}
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

// observedFillMax is the highest fill among the readings the line was
// fitted from (VariantState.LineReadings), ok false with none.
func (pv pricedVariant) observedFillMax() (float64, bool) {
	rs := pv.state.LineReadings
	if len(rs) == 0 {
		return 0, false
	}
	m := rs[0].K
	for _, o := range rs[1:] {
		m = max(m, o.K)
	}
	return m, true
}

// qualify adds the variant's own flags to an answer, and ExtrapolatedFill
// when its fill lies above every reading; a NoLine refusal on a variant that
// learned no line says so by the cycle.
func (pv pricedVariant) qualify(r whatif.Result) whatif.Result {
	a, ok := r.(whatif.Answer)
	if !ok {
		if u, ok := r.(whatif.Unanswerable); ok && u.Reason == whatif.NoLine && pv.state.Line.IsZero() {
			u.Detail = fmt.Sprintf("no ITL line learned by cycle %d", pv.cycle)
			return u
		}
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
// fraction. Zero is no percentile at all, and an error; the library refuses
// any other outside (0, 1).
func parsePercent(s string) (float64, error) {
	p, err := strconv.ParseFloat(strings.TrimPrefix(s, "p"), 64)
	if err != nil {
		return 0, err
	}
	if p == 0 {
		return 0, errors.New("a percentile of 0 asks for no percentile; give one above 0, as p90")
	}
	return p / 100, nil
}

// footer is the line every human answer ends with: the cycles and the
// readings the answer rests on.
func footer(st *learnreplay.State, variants []pricedVariant) string {
	n := 0
	for _, pv := range variants {
		n += len(pv.state.LineReadings)
	}
	return fmt.Sprintf("estimate from cycles 1-%d and %d readings, not a guarantee; see basis (-o json)", st.Cycle, n)
}

// shapeText is the shape a variant was priced at and where each length came
// from: the flags, or the cycles it was measured over.
func (pv pricedVariant) shapeText() string {
	m := pv.state.Shape
	measured := fmt.Sprintf("measured, cycles %d-%d", m.FromCycle, m.ToCycle)
	if m.Cycles == 0 {
		measured = fmt.Sprintf("no traffic by cycle %d", pv.cycle)
	}
	il, ol := num(round(pv.shape.AvgInputTokens)), num(round(pv.shape.AvgOutputTokens))
	switch {
	case pv.assumedIL && pv.assumedOL:
		return fmt.Sprintf("IL %s OL %s (assumed)", il, ol)
	case pv.assumedIL:
		return fmt.Sprintf("IL %s (assumed) OL %s (%s)", il, ol, measured)
	case pv.assumedOL:
		return fmt.Sprintf("IL %s (%s) OL %s (assumed)", il, measured, ol)
	}
	return fmt.Sprintf("IL %s OL %s (%s)", il, ol, measured)
}

// round is x to the nearest token.
func round(x float64) float64 { return math.Round(x) }

// newAnswerFlags is newFlags for an answer command: when args ask for JSON,
// and not for help, a flag error is not printed to out, so that the output
// stays one JSON object (asJSONError writes the error).
func newAnswerFlags(name string, args []string, out io.Writer) *flag.FlagSet {
	fs := newFlags(name, out)
	if wantsJSON(args) && !slices.ContainsFunc(args, func(a string) bool {
		return a == "-h" || a == "-help" || a == "--help" || a == "--h"
	}) {
		fs.SetOutput(io.Discard)
	}
	return fs
}

// wantsJSON reports whether args ask for -o json, read before the flags are
// parsed, so that a usage error is written as JSON too.
func wantsJSON(args []string) bool {
	for i, a := range args {
		switch a {
		case "-o", "--o":
			if i+1 < len(args) && args[i+1] == outputJSON {
				return true
			}
		case "-o=" + outputJSON, "--o=" + outputJSON:
			return true
		}
	}
	return false
}

// asJSONError writes err, a command's error before any answer was written,
// as a JSON error object when args asked for JSON, and ends with its exit
// code. Any other error, and an exit status (the output already written),
// passes through.
func asJSONError(question string, args []string, out io.Writer, err error) error {
	var s exitStatus
	if err == nil || errors.As(err, &s) || !wantsJSON(args) {
		return err
	}
	code := exitCode(err)
	w := wireOutput{SchemaVersion: schemaVersion, Question: question, ExitCode: code, Roles: []wireRole{},
		Error: &wireError{Message: err.Error()}}
	if werr := writeJSON(out, w); werr != nil {
		return err
	}
	return exitStatus(code)
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

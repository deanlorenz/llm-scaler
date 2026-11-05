package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/learnreplay"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// rateAnswer is question 1's answer for one decode variant.
type rateAnswer struct {
	pv     pricedVariant
	result whatif.Result
}

// runSLOToRate answers question 1 (docs/proposals/slo-capacity-planner.md,
// "Question 1: from an SLO to a rate") for decode: the largest rate the
// replicas carry at the TPOT target, capped at the operating point.
func runSLOToRate(ctx context.Context, args []string, out, warn io.Writer) error {
	return asJSONError("slo-to-rate", args, out, sloToRate(ctx, args, out, warn))
}

func sloToRate(ctx context.Context, args []string, out, warn io.Writer) error {
	fs := newAnswerFlags("slo-to-rate", args, out)
	f := answerFlags{question: "slo-to-rate", warn: warn}
	f.register(fs)
	tpot := fs.String("tpot", "", "the TPOT target: a mean (4ms) or a per-request percentile (4ms@p90) (required)")
	if ok, err := parse(fs, args); !ok {
		return err
	}
	if err := f.check(fs); err != nil {
		return err
	}
	target, err := parseTPOT(*tpot)
	if err != nil {
		return err
	}
	if f.set["assume-tpot-ratio"] && target.Percentile == 0 {
		return usagef("--assume-tpot-ratio needs a percentile TPOT target (--tpot 4ms@p90)")
	}
	st, err := f.readState(ctx)
	if err != nil {
		return err
	}
	variants, err := f.decodeVariants(st)
	if err != nil {
		return err
	}

	var ratioFlags []whatif.Flag
	var ratioRefused *whatif.Unanswerable
	if q := target.Percentile; q != 0 {
		if q > 0 && q < 1 {
			target.Ratio, ratioFlags, ratioRefused = f.tpotRatioFor(st, q)
		} else {
			target.Ratio = whatif.TPOTRatio{Value: f.tpotRatio, Assumed: true}
		}
	}
	query := whatif.RateQuery{Target: target, Headroom: f.headroomFor(st.Policy)}
	if ratioRefused != nil {
		// Priced with a placeholder R_q, so that the library still judges
		// the rest of the question; the answer is replaced by the refusal.
		query.Target.Ratio = placeholderRatio
	}

	answers := make([]rateAnswer, 0, len(variants))
	results := make([]whatif.Result, 0, len(variants)+1)
	for _, pv := range variants {
		res := withRatioRefusal(whatif.RateForTPOT(pv.decode, query), ratioRefused)
		res = pv.band(res, func(d whatif.Decode) whatif.Result { return whatif.RateForTPOT(d, query) })
		if a, ok := res.(whatif.Answer); ok {
			a.Flags = append(a.Flags, ratioFlags...)
			res = a
		}
		res = pv.qualify(res)
		answers = append(answers, rateAnswer{pv: pv, result: res})
		results = append(results, res)
	}
	none := noDecode(st, variants)
	if none != nil {
		results = append(results, *none)
	}
	code := whatif.ExitCode(results...)

	if f.output == outputJSON {
		w := newWireOutput("slo-to-rate", &f, st, variants, code)
		for _, a := range answers {
			in := inputsOf(&f, a.pv)
			h := query.Headroom
			in.Headroom = &h
			in.TPOT = tpotInputs(st, target.Seconds, target.Percentile, target.Ratio)
			w.Roles = append(w.Roles, wireRole{Role: "decode", Variant: a.pv.state.Variant, Inputs: in,
				Results: wireResults{Rate: toWire(a.result, unitRate)}})
		}
		if none != nil {
			w.Roles = append(w.Roles, wireRole{Role: "decode", Results: wireResults{Rate: toWire(*none, unitRate)}})
		}
		if err := writeJSON(out, w); err != nil {
			return err
		}
	} else if err := writeRateText(out, st, variants, answers, none); err != nil {
		return err
	}
	if code != whatif.ExitAnswered {
		return exitStatus(code)
	}
	return nil
}

// parseTPOT reads --tpot: a duration, and optionally "@p" and a percentile
// in percent.
func parseTPOT(s string) (whatif.TPOTTarget, error) {
	if s == "" {
		return whatif.TPOTTarget{}, usagef("--tpot is required: a mean (4ms) or a per-request percentile (4ms@p90)")
	}
	value, pct, hasPct := strings.Cut(s, "@")
	d, err := time.ParseDuration(value)
	if err != nil {
		return whatif.TPOTTarget{}, usagef("--tpot %q: %v", s, err)
	}
	t := whatif.TPOTTarget{Seconds: d.Seconds()}
	if hasPct {
		if !strings.HasPrefix(pct, "p") {
			return whatif.TPOTTarget{}, usagef("--tpot %q: a percentile is written @p90", s)
		}
		if t.Percentile, err = parsePercent(pct); err != nil {
			return whatif.TPOTTarget{}, usagef("--tpot %q: %v", s, err)
		}
	}
	return t, nil
}

// writeRateText is question 1's human output, in the page's layout.
func writeRateText(out io.Writer, st *learnreplay.State, variants []pricedVariant, answers []rateAnswer, none *whatif.Unanswerable) error {
	var b strings.Builder
	const layout = "%-9s%-19s%-41s%s\n"
	fmt.Fprintf(&b, layout, "role", "est. rate at SLO", "bound by", "basis")
	for _, a := range answers {
		role := "decode"
		if len(answers) > 1 {
			role += "/" + a.pv.state.Variant
		}
		switch r := a.result.(type) {
		case whatif.Answer:
			by := boundText(r.Basis.BoundBy)
			if len(r.Basis.BoundBy) > 0 && r.Basis.BoundBy[0] == whatif.BoundOperatingPoint && r.Basis.Uncapped > r.Value {
				by += fmt.Sprintf(" (TPOT alone: %.2f)", r.Basis.Uncapped)
			}
			basis := r.Basis.Tier
			if r.Band != nil {
				basis += fmt.Sprintf(" [%.1f .. %.1f]", r.Band.Lo, r.Band.Hi)
			}
			fmt.Fprintf(&b, layout, role, rateText(r.Value), by, strings.TrimSpace(basis))
		case whatif.Unanswerable:
			fmt.Fprintf(&b, "%-9s%s\n", role, refusedText(r))
		}
		fmt.Fprintf(&b, "%-9sshape %s\n", "", a.pv.shapeText())
		flagsLine(&b, a.pv, a.result)
	}
	if none != nil {
		fmt.Fprintf(&b, "%-9s%s\n", "decode", refusedText(*none))
	}
	b.WriteString(footer(st, variants) + "\n")
	_, err := io.WriteString(out, b.String())
	return err
}

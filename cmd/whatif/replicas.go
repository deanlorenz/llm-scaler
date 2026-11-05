package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/learnreplay"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// replicasAnswer is question 2's answer for one decode variant.
type replicasAnswer struct {
	pv  pricedVariant
	slo whatif.DecodeSLO
	// ratio is R_q, when a percentile was asked and resolved.
	ratio whatif.TPOTRatio
}

// runReplicasToSLO answers question 2 (docs/proposals/slo-capacity-planner.md,
// "Question 2: from replicas to an SLO") for decode: the knee the replicas
// saturate at, the operating rate below it, and the TPOT held there.
func runReplicasToSLO(ctx context.Context, args []string, out, warn io.Writer) error {
	return asJSONError("replicas-to-slo", args, out, replicasToSLO(ctx, args, out, warn))
}

func replicasToSLO(ctx context.Context, args []string, out, warn io.Writer) error {
	fs := newAnswerFlags("replicas-to-slo", args, out)
	f := answerFlags{question: "replicas-to-slo", warn: warn}
	f.register(fs)
	rate := fs.Float64("rate", 0, "the operating rate, requests per second over the fleet, in place of headroom x knee")
	pct := fs.String("tpot-percentile", "", "report the TPOT at this per-request percentile too, in percent (90)")
	if ok, err := parse(fs, args); !ok {
		return err
	}
	if err := f.check(fs); err != nil {
		return err
	}
	if f.set["rate"] && !(*rate > 0 && finite(*rate)) {
		return usagef("--rate %g, want a finite rate above 0", *rate)
	}
	if f.set["assume-tpot-ratio"] && *pct == "" {
		return usagef("--assume-tpot-ratio needs --tpot-percentile")
	}
	var q float64
	if *pct != "" {
		var err error
		if q, err = parsePercent(*pct); err != nil {
			return usagef("--tpot-percentile %q: %v", *pct, err)
		}
	}
	st, err := f.readState(ctx)
	if err != nil {
		return err
	}
	variants, err := f.decodeVariants(st)
	if err != nil {
		return err
	}

	query := whatif.ReplicasQuery{}
	if f.set["rate"] {
		query.Rate = *rate
		if f.set["headroom"] {
			query.Headroom = f.headroom // both set: the library refuses it
		}
	} else {
		query.Headroom = f.headroomFor(st.Policy)
	}
	var ratio whatif.TPOTRatio
	var ratioFlags []whatif.Flag
	var ratioRefused *whatif.Unanswerable
	if *pct != "" {
		query.PercentileTarget = &whatif.TPOTTarget{Percentile: q}
		// A percentile outside (0, 1) is the library's to refuse.
		if q > 0 && q < 1 {
			ratio, ratioFlags, ratioRefused = f.tpotRatioFor(st, q)
		} else {
			ratio = whatif.TPOTRatio{Value: f.tpotRatio, Assumed: true}
		}
		query.PercentileTarget.Ratio = ratio
		if ratioRefused != nil {
			query.PercentileTarget.Ratio = placeholderRatio
		}
	}

	answers := make([]replicasAnswer, 0, len(variants))
	var results []whatif.Result
	for _, pv := range variants {
		slo := whatif.SLOAtReplicas(pv.decode, query)
		if slo.PercentileTPOT != nil {
			slo.PercentileTPOT = withRatioRefusal(slo.PercentileTPOT, ratioRefused)
		}
		on := func(pick func(whatif.DecodeSLO) whatif.Result) func(whatif.Decode) whatif.Result {
			return func(d whatif.Decode) whatif.Result { return pick(whatif.SLOAtReplicas(d, query)) }
		}
		slo.KneeRate = pv.qualify(pv.band(slo.KneeRate, whatif.KneeRate))
		// A rate --rate gives is an input, not an estimate: it has no band.
		if !f.set["rate"] {
			slo.Rate = pv.band(slo.Rate, on(func(s whatif.DecodeSLO) whatif.Result { return s.Rate }))
		}
		slo.Rate = pv.qualify(slo.Rate)
		slo.MeanTPOT = pv.qualify(pv.band(slo.MeanTPOT, on(func(s whatif.DecodeSLO) whatif.Result { return s.MeanTPOT })))
		if slo.PercentileTPOT != nil {
			p := pv.band(slo.PercentileTPOT, on(func(s whatif.DecodeSLO) whatif.Result { return s.PercentileTPOT }))
			if a, ok := p.(whatif.Answer); ok {
				a.Flags = append(a.Flags, ratioFlags...)
				p = a
			}
			slo.PercentileTPOT = pv.qualify(p)
		}
		answers = append(answers, replicasAnswer{pv: pv, slo: slo, ratio: ratio})
		results = append(results, slo.KneeRate, slo.Rate, slo.MeanTPOT, slo.PercentileTPOT)
	}
	none := noDecode(st, variants)
	if none != nil {
		results = append(results, *none)
	}
	code := whatif.ExitCode(results...)

	if f.output == outputJSON {
		w := newWireOutput("replicas-to-slo", &f, st, variants, code)
		for _, a := range answers {
			in := inputsOf(&f, a.pv)
			if f.set["rate"] {
				in.Rate = rate
			} else {
				h := query.Headroom
				in.Headroom = &h
			}
			if query.PercentileTarget != nil {
				in.TPOT = tpotInputs(st, 0, q, a.ratio)
			}
			w.Roles = append(w.Roles, wireRole{Role: "decode", Variant: a.pv.state.Variant, Inputs: in, Results: wireResults{
				Knee: toWire(a.slo.KneeRate, unitRate), OperatingRate: toWire(a.slo.Rate, unitRate),
				MeanTPOT: toWire(a.slo.MeanTPOT, unitSeconds), PercentileTPOT: toWire(a.slo.PercentileTPOT, unitSeconds),
			}})
		}
		if none != nil {
			w.Roles = append(w.Roles, wireRole{Role: "decode", Results: wireResults{Knee: toWire(*none, unitRate)}})
		}
		if err := writeJSON(out, w); err != nil {
			return err
		}
	} else if err := writeReplicasText(out, &f, st, variants, answers, none, *pct, query); err != nil {
		return err
	}
	if code != whatif.ExitAnswered {
		return exitStatus(code)
	}
	return nil
}

// noDecode is the refusal for a cycle with no decode variant to price,
// nil when there is one.
func noDecode(st *learnreplay.State, variants []pricedVariant) *whatif.Unanswerable {
	if len(variants) > 0 {
		return nil
	}
	return &whatif.Unanswerable{Reason: whatif.NoDecodeReplicas,
		Detail: fmt.Sprintf("no decode variant priced at cycle %d of %s/%s", st.Cycle, st.Namespace, st.ModelID)}
}

// tpotInputs is the wire's account of the TPOT question.
func tpotInputs(st *learnreplay.State, target, q float64, r whatif.TPOTRatio) *wireTPOT {
	t := &wireTPOT{TargetSeconds: target, Percentile: q, Ratio: r.Value, Windows: len(st.TPOTWindows)}
	switch {
	case r.Value == 0:
	case r.Assumed:
		t.RatioSource = "assumed"
	default:
		t.RatioSource = "learned"
	}
	return t
}

// writeReplicasText is question 2's human output, in the page's layout.
func writeReplicasText(out io.Writer, f *answerFlags, st *learnreplay.State, variants []pricedVariant, answers []replicasAnswer,
	none *whatif.Unanswerable, pct string, query whatif.ReplicasQuery) error {
	var b strings.Builder
	for i, a := range answers {
		if len(answers) > 1 {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "variant %s (%s)\n", a.pv.state.Variant, a.pv.state.Role)
		}
		if u, ok := a.slo.KneeRate.(whatif.Unanswerable); ok {
			fmt.Fprintf(&b, "%-16s%s\n", "decode", refusedText(u))
			fmt.Fprintf(&b, "%-16s%s\n", "shape", a.pv.shapeText())
			continue
		}
		knee := a.slo.KneeRate.(whatif.Answer)
		row(&b, "knee", rateText(knee.Value), "", fmt.Sprintf("decode, %s binds at fill %.3f", boundText(knee.Basis.BoundBy), knee.Basis.Fill))
		switch r := a.slo.Rate.(type) {
		case whatif.Answer:
			why := "--rate"
			if query.Rate == 0 {
				src := "scaleUpThreshold"
				if f.set["headroom"] {
					src = "--headroom"
				}
				why = fmt.Sprintf("%s x knee (%s)", num(query.Headroom), src)
			}
			row(&b, "operating rate", rateText(r.Value), "", why)
		case whatif.Unanswerable:
			fmt.Fprintf(&b, "%-16s%s\n", "operating rate", refusedText(r))
		}
		tpotRow(&b, "mean TPOT", a.slo.MeanTPOT, lineText(a.pv))
		if a.slo.PercentileTPOT != nil {
			tpotRow(&b, "TPOT p"+strings.TrimPrefix(pct, "p"), a.slo.PercentileTPOT, "per request; "+f.ratioNote(st, a.ratio))
		}
		fmt.Fprintf(&b, "%-16s%s\n", "shape", a.pv.shapeText())
		flagsLine(&b, a.pv, a.slo.KneeRate, a.slo.Rate, a.slo.MeanTPOT, a.slo.PercentileTPOT)
	}
	if none != nil {
		fmt.Fprintf(&b, "%-16s%s\n", "decode", refusedText(*none))
	}
	b.WriteString(footer(st, variants) + "\n")
	_, err := io.WriteString(out, b.String())
	return err
}

// tpotRow is one TPOT row: the value in ms, its band, and its basis.
func tpotRow(b *strings.Builder, label string, r whatif.Result, basis string) {
	switch v := r.(type) {
	case whatif.Answer:
		band := ""
		if v.Band != nil {
			band = fmt.Sprintf("[%.2f .. %.2f]", 1000*v.Band.Lo, 1000*v.Band.Hi)
		}
		row(b, label, fmt.Sprintf("%.2f ms", 1000*v.Value), band, basis)
	case whatif.Unanswerable:
		fmt.Fprintf(b, "%-16s%s\n", label, refusedText(v))
	}
}

// row is one line of question 2's output: the label, the value, and, for a
// banded figure, the band, then the basis.
func row(b *strings.Builder, label, value, band, basis string) {
	if band == "" && !strings.HasPrefix(label, "mean") && !strings.HasPrefix(label, "TPOT") {
		fmt.Fprintf(b, "%-16s%-12s%s\n", label, value, basis)
		return
	}
	fmt.Fprintf(b, "%-16s%-12s%-17s%s\n", label, value, band, basis)
}

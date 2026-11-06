package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// The human output's pieces, shared by both questions.

// refusedText is a refusal as the output prints it: the reason, then what
// caused it.
func refusedText(u whatif.Unanswerable) string {
	if u.Detail == "" {
		return "refused: " + string(u.Reason)
	}
	return fmt.Sprintf("refused: %s (%s)", u.Reason, u.Detail)
}

// rateText is a rate in requests per second.
func rateText(r float64) string { return fmt.Sprintf("%.2f r/s", r) }

// boundText names what set a rate or a knee, in order.
func boundText(by []whatif.Bound) string {
	parts := make([]string, len(by))
	for i, b := range by {
		parts[i] = string(b)
	}
	return strings.Join(parts, ", ")
}

// lineText is the line a variant was priced on, and the fills it was read
// at.
func lineText(pv pricedVariant) string {
	tier := pv.state.Tier
	if tier == "" {
		tier = "unknown tier"
	}
	s := "line " + tier
	if pv.state.BorrowedFrom != "" {
		s += " from " + pv.state.BorrowedFrom
	}
	if top, ok := pv.observedFillMax(); ok {
		s += fmt.Sprintf(", observed fill up to %.2f", top)
	}
	return s
}

// flagsLine prints the flags the variant's answers carry, once each, in the
// order they first appear, with what each rests on where there is more to
// say. Nothing is printed when there are none.
func flagsLine(b *strings.Builder, pv pricedVariant, results ...whatif.Result) {
	var flags []whatif.Flag
	fill := -1.0
	for _, r := range results {
		var from []whatif.Flag
		switch a := r.(type) {
		case whatif.Answer:
			from = a.Flags
			if a.HasFlag(whatif.ExtrapolatedFill) {
				fill = max(fill, a.Basis.Fill)
			}
		case whatif.TTFT:
			from = a.Flags
		case whatif.Unanswerable:
			from = a.Flags
		}
		for _, f := range from {
			if !slices.Contains(flags, f) {
				flags = append(flags, f)
			}
		}
	}
	if len(flags) == 0 {
		return
	}
	parts := make([]string, 0, len(flags))
	for _, f := range flags {
		switch f {
		case whatif.AssumedShape:
			parts = append(parts, fmt.Sprintf("%s (IL %s, OL %s)", f, num(pv.shape.AvgInputTokens), num(pv.shape.AvgOutputTokens)))
		case whatif.ExtrapolatedFill:
			top, _ := pv.observedFillMax()
			parts = append(parts, fmt.Sprintf("%s (fill %.3f, %.3f above the readings)", f, fill, fill-top))
		case whatif.BorrowedLine:
			parts = append(parts, fmt.Sprintf("%s (from %s)", f, pv.state.BorrowedFrom))
		case whatif.TierUnstable:
			parts = append(parts, fmt.Sprintf("%s (%d/%d resamples changed tier)", f, pv.lines.TierChanges(), pv.lines.Drawn))
		case whatif.LineNearGate:
			parts = append(parts, fmt.Sprintf("%s (%d/%d)", f, pv.lines.TierChanges(), pv.lines.Drawn))
		default:
			parts = append(parts, string(f))
		}
	}
	fmt.Fprintf(b, "flags: %s\n", strings.Join(parts, "; "))
}

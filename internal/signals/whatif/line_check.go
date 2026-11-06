package whatif

import (
	"math"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// The line check (docs/proposals/slo-capacity-planner.md, "Uncertainty"): does
// the line an answer rests on predict the token rate the recording's replicas
// reported? It is the analyzers' GPS check (itl.GPSErrorPct) over a window of
// rows in place of one row, so that one noisy minute decides nothing, with the
// sequences capped as every price caps them (CappedSequences): GPSErrorPct
// prices a cache, and a replica at its batch cap would read as a mismatch.

// LineRow is one replica's reading at one cycle.
type LineRow struct {
	Cycle int
	// K is the fill paired with the rate's minute, and KVPerRequest the KV a
	// request held at that cycle: the row's own shape, not the shape asked.
	K, KVPerRequest float64
	// TokenRate is the replica's generated tokens per second.
	TokenRate float64
	// Ramp says the row is off the steady gate: its fill or its shape moved
	// within the gate's time, or it has not been read for that long. A
	// minute's rate and a minute's fill then span two levels, and say nothing
	// of the line.
	Ramp bool
}

// LineCheck is the line against a window of rows.
type LineCheck struct {
	// Compared is the rows priced, and OnRamp the rows left out as on a ramp.
	Compared, OnRamp int
	// Past is how many of the Compared read more than
	// itl.DefaultGPSMismatchThresholdPct off the line.
	Past int
	// MedianErrorPct is the median of the Compared rows' errors, without
	// their sign, WorstErrorPct the largest, and SignedErrorPct the median
	// with its sign: above 0, the line predicts more tokens than were read,
	// so it over-promises.
	MedianErrorPct, WorstErrorPct, SignedErrorPct float64
	// FromCycle and ToCycle are the first and the last cycle compared, and
	// FillLo and FillHi the fills.
	FromCycle, ToCycle int
	FillLo, FillHi     float64
}

// Mismatch reports a line the rows do not support: the median error past
// itl.DefaultGPSMismatchThresholdPct. A check with no row compared is no
// mismatch, and no pass: Compared says which it is.
func (c LineCheck) Mismatch() bool {
	return c.MedianErrorPct > itl.DefaultGPSMismatchThresholdPct
}

// CheckLine prices each row off a ramp on d's line, capacity and cap, at the
// row's own fill and KV per request, against the rate it reported. A row the
// GPS check would not compare, one below itl.DefaultGPSMinKForVerification or
// with no rate or KV per request, is left out and counted nowhere: it is the
// absence of evidence either way.
func CheckLine(d Decode, rows []LineRow) LineCheck {
	var c LineCheck
	var absolute, signed []float64
	for _, r := range rows {
		if !(r.K >= itl.DefaultGPSMinKForVerification) || !(r.TokenRate > 0) || !Finite(r.TokenRate) || !(r.KVPerRequest > 0) {
			continue
		}
		if r.Ramp {
			c.OnRamp++
			continue
		}
		predicted := ReplicaTokenRate(d.Line, r.K, d.CapacityTokens, r.KVPerRequest, d.ReplicaCap)
		if !(predicted > 0) {
			continue
		}
		e := 100 * (predicted - r.TokenRate) / r.TokenRate
		absolute, signed = append(absolute, math.Abs(e)), append(signed, e)
		if math.Abs(e) > itl.DefaultGPSMismatchThresholdPct {
			c.Past++
		}
		c.WorstErrorPct = max(c.WorstErrorPct, math.Abs(e))
		if c.Compared == 0 {
			c.FromCycle, c.FillLo, c.FillHi = r.Cycle, r.K, r.K
		}
		c.Compared++
		c.ToCycle = r.Cycle
		c.FillLo, c.FillHi = min(c.FillLo, r.K), max(c.FillHi, r.K)
	}
	if c.Compared > 0 {
		c.MedianErrorPct, c.SignedErrorPct = median(absolute), median(signed)
	}
	return c
}

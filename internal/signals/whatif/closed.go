package whatif

import (
	"fmt"
	"math"
	"slices"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// Decode is one decode role as the planner prices it: Replicas identical
// replicas, each running the fitted line over its band, sharing arrivals
// evenly (docs/proposals/slo-capacity-planner.md, "Decode: TPOT from the ITL
// line").
//
// A replica at fill k holds n(k) = min(k*C/KVreq, ReplicaCap) sequences, each
// emitting a token every ITL(k), so its request rate is
// lambda_rep(k) = n(k) / (ITL(k) * OL).
type Decode struct {
	// Line is the variant's fitted ITL line, ITL(k) = A*k + B in seconds.
	Line itl.Model
	// Band is the fill range the line is fitted over; prices are clamped to
	// it. It must meet FillBand's precondition.
	Band FillBand
	// CapacityTokens is C, the replica's whole KV capacity in tokens
	// (CapacityTokens).
	CapacityTokens float64
	// KVPerRequest is KVreq, the KV tokens one in-flight request holds on
	// average (shape.Shape.KVreq).
	KVPerRequest float64
	// OutputTokens is OL, the mean output length a request rate is divided
	// by.
	OutputTokens float64
	// ReplicaCap is the most sequences one replica runs: one engine's batch
	// cap times the data-parallel ranks the replica runs.
	ReplicaCap int64
	// Replicas is N_d, the decode replicas sharing the arrivals.
	Replicas int
}

// check refuses a Decode the planner cannot price, naming the first input
// that fails. The order is the order an operator fixes them in: the question
// first, then the line, the capacity, the cap and the shape.
func (d Decode) check() *Unanswerable {
	var u Unanswerable
	switch {
	// The band is the policy's, so an invalid one is a configuration error;
	// everything below it is read from the cluster, so a bad value is data.
	case !d.Band.valid():
		u = refuse(InvalidInput, fmt.Sprintf("band [%g, %g], want 0 < lo < hi < 1", d.Band.Lo, d.Band.Hi))
	case d.Replicas < 1:
		u = refuse(NoDecodeReplicas, fmt.Sprintf("decode replicas %d", d.Replicas))
	// B must be positive as well as A: with B <= 0 the request rate does not
	// rise with fill, so no fill answers a rate and the line describes no
	// engine (every step has a fixed cost).
	case !itl.ValidModel(d.Line.A, d.Line.B) || !Finite(d.Line.B) || !(d.Line.B > 0):
		u = refuse(NoLine, fmt.Sprintf("line A=%g B=%g", d.Line.A, d.Line.B))
	case !Finite(d.CapacityTokens) || !(d.CapacityTokens > 0):
		u = refuse(NoCapacity, fmt.Sprintf("KV capacity %g tokens", d.CapacityTokens))
	case d.ReplicaCap <= 0:
		u = refuse(NoBatchCap, fmt.Sprintf("replica cap %d", d.ReplicaCap))
	case !Finite(d.OutputTokens) || !(d.OutputTokens > 0) || !Finite(d.KVPerRequest) || !(d.KVPerRequest > 0):
		u = refuse(NoTraffic, fmt.Sprintf("output %g tokens, KV per request %g tokens", d.OutputTokens, d.KVPerRequest))
	// The band's top must hold one whole request: a replica that cannot
	// serves none, and the closed form would price a fraction of one.
	case math.Floor(d.Band.Hi*d.CapacityTokens/d.KVPerRequest) < 1:
		u = refuse(NoCapacity, fmt.Sprintf("KV capacity %g tokens holds no whole request of %g at the band top %g",
			d.CapacityTokens, d.KVPerRequest, d.Band.Hi))
	default:
		return nil
	}
	return &u
}

// fullPoint is where a replica saturates: the sequences it runs there, the
// fill those occupy, and the fill the line is read at.
type fullPoint struct {
	// slots is c, a whole number of sequences: the replica cap, or the
	// sequences the band's top holds, floor(band.Hi*C/KVreq), when that is
	// fewer. Kept as a float: a cap read as MaxInt64 is a number here, and
	// only the admission chain needs it as a count (maxSlots bounds that).
	slots float64
	// slotFill is the fill the slots occupy, c*KVreq/C: never above band.Hi.
	slotFill float64
	// fill is k_full, the fill the line is read at: slotFill, raised to
	// band.Lo when the slots fill less than the band's bottom, where the line
	// was not fitted (the higher, safer ITL, as ReachableFill reads it).
	fill float64
	// by is what set the slots.
	by Bound
	// belowBand is slotFill < band.Lo: the knee is read at the band's bottom.
	belowBand bool
}

// full is the replica's saturation point. The knee and the admission chain
// both saturate at it, so the two never disagree about where a rate stops
// being carried.
func (d Decode) full() fullPoint {
	p := fullPoint{slots: math.Floor(d.Band.Hi * d.CapacityTokens / d.KVPerRequest), by: BoundBandTop}
	if cp := float64(d.ReplicaCap); cp <= p.slots {
		p.slots, p.by = cp, BoundBatchCap
	}
	p.slotFill = p.slots * d.KVPerRequest / d.CapacityTokens
	p.fill = p.slotFill
	if p.slotFill < d.Band.Lo {
		p.fill, p.belowBand = d.Band.Lo, true
	}
	return p
}

// flags is BelowFittedBand when the knee is read at the band's bottom.
func (p fullPoint) flags() []Flag {
	if p.belowBand {
		return []Flag{BelowFittedBand}
	}
	return nil
}

// replicaRate is lambda_rep(k), one replica's request rate at fill k, with
// the sequences capped at the slots.
func (d Decode) replicaRate(k float64) float64 {
	seqs := min(itl.Sequences(k, d.CapacityTokens, d.KVPerRequest), d.full().slots)
	itlSec := d.Line.ITLAt(k)
	if !(seqs > 0) || !(itlSec > 0) {
		return 0
	}
	return seqs / (itlSec * d.OutputTokens)
}

// knee is the fleet's decode knee, N_d * c / (OL * ITL(k_full)).
func (d Decode) knee() float64 {
	p := d.full()
	return float64(d.Replicas) * p.slots / (d.OutputTokens * d.Line.ITLAt(p.fill))
}

// fillAtReplicaRate is the closed-form fill one replica runs at while it
// carries lam requests a second (Little's law, residence OL*ITL(k)):
//
//	k(lam) = lam*OL*B / (C/KVreq - lam*OL*A)
//
// It is valid while the denominator is positive and the sequences are under
// the slots (the fill under slotFill); ok is false past that, where the
// replica is saturated.
func (d Decode) fillAtReplicaRate(lam float64) (k float64, ok bool) {
	den := d.CapacityTokens/d.KVPerRequest - lam*d.OutputTokens*d.Line.A
	if !(den > 0) {
		return 0, false
	}
	k = lam * d.OutputTokens * d.Line.B / den
	if !Finite(k) || k > d.full().slotFill {
		return 0, false
	}
	return k, true
}

// Knee is the rate, in requests per second over the fleet, at which the
// decode replicas saturate: N_d * c / (OL * ITL(k_full)) (question 2, step 1).
// Its Basis names whether the band's top or the batch cap set the slots, and
// BelowFittedBand flags a knee read at the band's bottom because the slots
// fill less than it.
func Knee(d Decode) Result {
	if u := d.check(); u != nil {
		return *u
	}
	p := d.full()
	knee := d.knee()
	return Answer{Value: knee, Flags: p.flags(), Basis: Basis{Fill: p.fill, Knee: knee, BoundBy: []Bound{p.by}}}
}

// FillAtRate is the KV fill each decode replica runs at while the fleet
// carries rate requests a second, shared evenly: the closed form k(lambda_rep)
// with lambda_rep = rate / N_d. Saturated at or above the knee.
func FillAtRate(d Decode, rate float64) Result {
	if u := d.check(); u != nil {
		return *u
	}
	if !Finite(rate) || rate < 0 {
		return refuse(InvalidInput, fmt.Sprintf("rate %g", rate))
	}
	knee := d.knee()
	if rate >= knee {
		return refuse(Saturated, fmt.Sprintf("rate %g r/s at or above the knee %g r/s", rate, knee))
	}
	k, ok := d.fillAtReplicaRate(rate / float64(d.Replicas))
	if !ok {
		return refuse(Saturated, fmt.Sprintf("no fill carries %g r/s", rate))
	}
	return Answer{Value: k, Basis: Basis{Fill: k, Knee: knee}}
}

// TPOTTarget is a TPOT service level: the mean, or a percentile over requests
// of each request's own TPOT, (e2e - TTFT)/(output tokens - 1).
type TPOTTarget struct {
	// Seconds is the target.
	Seconds float64
	// Percentile is q in (0, 1) for a per-request percentile, or 0 for the
	// mean.
	Percentile float64
	// OverTokenGaps asks for a percentile over individual token gaps (ITL)
	// rather than over requests. It is refused: nothing models the spread of
	// single steps (TokenGapPercentile).
	OverTokenGaps bool
	// Ratio is R_q, the q-th percentile of per-request TPOT over its mean.
	// Required for a percentile; ignored for the mean.
	Ratio TPOTRatio
}

// TPOTRatio is R_q: learned from a TPOT histogram (TPOTRatioFromWindows), or
// assumed by the caller. A zero Value is "no ratio".
type TPOTRatio struct {
	Value float64
	// Assumed marks a ratio the caller supplied rather than learned; answers
	// priced with it carry AssumedTPOTRatio.
	Assumed bool
}

// ratio resolves the multiplier a target applies to the mean ITL: 1 for the
// mean, R_q for a percentile, with the flags it brings.
func (t TPOTTarget) ratio() (float64, []Flag, *Unanswerable) {
	var u Unanswerable
	switch {
	case t.OverTokenGaps:
		u = refuse(TokenGapPercentile, "percentiles over token gaps are not modelled; use a per-request TPOT percentile")
	case t.Percentile == 0:
		return 1, nil, nil
	case !(t.Percentile > 0 && t.Percentile < 1):
		u = refuse(InvalidInput, fmt.Sprintf("TPOT percentile %g, want 0 < q < 1", t.Percentile))
	case t.Ratio.Value == 0:
		u = refuse(NoTPOTHistogram, fmt.Sprintf("no R_q for the p%g TPOT: no histogram and none assumed", 100*t.Percentile))
	case !Finite(t.Ratio.Value) || !(t.Ratio.Value >= 1):
		u = refuse(InvalidInput, fmt.Sprintf("R_q %g, want a finite ratio of at least 1", t.Ratio.Value))
	default:
		var flags []Flag
		if t.Ratio.Assumed {
			flags = append(flags, AssumedTPOTRatio)
		}
		return t.Ratio.Value, flags, nil
	}
	return 0, nil, &u
}

// checkHeadroom refuses a headroom outside (0, 1]: the fraction of the knee
// the operating point sits at, 1 lifting the cap.
func checkHeadroom(h float64) *Unanswerable {
	if h > 0 && h <= 1 {
		return nil
	}
	u := refuse(InvalidInput, fmt.Sprintf("headroom %g, want 0 < headroom <= 1", h))
	return &u
}

// RateForTPOT answers question 1 for decode: the largest rate, in requests per
// second over the fleet, at which the replicas hold the TPOT target
// (docs/proposals/slo-capacity-planner.md, "Question 1").
//
// It inverts ITL(k)*R_q = t at k* = (t/R_q - B)/A (R_q = 1 for the mean),
// refuses with TargetBelowFloor when ITL(band.Lo)*R_q already reaches t,
// clamps k* to k_full, and prices N_d * lambda_rep(k*). The rate is then capped
// at the operating point, headroom times the knee, because past it the
// admission wait is no longer small; a headroom of 1 lifts the cap.
//
// Basis.Uncapped is the rate the target alone allows, Basis.Fill the fill the
// answer runs at, and Basis.BoundBy what set the rate.
func RateForTPOT(d Decode, t TPOTTarget, headroom float64) Result {
	if u := d.check(); u != nil {
		return *u
	}
	if !Finite(t.Seconds) || !(t.Seconds > 0) {
		return refuse(InvalidInput, fmt.Sprintf("TPOT target %g s", t.Seconds))
	}
	r, flags, u := t.ratio()
	if u != nil {
		return *u
	}
	if u := checkHeadroom(headroom); u != nil {
		return *u
	}
	floor := d.Line.ITLAt(d.Band.Lo) * r
	if floor >= t.Seconds {
		return refuse(TargetBelowFloor, fmt.Sprintf("target %g s at or below the floor %g s", t.Seconds, floor))
	}
	p := d.full()
	k := (t.Seconds/r - d.Line.B) / d.Line.A
	by := []Bound{BoundTPOT}
	if k > p.fill {
		k, by = p.fill, []Bound{p.by}
		flags = append(flags, p.flags()...)
	}
	knee := d.knee()
	uncapped := float64(d.Replicas) * d.replicaRate(k)
	if !Finite(uncapped) || !(uncapped > 0) {
		return refuse(NoLine, fmt.Sprintf("the line prices no rate at fill %g", k))
	}
	rate := uncapped
	// A headroom of 1 is no cap: the knee bounds the rate, and the
	// operating point is not named.
	if op := headroom * knee; headroom < 1 && rate >= op {
		if rate > op {
			// The cap's own fill, from the closed form. Below the knee it
			// exists; if it did not, the answer would have no fill to stand
			// on, so it is refused rather than reported at the uncapped one.
			kop, ok := d.fillAtReplicaRate(op / float64(d.Replicas))
			if !ok {
				return refuse(Saturated, fmt.Sprintf("no fill carries the operating point %g r/s", op))
			}
			rate, k, by, flags = op, kop, nil, slices.DeleteFunc(flags, func(f Flag) bool { return f == BelowFittedBand })
			if kop < d.Band.Lo {
				flags = append(flags, BelowFittedBand)
			}
		}
		by = append([]Bound{BoundOperatingPoint}, by...)
	}
	return Answer{Value: rate, Flags: flags, Basis: Basis{
		Fill: k, Knee: knee, Floor: floor, Uncapped: uncapped, BoundBy: by,
	}}
}

// ReplicasQuery is question 2's input beyond the fleet: where to operate, and
// which TPOT percentile to report besides the mean.
type ReplicasQuery struct {
	// Headroom places the operating point at Headroom times the knee: the
	// scaling policy's scaleUpThreshold, 0.85 by default. Used when Rate is 0,
	// and then it must lie in (0, 1): unlike question 1, where 1 lifts the
	// cap, question 2 refuses 1 (InvalidInput), because the knee itself is a
	// point no fill carries.
	Headroom float64
	// Rate, when positive, is the operating rate in requests per second over
	// the fleet, in place of Headroom times the knee (--rate).
	Rate float64
	// Percentile, when set, asks for the TPOT at that per-request percentile
	// as well as the mean (TPOTTarget.Percentile, TPOTTarget.Ratio and
	// TPOTTarget.OverTokenGaps; Seconds is ignored).
	Percentile TPOTTarget
}

// DecodeSLO is question 2's decode answer. Each field is its own Result, so a
// percentile refused for want of a histogram leaves the mean answered.
type DecodeSLO struct {
	// Knee is the saturation rate, requests per second over the fleet.
	Knee Result
	// Rate is the operating rate the TPOTs are priced at.
	Rate Result
	// MeanTPOT is the mean TPOT there, in seconds.
	MeanTPOT Result
	// PercentileTPOT is the per-request TPOT percentile there, in seconds;
	// nil when no percentile was asked.
	PercentileTPOT Result
}

// SLOAtReplicas answers question 2 for decode: the knee, the operating rate
// below it, and the TPOT held there (docs/proposals/slo-capacity-planner.md,
// "Question 2"). The TPOT is the line at the closed-form fill, as the mean and,
// when asked, times R_q; it never beats the floor, ITL(band.Lo) (times R_q),
// and a fill below the band is read at the band's bottom and flagged
// BelowFittedBand.
func SLOAtReplicas(d Decode, q ReplicasQuery) DecodeSLO {
	if u := d.check(); u != nil {
		out := DecodeSLO{Knee: *u, Rate: *u, MeanTPOT: *u}
		if q.Percentile != (TPOTTarget{}) {
			out.PercentileTPOT = *u
		}
		return out
	}
	out := DecodeSLO{Knee: Knee(d)}
	knee := d.knee()

	var rate float64
	var rateBy []Bound
	var refused *Unanswerable
	switch {
	case q.Rate != 0:
		if !Finite(q.Rate) || q.Rate < 0 {
			u := refuse(InvalidInput, fmt.Sprintf("rate %g", q.Rate))
			refused = &u
		}
		rate = q.Rate
	default:
		// Question 2 operates below the knee: at a headroom of 1 the
		// operating point is the knee itself, where no fill carries the rate
		// and the admission wait is unbounded. Ask for the knee with Knee.
		if !(q.Headroom > 0 && q.Headroom < 1) {
			u := refuse(InvalidInput, fmt.Sprintf("headroom %g, want 0 < headroom < 1 for question 2", q.Headroom))
			refused = &u
		}
		rate, rateBy = q.Headroom*knee, []Bound{BoundOperatingPoint}
	}
	var k float64
	if refused == nil {
		switch res := FillAtRate(d, rate).(type) {
		case Answer:
			k = res.Value
		case Unanswerable:
			refused = &res
		}
	}
	if refused != nil {
		out.Rate, out.MeanTPOT = *refused, *refused
		if q.Percentile != (TPOTTarget{}) {
			out.PercentileTPOT = *refused
		}
		return out
	}
	out.Rate = Answer{Value: rate, Basis: Basis{Fill: k, Knee: knee, BoundBy: rateBy}}
	out.MeanTPOT = d.tpotAt(k, knee, 1, nil)
	if q.Percentile != (TPOTTarget{}) {
		p := q.Percentile
		if p.Percentile == 0 && !p.OverTokenGaps {
			// A percentile query with no percentile is a caller's mistake,
			// not a request for the mean twice.
			out.PercentileTPOT = refuse(InvalidInput, "TPOT percentile 0 asked for")
			return out
		}
		r, flags, u := p.ratio()
		if u != nil {
			out.PercentileTPOT = *u
		} else {
			out.PercentileTPOT = d.tpotAt(k, knee, r, flags)
		}
	}
	return out
}

// tpotAt is the TPOT at fill k, times r, never below the floor.
func (d Decode) tpotAt(k, knee, r float64, flags []Flag) Answer {
	floor := d.Line.ITLAt(d.Band.Lo) * r
	at := k
	if k < d.Band.Lo {
		at = d.Band.Lo
		flags = append(flags, BelowFittedBand)
	}
	return Answer{Value: d.Line.ITLAt(at) * r, Flags: flags, Basis: Basis{Fill: k, Knee: knee, Floor: floor}}
}

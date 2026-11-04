package whatif

// Result is what every what-if question returns: an Answer, an Unanswerable,
// or (from DecodeAdmission) an Admission. It is sealed, so a caller reaches a
// number only through a type switch or assertion, and a refusal can never be
// read as a value of 0. An unchecked assertion on a refusal panics rather than
// yielding a zero.
//
// docs/proposals/slo-capacity-planner.md, "Results and exit codes", sets out
// the contract.
type Result interface {
	result()
}

// Reason says why a question was refused. Each one is a refusal with a cause
// the operator can act on, never a number.
type Reason string

// The reasons this package refuses with. The CLI reference's table lists the
// planner's full set; each later PR adds the reasons its own terms can raise
// (NoPrefillCapacity, NoRoleMap, FlowControlUnknown, PinnedDefaultB), so every
// reason defined here has a case that produces it.
const (
	// NoLine is no ITL line, or one the planner cannot price from: a slope
	// or intercept that is not positive and finite, or a line that predicts no
	// positive ITL at the bottom of the band.
	NoLine Reason = "NoLine"
	// NoCapacity is no positive, finite KV capacity reading.
	NoCapacity Reason = "NoCapacity"
	// NoBatchCap is no batch cap known for the replica.
	NoBatchCap Reason = "NoBatchCap"
	// NoTraffic is no traffic shape: no positive output length or KV
	// footprint per request.
	NoTraffic Reason = "NoTraffic"
	// TokenGapPercentile is a percentile over token gaps (ITL), which is not
	// supported; a percentile over requests of their own TPOT is.
	TokenGapPercentile Reason = "TokenGapPercentile"
	// NoTPOTHistogram is a per-request TPOT percentile with no ratio R_q:
	// no TPOT histogram to learn it from, and none assumed.
	NoTPOTHistogram Reason = "NoTPOTHistogram"
	// TPOTQuantileUnbounded is a TPOT quantile that falls in a histogram's
	// +Inf bucket, which has no upper edge to read.
	TPOTQuantileUnbounded Reason = "TPOTQuantileUnbounded"
	// TargetBelowFloor is a target below what any number of replicas
	// reaches: the line's ITL at the bottom of the band, times R_q, already
	// meets or exceeds it.
	TargetBelowFloor Reason = "TargetBelowFloor"
	// Saturated is a rate at or above the knee, where the decode replicas
	// cannot drain their arrivals and no fill or wait exists.
	Saturated Reason = "Saturated"
	// InvalidInput is a question the caller asked wrongly: a NaN or infinite
	// value, a percentile outside (0, 1), a ratio below 1, a headroom outside
	// (0, 1], fewer than one replica, or a band that breaks FillBand's
	// precondition. The CLI maps it to a usage error, not to a refusal.
	InvalidInput Reason = "InvalidInput"
)

// Reasons lists every Reason this package can return, in a fixed order.
func Reasons() []Reason {
	return []Reason{NoLine, NoCapacity, NoBatchCap, NoTraffic, TokenGapPercentile,
		NoTPOTHistogram, TPOTQuantileUnbounded, TargetBelowFloor, Saturated, InvalidInput}
}

// Unanswerable is a refused question: the Reason, and a Detail naming the
// input that caused it.
type Unanswerable struct {
	Reason Reason
	Detail string
}

func (Unanswerable) result() {}

// Error lets an Unanswerable travel as an error where a caller wants one.
func (u Unanswerable) Error() string {
	if u.Detail == "" {
		return string(u.Reason)
	}
	return string(u.Reason) + ": " + u.Detail
}

func refuse(r Reason, detail string) Unanswerable {
	return Unanswerable{Reason: r, Detail: detail}
}

// Flag qualifies an answer without refusing it: the answer stands, and the
// flag says what it rests on that the operator should know.
type Flag string

// The flags this package raises.
const (
	// AssumedTPOTRatio is a percentile TPOT priced with a ratio R_q the
	// caller assumed rather than learned from a histogram.
	AssumedTPOTRatio Flag = "AssumedTPOTRatio"
	// CoarseTPOTBucket is an R_q read off a histogram bucket wider than a
	// quarter of the window's mean: the ratio is set by the bucket grid, not
	// by the traffic.
	CoarseTPOTBucket Flag = "CoarseTPOTBucket"
	// BelowFittedBand is a fill below the band the line was fitted over: the
	// TPOT is read at the band's bottom, the higher, safer reading, so the
	// answer's TPOT is a floor rather than the line's extrapolation.
	BelowFittedBand Flag = "BelowFittedBand"
	// KVBoundSlots is an admission chain whose slots are set by the KV cache
	// (C/KVreq at a full cache) rather than the batch cap: an approximation,
	// since KV-bound admission is not a fixed number of slots.
	KVBoundSlots Flag = "KVBoundSlots"
)

// Bound names what set a rate. On a tie every bound is named, the
// operating-point cap first.
type Bound string

// The bounds a decode rate can be set by.
const (
	// BoundOperatingPoint is the headroom cap: the operating point, a
	// fraction of the knee, past which decode admission waits are no longer
	// small.
	BoundOperatingPoint Bound = "operating-point cap"
	// BoundTPOT is the TPOT target itself.
	BoundTPOT Bound = "TPOT"
	// BoundBatchCap is the knee set by the batch cap's fill.
	BoundBatchCap Bound = "batch cap"
	// BoundBandTop is the knee set by the band's top, the highest fill the
	// line is fitted over.
	BoundBandTop Bound = "band top"
)

// Interval is a closed range [Lo, Hi].
type Interval struct{ Lo, Hi float64 }

// Basis is what an answer rests on, for the operator to check it against.
// A field that does not apply to the question is 0.
type Basis struct {
	// Fill is the KV fill one decode replica runs at for the answer.
	Fill float64
	// Knee is the rate, in requests per second over the fleet, at which the
	// decode replicas saturate.
	Knee float64
	// Floor is the lowest TPOT any number of replicas reaches: the line's
	// ITL at the bottom of the band, times R_q for a percentile.
	Floor float64
	// Uncapped is, for a rate, the rate the target alone allows before the
	// operating-point cap (the "TPOT alone" figure).
	Uncapped float64
	// BoundBy names what set a rate, the operating-point cap first on a tie.
	BoundBy []Bound
}

// Answer is a priced figure: Value in the question's unit (requests per
// second for a rate, seconds for a TPOT or a wait, a ratio for R_q), its
// Basis and Flags.
type Answer struct {
	Value float64
	// Band is the figure's uncertainty band, nil until bands are computed
	// (the block bootstrap of the planner's D3 PR). Nil means "not computed",
	// never "no uncertainty".
	Band  *Interval
	Basis Basis
	Flags []Flag
}

func (Answer) result() {}

// HasFlag reports whether the answer carries f.
func (a Answer) HasFlag(f Flag) bool {
	for _, g := range a.Flags {
		if g == f {
			return true
		}
	}
	return false
}

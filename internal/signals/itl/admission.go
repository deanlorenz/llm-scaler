package itl

import (
	"math"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// Admission is what decides, per Observe, which replica rows are readings:
// the KV band and the sequence-cap exception above it; and how long a key's
// last A may stand in for a fit when there are too few readings for one. The
// zero value is the defaults with no cap known, which admits nothing above the
// band, and holds an A without limit.
type Admission struct {
	// MinK and MaxK are the band's edges; zero takes DefaultMinObservableK
	// and DefaultMaxObservableK. The policy keys analyzerTuning.itlBand.minObservableK and
	// analyzerTuning.itlBand.maxObservableK set them (config.ScalingPolicy.ITLBand), which
	// validates 0 < MinK < MaxK < 1.
	MinK, MaxK float64
	// MaxNumSeqs is the engine's batch cap, per rank, as something states it:
	// its flags (max_num_seqs, lowered by --max-num-active-seqs when that is
	// set), or where they state none the llm-d.ai/max-num-seqs annotation
	// (capacity.EngineParams.StatedBatchCap). Zero when it is unknown or
	// only a default, which admits nothing above MaxK.
	MaxNumSeqs int64
	// MaxNumSeqsSource is where MaxNumSeqs came from, for the log only
	// (flags, declared, MaxNumSeqsSourceNone): nothing here reads it.
	MaxNumSeqsSource string
	// CapMaxPreemptionRate is the most preemptions per second a capped
	// replica may show over the ITL's window and still be read above MaxK.
	// The policy key analyzerTuning.itlBand.capMaxPreemptionRate sets it; zero, the default,
	// requires none.
	CapMaxPreemptionRate float64
	// Steady is the steady-state gate: which readings teach the full fit and
	// the pinned one. The policy's analyzerTuning.steadyGate sets it; a zero
	// field takes its default (DefaultSteadiness).
	Steady Steadiness
	// SlopeHoldFor is how long after it was fitted a key's last A may be
	// held when the window cannot fit (Tracker.Observe). The analyzers pass
	// their bucket retention, the horizon a finished fit is kept on
	// elsewhere; zero holds it without limit.
	SlopeHoldFor time.Duration
	// Args is the engine arguments the rows were produced under
	// (capacity.EngineParams.ArgsFingerprint), empty when unknown. An A
	// fitted under other known arguments is not held: it describes another
	// engine.
	Args string

	// Settle, when set, is the settle tracker a replica's settle is read
	// from (settle.go). Nil keeps the key's own trace of each replica, the
	// rule before the tracker.
	Settle *Settle
	// WaitsAfterRestart makes a replica already serving on the key's first
	// Observe wait its settle too: a controller restart is a settle, and the
	// line does not refit from it for Steadiness.For. Without it, as before,
	// such a replica is taken as settled.
	WaitsAfterRestart bool
	// KeepsThroughMissingRow keeps a replica's settle through a cycle it is
	// missing from, up to the absence tolerance: one missed scrape is not a
	// replica that left and came back. On the tracker the tracker's
	// tolerance rules; on the key's own trace, AbsenceTolerance. Without
	// it, as before, a replica missing from one cycle starts over.
	KeepsThroughMissingRow bool
	// AbsenceTolerance is how long a replica may go without a row and keep
	// its trace on the key's own trace (KeepsThroughMissingRow): the settle
	// tracker's tolerance at the controller's interval
	// (settle.Tolerance). At or below settle.Period, settle.Period.
	AbsenceTolerance time.Duration
	// RestartUnderName, on the tracker (Settle), starts a settle when a
	// replica's Ready time changes under the same Pod name
	// (settle.Restarted), whether or not a row that was not Ready came
	// between. Without it, as on the key's own trace, only the row that was
	// not Ready shows the restart.
	RestartUnderName bool
	// IdleOnEvidence, on the tracker, reads load returning after idleRows
	// idle rows from the tracker, idle on evidence (settle.Row.Activity: a
	// known KV of 0, and a known empty ITL window or a known generation
	// rate of 0); a scrape that read nothing is not idle. Without it, as on
	// the key's own trace, a row with neither an ITL nor a k is idle.
	IdleOnEvidence bool
	// FiniteOnly reads a row's ITL by its presence bits: a reading needs the
	// Finite state (domain.ReplicaMetrics.ITLState), so a Pod with an Absent
	// rank, whose AvgITL is its Finite ranks' mean, is no reading. Without
	// it, as before, any finite AvgITL above zero is.
	FiniteOnly bool
	// AboveBandFallback keeps the readings above the band that are not
	// admitted (no sequence cap), at a k of at least DefaultKSat, in a
	// window of their own, stamped by the same steady gate and settle as the
	// band's, and fits a line from them, B pinned at their least ITL, when
	// the band gives none and the key holds no slope it may hold
	// (Reading.AboveBandFit). It never prices under the true line at
	// DefaultKSat (fitAboveBand), and never sets the held slope or the
	// intercept. Without it, as before, such a reading is only counted.
	AboveBandFallback bool
	// AboveBandWaitsAfterRestart stamps the above-band window's readings
	// with the restart's wait too (WaitsAfterRestart), as learner PR 4b-2
	// first built it, before its review exempted them (aboveBandSettling).
	// Only negative controls set it.
	AboveBandWaitsAfterRestart bool
}

// MaxNumSeqsSourceNone is Admission.MaxNumSeqsSource when no cap is stated:
// nothing above the band is admitted. The analyzers that log the source
// share this one value.
const MaxNumSeqsSourceNone = "none"

// PolicyReader is what an ITL line reads of a scaling policy: the KV band
// (analyzerTuning.itlBand), the steady-state gate (analyzerTuning.steadyGate)
// and the most preemption a capped replica may show
// (analyzerTuning.itlBand.capMaxPreemptionRate). *config.ScalingPolicy
// implements it, nil included, and asserts so at compile time: an analyzer
// that cannot import config reads the policy through it, and a renamed
// method fails the build instead of reading the defaults.
type PolicyReader interface {
	ITLBand() (lo, hi float64)
	ITLSteadiness() Steadiness
	ITLCapPreemptionRate() float64
}

// mayHold reports whether a key's last A may stand in for a fit at now: it was
// fitted within SlopeHoldFor, and not under other engine arguments. Arguments
// unknown on either side are not a change, as the saturation analyzer's own
// window reset (itlUnderArgs) treats them.
func (a Admission) mayHold(s heldSlope, now time.Time) bool {
	if a.SlopeHoldFor > 0 && now.Sub(s.fittedAt) > a.SlopeHoldFor {
		return false
	}
	return a.Args == "" || s.args == "" || a.Args == s.args
}

// band is the admission's KV band, with the defaults for an edge left at zero.
func (a Admission) band() (lo, hi float64) {
	lo, hi = a.MinK, a.MaxK
	if !(lo > 0) {
		lo = DefaultMinObservableK
	}
	if !(hi > 0) {
		hi = DefaultMaxObservableK
	}
	return lo, hi
}

// atSequenceCap reports whether the replica's batch is at its sequence cap,
// with no more preemption over the ITL's window than CapMaxPreemptionRate, so
// its reading above the band is a point on its line (issue #287).
//
// The band stops at 0.80 to keep an engine thrashing out of the fit: the
// readings above it steepen A and push B below zero, and preemption tracks
// them (see DefaultMaxObservableK). A replica whose batch is at max_num_seqs
// is not short of memory. It cannot take another sequence however much cache
// is free, and a fill above the band is its sequences' contexts, not
// pressure. Under uneven routing that replica can be the only one with a k
// in reach: on one run a decode replica held 256 running (its max_num_seqs)
// and 768 waiting at k 0.85-0.89 for 20 minutes while three siblings sat
// below 0.15, and no line was ever fitted.
//
// Every condition is required, and an unknown one admits nothing:
//   - MaxNumSeqs is stated -- by the engine's flag, or by the scale target's
//     llm-d.ai/max-num-seqs annotation -- not a default and not learned: vLLM
//     V1 defaults to 1024 on large accelerators where the parser assumes 256,
//     and a batch of 300 would read as capped while it is still growing. A
//     learned cap may sit below the true one in the same way.
//   - The running batch is EXACTLY the cap on every rank (max_num_seqs bounds
//     one engine, so the cap is it times Ranks). More than the cap means the
//     cap is not what was read; fewer means memory, not the cap, binds.
//   - Requests are waiting: a batch at the cap with nothing queued is not
//     held there by the cap. The full waiting count, also on a P/D decode,
//     where part of it is vLLM's reason="deferred": at the cap the scheduler
//     stops before it looks at a request whose KV pull has landed, so that
//     request stays deferred although only the cap holds it.
//   - The preemption rate was read and is at most CapMaxPreemptionRate: an
//     engine preempting for memory is the case the band exists for.
func (a Admission) atSequenceCap(rm domain.ReplicaMetrics) bool {
	return rm.AtBatchCap(a.MaxNumSeqs) &&
		rm.QueueLength > 0 &&
		rm.PreemptionRateKnown && !math.IsNaN(rm.PreemptionRate) &&
		rm.PreemptionRate <= max(a.CapMaxPreemptionRate, 0)
}

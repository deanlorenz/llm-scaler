package throughput

import (
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle/settlerow"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
)

// The ITL line's settle on the engine's settle tracker (analyzer-learner.md,
// PR 4b-2). Since 4b-1 the line stamps a reading taken while its replica
// settles and keeps it out of the pinned fit, judging the settle from the
// key's own trace of each replica. From 4b-2 the settle start is read from
// the engine's tracker, which the engine observes at the top of every cycle,
// before any analyzer runs, and hands this analyzer read-only
// (WithSettleView). Six replay switches, one per named change, as the
// saturation and slo analyzers' lines took them in learner PR 4a: the move
// itself, and five behaviour changes that ride on it. Each is meaningful
// only with SwitchITLSteadyGate on: the parent's window has no settle.
//
// An analyzer given no view (a test that drives Analyze alone, a replay of a
// recording from before 4b-0) observes a tracker of its own at the top of
// each Analyze, as the slo analyzer does, standing for a controller that has
// watched the fleet: nothing waits after a restart.

// SwitchITLReadsSettle is the replay switch for the move. On, a replica's
// settle starts where the settle tracker says: it joined, came back past
// the absence tolerance (settle.Rejoined), was lent, or rolled out to a new
// revision under its name (settle.RolledOut); load came back after
// steadyGate.idleRows idle rows; or it was Ready again after a row that was
// not. Off, as in 4b-1, the line's own trace of each replica: a joiner, a
// return after idleRows rows with neither an ITL nor a k, and any replica
// missing from a cycle or not Ready on one.
//
// Differences that are the move's own: a gap between two of the model's
// cycles longer than the absence tolerance (a controller paused, a clock
// jump) is an absence to the tracker, and every replica settles again; the
// line's own trace counted an absence only by cycles that went without a
// replica. A new revision under the same Pod name settles; the own trace
// saw it only through a row that was not Ready. A warm-pool lend is a new
// key on the tracker, but the line reads no lent row on either path
// (ownReplicasOnly, and itl.Tracker refuses FromWarmPool), so a lend moves
// nothing here.
const SwitchITLReadsSettle = "throughput-itl-reads-the-settle-tracker"

// SwitchITLWaitsAfterRestart is the replay switch for the line's wait after
// a controller restart (analyzer-learner.md 4.4). The engine's tracker is
// new with the controller, so it first sees every replica on the first
// cycle (settle.Joined). On, a replica already serving on the line's first
// cycle waits its settle too, from the tracker's first sight of it: no line
// from the band for steadyGate.steadyFor after a restart, then until the
// window holds itl.DefaultMinSamples readings, and the analyzer is silent
// meanwhile unless the above-band fallback prices it. The fallback's window
// does not wait (it is one-sided: a warm-up reading can only raise its
// price), only a replica that became Ready within steadyFor is kept out of
// it. Off, as in 4b-1, such a replica is taken as settled. The wait is read
// only on the tracker (SwitchITLReadsSettle on) with an engine view: an
// analyzer with no view stands for a controller that has watched the
// fleet, and the line's own trace does not wait.
const SwitchITLWaitsAfterRestart = "throughput-itl-waits-after-controller-restart"

// SwitchITLKeepsSettleThroughMissingRow is the replay switch for the
// absence rule (analyzer-learner.md 4.2). On, a replica missing from a
// cycle keeps its settle for up to the tracker's absence tolerance
// (settle.Tolerance: 2.5 optimize intervals, at least 1 min): one missed
// scrape is not a replica that left. Off, as in 4b-1, a replica missing
// from one cycle starts its settle over.
const SwitchITLKeepsSettleThroughMissingRow = "throughput-itl-keeps-the-settle-through-a-missing-row"

// SwitchITLRestartUnderName is the replay switch for an engine restart under
// the same Pod name, on the tracker. On, a new Ready time under the same
// name starts a settle (settle.Restarted), whether or not a row that was not
// Ready came between. Off, as on the line's own trace, only a row that was
// not Ready shows the restart.
const SwitchITLRestartUnderName = "throughput-itl-restart-under-its-name-settles"

// SwitchITLIdleOnEvidence is the replay switch for what an idle row is, on
// the tracker. On, a row is idle only on evidence (settle.Row.Activity: a
// known KV of 0, and a known empty ITL window or a known generation rate of
// 0; on a prefill pod a known request rate of 0); a scrape that read
// nothing neither counts nor breaks an idle run. Off, as on the line's own
// trace, a row with neither an ITL nor a k is idle.
const SwitchITLIdleOnEvidence = "throughput-itl-idle-needs-evidence"

// SwitchITLReadsFinitePodMean is the replay switch for what a reading's ITL
// is (analyzer-learner.md 2.3). On, a row is a reading only in the Finite
// state its presence bits give (domain.ReplicaMetrics.ITLState): a Pod with
// an Absent rank, whose AvgITL is its Finite ranks' mean, is refused. Off,
// as in 4b-1, any finite AvgITL above zero is a reading.
const SwitchITLReadsFinitePodMean = "throughput-itl-reads-the-finite-pod-mean"

// ownSettleBackdate is how far before its first Analyze of a model the
// analyzer's own tracker first observes the model's rows
// (settlerow.ObserveOwn), as the slo analyzer's. For the ITL line it does
// not matter: a line with no engine view never waits after a restart.
const ownSettleBackdate = itl.DefaultSteadyFor

// ownSettleTimeout is how long the analyzer's own tracker keeps a model it
// has not analyzed: as long as the model's variant state lives.
const ownSettleTimeout = 2 * itl.DefaultObservationMaxAge

// settleView is the tracker the line reads: the engine's when it was given
// one, else the analyzer's own.
func (a *ThroughputAnalyzer) settleView() settle.View {
	if a.settle != nil {
		return a.settle
	}
	return a.ownSettle
}

// observeOwnSettle observes input's rows into the analyzer's own tracker at
// now, when the engine gave it none. Every row, lent ones included: the
// tracker keys a lend apart, as the engine's does.
//
// A model last observed more than ownSettleTimeout ago is first-seen, as
// one evicted is: its observations are forgotten and observed again
// backdated, and its line's keys forget their replicas' settle traces
// (itl.Tracker.ForgetTraces), so the keys read every replica as on their
// first cycle and nothing settles. The eviction (evictOwnSettleLocked) runs
// inside observe, after the model analyzed first has observed: before this,
// after a long gap that model read the gap as an absence (settle.Rejoined)
// and settled, while every model after it, evicted meanwhile, was
// first-seen and did not, so the result depended on the order. Part of
// SwitchITLReadsSettle: off, the line reads no tracker and keeps its own
// trace.
func (a *ThroughputAnalyzer) observeOwnSettle(input domain.AnalyzerInput, now time.Time) {
	if a.settle != nil {
		return
	}
	roles := make(map[string]string, len(input.VariantStates))
	for _, vs := range input.VariantStates {
		roles[vs.VariantName] = vs.Role
	}
	model := utils.GetNamespacedKey(input.Namespace, input.ModelID)
	a.mu.Lock()
	last, seen := a.ownSettled[model]
	stale := seen && now.Sub(last) > ownSettleTimeout && a.itlReadsSettle
	a.ownSettled[model] = now
	if stale {
		for _, rm := range input.ReplicaMetrics {
			a.lines.ForgetTraces(variantKey(input.Namespace, input.ModelID, rm.VariantName))
		}
	}
	a.mu.Unlock()
	if stale {
		a.ownSettle.Forget(model)
	}
	settlerow.ObserveOwn(a.ownSettle, model, settlerow.Of(input.ReplicaMetrics, roles), !seen || stale, ownSettleBackdate, now)
}

// evictOwnSettleLocked forgets what the analyzer's own tracker holds of a
// model it has not analyzed for ownSettleTimeout. Callers hold a.mu.
func (a *ThroughputAnalyzer) evictOwnSettleLocked(now time.Time) {
	for model, at := range a.ownSettled {
		if now.Sub(at) > ownSettleTimeout {
			delete(a.ownSettled, model)
			a.ownSettle.Forget(model)
		}
	}
}

// settleAdmission sets on adm how the line reads its replicas' settle for a
// model, by the six switches. Callers hold a.mu, which guards the switches.
func (a *ThroughputAnalyzer) settleAdmission(adm *itl.Admission, namespace, modelID string) {
	adm.Settle = nil
	if a.itlReadsSettle {
		adm.Settle = &itl.Settle{View: a.settleView(), Model: utils.GetNamespacedKey(namespace, modelID)}
	}
	// The wait reads the engine's tracker: on the line's own trace it would
	// wait for every key the analyzer makes anew, not a controller restart.
	adm.WaitsAfterRestart = a.itlWaitsAfterRestart && a.itlReadsSettle && a.settle != nil
	adm.KeepsThroughMissingRow = a.itlKeepsSettle
	adm.FiniteOnly = a.itlFiniteOnly
	adm.RestartUnderName, adm.IdleOnEvidence = a.itlRestartUnderName, a.itlIdleOnEvidence
}

package saturation

import (
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
)

// The ITL line on the settle tracker (analyzer-learner.md, section 7, PR
// 4a). The line keeps out of its fits the readings of a replica inside its
// settle (itl.DefaultSteadyFor); when that settle began is now read from the
// engine's settle tracker, through the view k2 reads (settle_view.go). Four
// replay switches, one per named change: the move itself, which the
// recordings hold neutral, and three behaviour changes.

// SwitchITLReadsSettle is the replay switch for the move (analyzer-learner.md
// 4.1-4.3). On, a replica's settle starts where the settle tracker says: it
// joined, came back past the absence tolerance, restarted under its own
// name, was lent or rolled out (settle.Replica.Since); load came back after
// steadyGate.idleRows idle rows, idle read on positive evidence
// (settle.Row.Activity: a known KV of 0 and a known empty ITL window or a
// known generation rate of 0); or it was Ready again after a row that was
// not. Off, as before, the line's own trace of each replica: a joiner, a
// return after idleRows rows with neither an ITL nor a k, and any replica
// missing from a cycle or not Ready on one.
const SwitchITLReadsSettle = "itl-reads-the-settle-tracker"

// SwitchITLWaitsAfterRestart is the replay switch for the line's wait after
// a controller restart (analyzer-learner.md 4.4). On, a replica already
// serving on the line's first cycle waits its settle too, from the
// tracker's first sight of it: the line does not refit for
// steadyGate.steadyFor after a restart. Off, as before, such a replica is
// taken as settled. An analyzer with its own tracker (no engine, a test)
// never waits: it stands for a controller that has watched the fleet.
const SwitchITLWaitsAfterRestart = "itl-waits-after-controller-restart"

// SwitchITLKeepsSettleThroughMissingRow is the replay switch for the
// absence rule (analyzer-learner.md 4.2). On, a replica missing from a
// cycle keeps its settle for up to the tracker's absence tolerance
// (settle.Tolerance): one missed scrape is not a replica that left. Off, as
// before, a replica missing from one cycle starts its settle over.
const SwitchITLKeepsSettleThroughMissingRow = "itl-keeps-the-settle-through-a-missing-row"

// SwitchITLReadsFinitePodMean is the replay switch for what a reading's ITL
// is (analyzer-learner.md 2.3). On, a row is a reading only in the Finite
// state its presence bits give (domain.ReplicaMetrics.ITLState): a Pod with
// an Absent rank, whose AvgITL is its Finite ranks' mean, is refused. Off,
// as before, any finite AvgITL above zero is a reading.
const SwitchITLReadsFinitePodMean = "itl-reads-the-finite-pod-mean"

// itlSettleAdmission sets on adm how the line reads its replicas' settle
// for a model, by the four switches.
func (a *SaturationAnalyzer) itlSettleAdmission(adm *itl.Admission, namespace, modelID string) {
	a.mu.Lock()
	reads, waits, keeps, finite := a.itlReadsSettle, a.itlWaitsAfterRestart, a.itlKeepsSettle, a.itlFiniteOnly
	interval := a.settleInterval
	a.mu.Unlock()
	if reads {
		adm.Settle = &itl.Settle{View: a.settleView(), Model: settleModel(namespace, modelID)}
	}
	// The analyzer alone stands for a controller that has watched the
	// fleet (observeOwnSettle), so nothing restarted.
	adm.WaitsAfterRestart = waits && a.settle != nil
	adm.KeepsThroughMissingRow = keeps
	if interval > 0 {
		adm.AbsenceTolerance = settle.Tolerance(interval)
	}
	adm.FiniteOnly = finite
}

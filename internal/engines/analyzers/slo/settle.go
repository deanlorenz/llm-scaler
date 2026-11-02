package slo

import (
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle/settlerow"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
)

// The SLO line on the settle tracker (analyzer-learner.md, section 7, PR
// 4a): the same four changes as the saturation analyzer's ITL line, each
// behind a switch of this package, since this analyzer fits its own line
// with its own itl.Tracker. The engine writes the tracker once per model per
// cycle, before any analyzer runs, and hands this analyzer a read-only view
// (WithSettleView). An analyzer given none (a test that drives Analyze
// alone) observes a tracker of its own at the top of each Analyze, as one
// that has watched the fleet: nothing restarted.

// SwitchITLReadsSettle is the replay switch for the move: on, a replica's
// settle starts where the settle tracker says (itl.Settle); off, as before,
// the line's own trace of each replica.
const SwitchITLReadsSettle = "slo-itl-reads-the-settle-tracker"

// SwitchITLWaitsAfterRestart is the replay switch for the line's wait after
// a controller restart: on, a replica already serving on the line's first
// cycle waits its settle too, so the line does not refit for
// steadyGate.steadyFor after a restart; off, as before, it is taken as
// settled. An analyzer with its own tracker never waits.
const SwitchITLWaitsAfterRestart = "slo-itl-waits-after-controller-restart"

// SwitchITLKeepsSettleThroughMissingRow is the replay switch for the
// absence rule: on, a replica missing from a cycle keeps its settle up to
// the tracker's absence tolerance; off, as before, it starts over.
const SwitchITLKeepsSettleThroughMissingRow = "slo-itl-keeps-the-settle-through-a-missing-row"

// SwitchITLReadsFinitePodMean is the replay switch for what a reading's ITL
// is: on, a row is a reading only in the Finite state its presence bits
// give, so a Pod with an Absent rank is refused; off, as before, any finite
// AvgITL above zero is a reading.
const SwitchITLReadsFinitePodMean = "slo-itl-reads-the-finite-pod-mean"

// WithSettleView sets the settle tracker the line reads each replica's
// settle from: the engine's, which it observes at the top of every cycle.
// Call it before the analyzer is used. Without it the analyzer observes a
// tracker of its own.
func (a *Analyzer) WithSettleView(v settle.View) *Analyzer {
	a.settle = v
	return a
}

// ownSettleBackdate is how far before its first Analyze of a model the
// analyzer's own tracker first observes the model's rows: the line's settle,
// so a replica on the first cycle has settled (settlerow.ObserveOwn).
const ownSettleBackdate = itl.DefaultSteadyFor

// settleView is the tracker the line reads: the engine's when it was given
// one, else its own.
func (a *Analyzer) settleView() settle.View {
	if a.settle != nil {
		return a.settle
	}
	return a.ownSettle
}

// observeOwnSettle observes input's rows into the analyzer's own tracker at
// now, when the engine gave it none.
func (a *Analyzer) observeOwnSettle(input domain.AnalyzerInput, now time.Time) {
	if a.settle != nil {
		return
	}
	roles := make(map[string]string, len(input.VariantStates))
	for _, vs := range input.VariantStates {
		roles[vs.VariantName] = vs.Role
	}
	model := utils.GetNamespacedKey(input.Namespace, input.ModelID)
	a.mu.Lock()
	_, seen := a.ownSettled[model]
	a.ownSettled[model] = now
	a.mu.Unlock()
	settlerow.ObserveOwn(a.ownSettle, model, settlerow.Of(input.ReplicaMetrics, roles), !seen, ownSettleBackdate, now)
}

// evictOwnSettleLocked forgets what the analyzer's own tracker holds of a
// model it has not analyzed for the state timeout. Callers hold a.mu.
func (a *Analyzer) evictOwnSettleLocked(now time.Time) {
	for model, at := range a.ownSettled {
		if now.Sub(at) > a.stateTimeout {
			delete(a.ownSettled, model)
			a.ownSettle.Forget(model)
		}
	}
}

// settleAdmission sets on adm how the line reads its replicas' settle for a
// model, by the four switches.
func (a *Analyzer) settleAdmission(adm *itl.Admission, namespace, modelID string) {
	a.mu.Lock()
	reads, waits, keeps, finite := a.itlReadsSettle, a.itlWaitsAfterRestart, a.itlKeepsSettle, a.itlFiniteOnly
	a.mu.Unlock()
	if reads {
		adm.Settle = &itl.Settle{View: a.settleView(), Model: utils.GetNamespacedKey(namespace, modelID)}
	}
	adm.WaitsAfterRestart = waits && a.settle != nil
	adm.KeepsThroughMissingRow = keeps
	adm.FiniteOnly = finite
}

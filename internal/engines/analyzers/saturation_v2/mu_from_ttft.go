package saturation_v2

import (
	"time"

	"github.com/go-logr/logr"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/logging"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
)

// notePrefill folds this cycle's prefill readings into the variant's TTFT
// window and returns the fitted model, which is the zero Model until the
// window is Ready.
//
// The prefill counterpart of noteITL. Same shape, different regressor: decode
// is bounded by resident KV and prefill by the prompt tokens it has to
// compute, so decode regresses ITL on k and prefill regresses TTFT on the
// backlog in tokens (internal/signals/prefill).
//
// The observation is taken from replicas that are Ready and not warm-pool
// bridges, for the reasons the throughput window already documents: a pod
// failing its readiness probe reports completions whose timing the collector
// deliberately drops, and a bridge runs its engine on the pool's terms, so
// neither describes one of this variant's replicas.
func (a *SaturationAnalyzer) notePrefill(key string, replicas []domain.ReplicaMetrics,
	variantName string, avgInputTokens float64, now time.Time, logger logr.Logger) prefill.Model {
	// Same lock discipline as noteITL: it covers the window mutation only,
	// and the log lines are emitted after it is dropped rather than
	// serialising every other model's cycle behind this one's fit.
	a.mu.Lock()
	w, ok := a.prefillWindows[key]
	if !ok {
		w = prefill.NewWindow(prefill.DefaultWindowMaxSize)
		a.prefillWindows[key] = w
	}

	// Counted per reason, as noteITL counts its own: "the window is empty"
	// has several causes with different fixes, and a run that cannot tell
	// them apart costs a rebuild to find out which one it was.
	var considered, notReady, noTTFT, noBacklog, added int
	for _, rm := range replicas {
		if rm.VariantName != variantName {
			continue
		}
		considered++
		if !rm.Ready || rm.FromWarmPool {
			notReady++
			continue
		}
		if !(rm.AvgTTFT > 0) {
			noTTFT++
			continue
		}
		// The regressor: prompt tokens accepted and not yet first-tokened.
		// Running plus waiting, because a request waiting in the engine's
		// queue is work ahead of the next arrival exactly as a running one
		// is -- that is what makes the slope a reciprocal velocity rather
		// than a per-request cost.
		//
		// The running half is already in tokens (TokensInUse); the waiting
		// half is a request count, and the shape's average prompt length is
		// what turns it into tokens. Without that length the waiting half
		// cannot be priced, and a backlog missing its queue would regress
		// the busiest cycles at their smallest T -- flattening the slope,
		// which INFLATES the velocity and under-orders.
		if !(avgInputTokens > 0) {
			noBacklog++
			continue
		}
		backlog := float64(rm.TokensInUse) + float64(rm.QueueLength)*avgInputTokens
		if !(backlog >= prefill.DefaultMinObservableTokens) {
			noBacklog++
			continue
		}
		if w.Add(prefill.Observation{Tokens: backlog, TTFTSec: rm.AvgTTFT, At: now}) {
			added++
		}
	}
	w.Prune(now, prefill.DefaultObservationMaxAge)
	ready := w.Ready(prefill.DefaultMinSamples, prefill.DefaultMinTokenSpread)
	samples := w.Len()
	var model prefill.Model
	var fitted bool
	if ready {
		model, fitted = prefill.Fit(w.Observations())
	}
	a.mu.Unlock()

	// At DEFAULT for the same reason noteITL is: the deployment passes no -v,
	// so a DEBUG line is one nobody can read, and this is one line per prefill
	// variant per cycle.
	logger.V(logging.DEFAULT).Info("prefill-ttft-window",
		"variant", variantName, "considered", considered, "added", added,
		"samples", samples, "ready", ready, "fitted", fitted,
		"notReady", notReady, "noTTFT", noTTFT, "noBacklog", noBacklog,
		"avgInputTokens", avgInputTokens,
		"tokenVelocity", model.TokenVelocity(), "A", model.A, "B", model.B)
	if !fitted {
		return prefill.Model{}
	}
	return model
}

// derivePrefillMu converts a fitted prefill model into the mu the demand floor
// divides by, which is a REQUEST rate.
//
// The model's own figure is a token velocity, which is the honest unit for
// prefill (docs/proposals/prefill-ttft-model.md). The floor is
// request-denominated throughout, so the conversion happens here, at the
// boundary, rather than by changing what the floor means:
//
//	mu [req/s] = V [tokens/s] / ILeff [tokens/req]
//
// ILeff and not the raw prompt length: a prefix the cache already holds costs
// prefill nothing, so a request whose prompt is mostly cached consumes less of
// the velocity than its length suggests.
func derivePrefillMu(m prefill.Model, ileff float64) derivedMu {
	v := m.TokenVelocity()
	if !(v > 0) || !(ileff > 0) {
		return derivedMu{}
	}
	rate := v / ileff
	if !(rate > 0) {
		return derivedMu{}
	}
	return derivedMu{rate: rate, tokenSec: v, ok: true}
}

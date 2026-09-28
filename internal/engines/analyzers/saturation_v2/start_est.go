package saturation_v2

import (
	"time"

	"github.com/go-logr/logr"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/logging"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
)

// How long a replica of this variant takes to become Ready, which the demand
// floor projects the backlog forward over.
//
// It has to be a measurement. Run T measured 67 s for four pods and 82 s for
// five, while the controller ordered one replica per cycle for a minute and the
// queue reached 191 -- a projection over a guessed dead time would have been
// wrong by whatever the guess was, multiplied by the arrival rate.
//
// Per VARIANT, not per model: it is an image, a set of engine flags and a node,
// and two variants of one model routinely differ. Keyed exactly like the ITL
// windows, and swept with them, so a deleted or renamed variant does not leave a
// float behind for as long as the process lives.

const (
	// DefaultReplicaStartSeconds is the seed for a variant that has never had a
	// replica start under observation.
	//
	// 70 s, the middle of run T's measured 67-82 s for a small model on an H200.
	// It is deliberately NOT generous: a seed that is too long over-states the
	// backlog that will accumulate and over-orders, and the first real
	// measurement replaces it within one replica start. A GLM-5.2 cold start
	// with a cold JIT cache is nearer 500 s, which is exactly why an operator
	// can seed it per ScaledObject rather than living with this number.
	DefaultReplicaStartSeconds = 70.0

	// startSecondsAlpha weights a new measurement against the running estimate.
	//
	// 0.3 -- slow enough that one unlucky start (an image pull on a cold node)
	// does not move the estimate far, fast enough that a genuine change in image
	// or node class is absorbed within a handful of starts. Run T's own spread,
	// 67 to 82 s, moves a 70 s estimate to 73.6 s on one 82 s sample, which is
	// the right order of response for a figure the projection multiplies by
	// lambda.
	startSecondsAlpha = 0.3
)

// noteReplicaStart folds this cycle's observed start times into the per-variant
// estimate, and publishes both the observation and the estimate.
//
// Warm-pool bridges are skipped. A bridged Pod reaching Ready in a second is a
// wake, not a start, and it says nothing about how long this variant's own
// replicas take -- the same exclusion noteITL makes for the fit and
// floor.Estimate makes for the price.
//
// An observation is counted ONCE per replica. Every cycle sees the same Ready
// Pod reporting the same StartSeconds, so folding it in on every cycle would
// drive the estimate to whatever the longest-lived replica measured and make the
// histogram a count of cycles rather than of starts.
func (a *SaturationAnalyzer) noteReplicaStart(
	key, namespace, variantName string,
	replicas []domain.ReplicaMetrics,
	logger logr.Logger,
) {
	a.mu.Lock()
	observed := 0
	for _, rm := range replicas {
		if rm.VariantName != variantName || rm.FromWarmPool {
			continue
		}
		if !(rm.StartSeconds > 0) || rm.PodName == "" {
			continue
		}
		if _, seen := a.startSeenPods[rm.PodName]; seen {
			continue
		}
		a.startSeenPods[rm.PodName] = a.now()
		observed++

		if cur, ok := a.startSeconds[key]; ok && cur > 0 {
			a.startSeconds[key] = (1-startSecondsAlpha)*cur + startSecondsAlpha*rm.StartSeconds
		} else {
			// The first measurement REPLACES the seed rather than being averaged
			// with it. The seed is a guess and the measurement is not, so giving
			// the guess 70% of the weight of the first real figure would keep a
			// wrong number in force for several starts.
			a.startSeconds[key] = rm.StartSeconds
		}
		metrics.ObserveReplicaStartSeconds(namespace, variantName, rm.StartSeconds)
	}
	est, measured := a.startSeconds[key]
	a.mu.Unlock()

	if !measured || !(est > 0) {
		est = DefaultReplicaStartSeconds
	}
	metrics.SetReplicaStartSecondsEstimate(namespace, variantName, est, measured)
	if observed > 0 {
		logger.V(logging.DEFAULT).Info("replica-start-measured",
			"variant", variantName, "observations", observed,
			"estimateSeconds", est, "source", startSource(measured))
	}
}

// startSource labels the estimate for the log and the metric, so a run says
// whether it sized against a measurement or a guess.
func startSource(measured bool) string {
	if measured {
		return "measured"
	}
	return "seed"
}

// evictStartSeenPods drops the once-per-replica bookkeeping for Pods nothing has
// reported for the timeout.
//
// Without it the set grows by one entry per Pod the process ever sees, which on
// a fleet that scales up and down all day is unbounded. Called from
// EvictStaleHistory, under the same lock and the same timeout as the windows.
func (a *SaturationAnalyzer) evictStartSeenPods(now time.Time, timeout time.Duration) int {
	dropped := 0
	for pod, seen := range a.startSeenPods {
		if now.Sub(seen) > timeout {
			delete(a.startSeenPods, pod)
			dropped++
		}
	}
	return dropped
}

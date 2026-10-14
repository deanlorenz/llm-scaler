package slo

import (
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// minTransientGrace is the least time after a transient ends that its requests
// may still be completing into the router's window. The router records a
// request at end of stream, so a request queued through a wake finishes, and
// is recorded, up to its whole duration later; the grace grows with the
// requests the fleet is serving (transientGrace).
const minTransientGrace = 120 * time.Second

// startingMaxAge is how old a not-Ready Pod may be and still be starting. An
// older one was Ready and failed its probe: a flap, not a new replica, and not
// a reason to stop judging the fleet it belongs to.
const startingMaxAge = 30 * time.Minute

// transientMemo is one model's last transient cycle per objective scope, and
// when the model was last seen.
type transientMemo struct {
	// all is the last transient over every variant; generating the last over
	// the decode and aggregated variants alone.
	all, generating transientMark
	seenAt          time.Time
}

type transientMark struct {
	at     time.Time
	reason string
}

// starting counts a variant's replicas that are starting rather than flapping.
// PendingReplicas is discovery's status.replicas less ready, so it counts a
// Ready Pod that failed its probe as well as a new one; the Pods' ages tell
// them apart. Without the ages, pending less the Pods that will never become
// Ready is the estimate.
func starting(vs domain.VariantReplicaState) int {
	if vs.PendingAges == nil {
		return max(vs.PendingReplicas-vs.StuckReplicas, 0)
	}
	n := 0
	for _, age := range vs.PendingAges {
		if age >= 0 && age < startingMaxAge.Seconds() {
			n++
		}
	}
	return n
}

// transientOf names what makes this cycle transient over a set of variants, or
// "". The set is waking when none of it has a ready replica but some of it is
// on its way (CurrentReplicas is status.replicas, Pods not Ready included, so
// ready is Current - Pending). A set with nothing at all is parked, and a
// parked model is not analyzed; within a model, a variant at zero beside one
// that serves is not a wake. A replica starting anywhere in the set is a rise
// in progress, so a rise needs no reason of its own.
func transientOf(states []domain.VariantReplicaState, in func(domain.VariantReplicaState) bool) string {
	var ready, committed, starts int
	for _, vs := range states {
		if !in(vs) {
			continue
		}
		ready += max(vs.CurrentReplicas-vs.PendingReplicas, 0)
		committed += vs.CurrentReplicas + vs.PendingReplicas
		starts += starting(vs)
	}
	switch {
	case ready == 0 && committed > 0:
		return constants.SLOExcludedZeroReplicas
	case starts > 0:
		return constants.SLOExcludedStarting
	}
	return ""
}

// transientGrace is the grace after a transient: the router window clears the
// requests that began in it only once the longest of them has finished, so it
// is the longer of minTransientGrace and one and a half times the longest mean
// request duration among the model's ready replicas.
func transientGrace(rows []domain.ReplicaMetrics) time.Duration {
	longest := 0.0
	for _, rm := range rows {
		if rm.Ready && !rm.FromWarmPool && finite(rm.AvgServiceTime) && rm.AvgServiceTime > longest {
			longest = rm.AvgServiceTime
		}
	}
	return max(minTransientGrace, time.Duration(1.5*longest*float64(time.Second)))
}

// noteTransient records this cycle and returns, per objective, why the router's
// current window must not be judged ("" when it may). A window is judged only
// once the fleet has been steady for the whole of it and the grace after it: a
// wake from zero or a replica starting inside it would otherwise read as an SLO
// miss that no amount of steady-state capacity would have prevented.
//
// TTFT is excluded on a transient of any variant: a decode short of capacity
// back-pressures prefill, so it delays first tokens too. TPOT is excluded only
// on a transient of a variant that generates (decode or aggregated): a prefill
// replica starting does not change how fast tokens come out.
func (a *Analyzer) noteTransient(key string, states []domain.VariantReplicaState, window, grace time.Duration,
	now time.Time) (ttft, tpot string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.transients[key]
	if m == nil {
		m = &transientMemo{}
		a.transients[key] = m
	}
	m.seenAt = now
	if r := transientOf(states, func(domain.VariantReplicaState) bool { return true }); r != "" {
		m.all = transientMark{at: now, reason: r}
	}
	if r := transientOf(states, func(vs domain.VariantReplicaState) bool { return isGenerating(vs.Role) }); r != "" {
		m.generating = transientMark{at: now, reason: r}
	}
	held := func(t transientMark) string {
		if !t.at.IsZero() && now.Sub(t.at) < window+grace {
			return t.reason
		}
		return ""
	}
	return held(m.all), held(m.generating)
}

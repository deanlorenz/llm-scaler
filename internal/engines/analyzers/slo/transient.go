package slo

import (
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// transientGrace is how long after a transient ends its requests may still be
// completing into the router's window. A request queued through a wake or a
// rise finishes up to its whole decode time later, and the router records it
// then (it records at end of stream), so the window must also clear that.
const transientGrace = 120 * time.Second

// transientMemo is one model's recent fleet history: enough to tell whether
// the router's last window covered a wake, a start or a rise.
type transientMemo struct {
	// replicas is the model's committed replica count last cycle: current
	// plus pending, summed over its variants.
	replicas int
	// lastAt and lastReason are the most recent cycle that was transient, and
	// why. Zero when none has been seen.
	lastAt     time.Time
	lastReason string
	seenAt     time.Time
}

// transientNow names what makes this cycle transient for the model, or "".
// The order is the order of severity: a model with no replica at all is
// waking, which also starts a replica and raises the count.
func transientNow(states []domain.VariantReplicaState, prevReplicas int, hadPrev bool) (reason string, replicas int) {
	var current, starting int
	for _, vs := range states {
		current += vs.CurrentReplicas
		if s := vs.PendingReplicas - vs.StuckReplicas; s > 0 {
			starting += s
		}
		replicas += vs.CurrentReplicas + vs.PendingReplicas
	}
	switch {
	case current == 0:
		return constants.SLOExcludedZeroReplicas, replicas
	case starting > 0:
		return constants.SLOExcludedStarting, replicas
	case hadPrev && replicas > prevReplicas:
		return constants.SLOExcludedRise, replicas
	}
	return "", replicas
}

// noteTransient records this cycle and returns why the router's current window
// must not be judged, or "" when it may. A window is judged only once the
// fleet has been steady for the whole of it and the grace after it: a wake from
// zero, a replica starting or a rise inside it would otherwise read as an SLO
// miss that no amount of steady-state capacity would have prevented.
func (a *Analyzer) noteTransient(key string, states []domain.VariantReplicaState, window time.Duration, now time.Time) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	m, had := a.transients[key]
	if !had {
		m = &transientMemo{}
		a.transients[key] = m
	}
	reason, replicas := transientNow(states, m.replicas, had)
	m.replicas, m.seenAt = replicas, now
	if reason != "" {
		m.lastAt, m.lastReason = now, reason
	}
	if !m.lastAt.IsZero() && now.Sub(m.lastAt) < window+transientGrace {
		return m.lastReason
	}
	return ""
}

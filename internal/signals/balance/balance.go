// Package balance measures how evenly the router spreads one model's load over
// the ready replicas of each role, and remembers whether a role has been
// unevenly loaded for long enough to warn about it.
//
// It is a diagnostic. Nothing here feeds a scaling decision: the analyzers
// price capacity per replica and demand per fleet, and a router that loads one
// replica while its siblings idle is something the operator fixes in the
// router's scorer configuration, not something a replica count fixes. The
// figures come from the per-replica rows the analyzers already receive, so
// they cost no query.
package balance

import (
	"math"
	"sort"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// Thresholds that classify one replica. They are fractions of one engine's KV
// cache, read on the replica's busiest engine rank
// (domain.ReplicaMetrics.BusiestRankKvUsage), which for a pod of one rank is
// the pod's own utilization.
const (
	// SaturatedKvUsage is the KV utilization at or above which a replica is
	// saturated whatever its queue.
	SaturatedKvUsage = 0.90
	// IdleKvUsage is the KV utilization below which a replica with nothing
	// waiting is idle.
	IdleKvUsage = 0.30
	// SustainCycles is how many consecutive cycles a role must read
	// imbalanced before it is reported as imbalanced (Reading.Sustained), so
	// that one cycle's burst does not warn.
	SustainCycles = 2
)

// Distribution is one role's load spread over its ready replicas in one cycle.
//
// The KV figures are per replica, on the replica's busiest engine rank, and
// are the collector's one-minute peak: the same reading the saturation tests
// use. The queue figures are the replica's waiting requests (vLLM
// num_requests_waiting summed over its ranks). The engine's running count is
// not collected, so the in-flight spread is read from the waiting queue alone.
type Distribution struct {
	// Role is the canonical role: prefill, decode or both.
	Role string
	// Ready is how many ready replicas the figures are over. Warm-pool
	// bridges count: the router routes to them like any other replica.
	Ready int

	MaxKvUsage  float64
	MeanKvUsage float64
	MaxQueue    float64
	MeanQueue   float64

	// Saturated counts ready replicas at or over SaturatedKvUsage, or (prefill
	// and aggregated roles only) with a busiest-rank queue at or over the
	// policy's queueLengthThreshold. A decode replica's waiting count also
	// holds requests waiting for their remote KV, so decode is judged on KV
	// alone.
	Saturated int
	// Idle counts ready replicas under IdleKvUsage with nothing waiting.
	Idle int

	// BusiestPod and IdlePods name the replicas behind the verdict, for the
	// warning line.
	BusiestPod string
	IdlePods   []string
}

// Imbalanced reports whether, this cycle, at least one ready replica of the
// role is saturated while another is idle.
func (d Distribution) Imbalanced() bool {
	return d.Saturated > 0 && d.Idle > 0
}

// IdleWhileSaturated is the number of idle replicas when another replica of
// the role is saturated, and zero otherwise.
func (d Distribution) IdleWhileSaturated() int {
	if !d.Imbalanced() {
		return 0
	}
	return d.Idle
}

// KvMaxToMean is MaxKvUsage / MeanKvUsage. ok is false with fewer than two
// ready replicas or a mean of zero, where the ratio says nothing.
func (d Distribution) KvMaxToMean() (ratio float64, ok bool) {
	return maxToMean(d.MaxKvUsage, d.MeanKvUsage, d.Ready)
}

// QueueMaxToMean is MaxQueue / MeanQueue, with the same absence as KvMaxToMean.
// With N replicas it is at most N: every waiting request on one replica.
func (d Distribution) QueueMaxToMean() (ratio float64, ok bool) {
	return maxToMean(d.MaxQueue, d.MeanQueue, d.Ready)
}

func maxToMean(maxV, mean float64, n int) (float64, bool) {
	if n < 2 || !(mean > 0) {
		return 0, false
	}
	return maxV / mean, true
}

// Measure computes the distribution of every role that has a ready replica
// among rows. rolesByVariant maps a variant to its role as discovery recorded
// it; a variant it does not name is read as aggregated (domain.RoleBoth), as
// domain.CanonicalRole reads an empty role. queueThreshold is the policy's
// queueLengthThreshold; at or below zero no replica is saturated on its queue.
//
// A replica that is not Ready is left out: nothing routes to it, so its load
// says nothing about routing. A KV reading that is not a finite fraction is
// read as zero, and a negative queue as empty.
func Measure(rows []domain.ReplicaMetrics, rolesByVariant map[string]string, queueThreshold float64) map[string]Distribution {
	out := map[string]Distribution{}
	sums := map[string]struct{ kv, queue float64 }{}
	busiestKv := map[string]float64{}
	for _, rm := range rows {
		if !rm.Ready {
			continue
		}
		role := domain.CanonicalRole(rolesByVariant[rm.VariantName])
		kv := fraction(rm.BusiestRankKvUsage())
		queue := float64(max(rm.QueueLength, 0))
		d := out[role]
		d.Role = role
		d.Ready++
		d.MaxKvUsage = max(d.MaxKvUsage, kv)
		d.MaxQueue = max(d.MaxQueue, queue)
		s := sums[role]
		s.kv += kv
		s.queue += queue
		sums[role] = s
		if d.BusiestPod == "" || kv > busiestKv[role] {
			d.BusiestPod, busiestKv[role] = rm.PodName, kv
		}
		switch {
		case saturated(rm, role, kv, queueThreshold):
			d.Saturated++
		case kv < IdleKvUsage && rm.QueueLength <= 0:
			d.Idle++
			d.IdlePods = append(d.IdlePods, rm.PodName)
		}
		out[role] = d
	}
	for role, d := range out {
		d.MeanKvUsage = sums[role].kv / float64(d.Ready)
		d.MeanQueue = sums[role].queue / float64(d.Ready)
		sort.Strings(d.IdlePods)
		out[role] = d
	}
	return out
}

func saturated(rm domain.ReplicaMetrics, role string, kv, queueThreshold float64) bool {
	if kv >= SaturatedKvUsage {
		return true
	}
	if role == domain.RoleDecode || !(queueThreshold > 0) {
		return false
	}
	return float64(rm.BusiestRankQueue()) >= queueThreshold
}

func fraction(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return min(v, 1)
}

package slo

import (
	"math"
	"slices"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// TTFT, phase 2a: the inputs of the prefill curve, observed and published, and
// nothing priced from them. The proposal prices TTFT only once shadow data has
// shown the curve can be learned on real traffic; this is that shadow data.
// See docs/proposals/slo-tiers.md, "TTFT (phase 2)".

const (
	// ceilingMaxAge is how far back a variant's prefill ceiling looks.
	ceilingMaxAge = time.Hour
	// ceilingMaxSamples bounds the window: one sample per cycle, so an hour
	// at a 15-second cycle.
	ceilingMaxSamples = 240
	// ceilingMinSamples and ceilingMinSpan are what a ceiling is reported
	// from: enough cycles, spread over enough time, that it is not one burst.
	ceilingMinSamples = 10
	ceilingMinSpan    = 5 * time.Minute
	// ceilingQuantile is the share of saturated cycles at or below the
	// ceiling. A high quantile, not the maximum: a running max of noisy rates
	// is biased up, which would under-read utilization and over-price prefill.
	ceilingQuantile = 0.9
)

// ceilingSample is one cycle's prefill token rate, the median over the
// variant's replicas that were saturated on that cycle.
type ceilingSample struct {
	rate float64
	at   time.Time
}

// ceilingState is a variant's saturated readings and the last ceiling they
// gave. The last ceiling is kept when the window runs dry, so a fleet that has
// stopped queueing still has utilization measured against something: dropping
// it would leave the low-load half of the curve, the healthy half, unobserved.
type ceilingState struct {
	samples   []ceilingSample
	last      float64
	learnedAt time.Time
}

// ttftInputs is one variant's prefill-curve inputs this cycle. Any field is
// NaN when it could not be read.
type ttftInputs struct {
	prefillSeconds float64
	queueSeconds   float64
	utilization    float64
	ceiling        float64
}

// saturatedForCeiling reports whether a prefill replica's reading is one of
// the replica computing at its limit. Queued work alone is not enough: a P/D
// prefill replica holds its KV until decode pulls it, so a decode short of
// capacity fills prefill's cache, its queue grows and its compute sits idle --
// a reading of decode, not of prefill. So the replica's own KV must be below
// the range the engine is still working in.
func saturatedForCeiling(rm domain.ReplicaMetrics) bool {
	return rm.QueueLength > 0 && finite(rm.PrefillComputedTokenRate) && rm.PrefillComputedTokenRate > 0 &&
		finite(rm.KvUsageInstant) && rm.KvUsageInstant < itl.DefaultMaxObservableK
}

// observeTTFTInputs returns this cycle's inputs for one variant: the
// request-rate-weighted means of prefill time and engine queue time over its
// ready, non-pool rows and, for a prefill-role variant, its prefill utilization
// against the learned ceiling.
//
// The ceiling and utilization are a prefill role's only. The computed-token
// counter is observed when a request finishes, which on an aggregated replica
// is a whole generation after its prefill, and an aggregated replica's queue is
// mostly KV held by decodes; a "ceiling" there is neither prefill's capacity
// nor this cycle's.
func (a *Analyzer) observeTTFTInputs(key, variant, role string, rows []domain.ReplicaMetrics, now time.Time) ttftInputs {
	out := ttftInputs{prefillSeconds: math.NaN(), queueSeconds: math.NaN(),
		utilization: math.NaN(), ceiling: math.NaN()}
	var prefill, queue, wPrefill, wQueue, tokenRate float64
	var replicas int
	var saturated []float64
	for _, rm := range rows {
		if rm.VariantName != variant || !rm.Ready || rm.FromWarmPool {
			continue
		}
		replicas++
		w := 1.0
		if finite(rm.RequestRate) && rm.RequestRate > 0 {
			w = rm.RequestRate
		}
		if finite(rm.AvgPrefillTime) && rm.AvgPrefillTime > 0 {
			prefill += w * rm.AvgPrefillTime
			wPrefill += w
		}
		if finite(rm.AvgQueueTime) && rm.AvgQueueTime >= 0 && (rm.AvgQueueTime > 0 || rm.AvgPrefillTime > 0) {
			queue += w * rm.AvgQueueTime
			wQueue += w
		}
		if finite(rm.PrefillComputedTokenRate) && rm.PrefillComputedTokenRate > 0 {
			tokenRate += rm.PrefillComputedTokenRate
		}
		if saturatedForCeiling(rm) {
			saturated = append(saturated, rm.PrefillComputedTokenRate)
		}
	}
	if wPrefill > 0 {
		out.prefillSeconds = prefill / wPrefill
	}
	if wQueue > 0 {
		out.queueSeconds = queue / wQueue
	}
	if role != domain.RolePrefill {
		return out
	}

	a.mu.Lock()
	a.seen[key] = now
	st := a.ceilings[key]
	if st == nil {
		st = &ceilingState{last: math.NaN()}
		a.ceilings[key] = st
	}
	// One sample per cycle, however many replicas were saturated on it:
	// replicas of one cycle share one load state, and counting each would let
	// a single cycle of ten replicas satisfy the minimum on its own.
	if len(saturated) > 0 {
		st.samples = append(st.samples, ceilingSample{rate: quantileOf(saturated, 0.5), at: now})
	}
	st.samples = slices.DeleteFunc(st.samples, func(s ceilingSample) bool { return now.Sub(s.at) > ceilingMaxAge })
	if len(st.samples) > ceilingMaxSamples {
		st.samples = slices.Clone(st.samples[len(st.samples)-ceilingMaxSamples:])
	}
	if n := len(st.samples); n >= ceilingMinSamples && st.samples[n-1].at.Sub(st.samples[0].at) >= ceilingMinSpan {
		rates := make([]float64, n)
		for i, s := range st.samples {
			rates[i] = s.rate
		}
		st.last, st.learnedAt = quantileOf(rates, ceilingQuantile), now
	}
	out.ceiling = st.last
	a.mu.Unlock()

	if finite(out.ceiling) && out.ceiling > 0 && replicas > 0 {
		out.utilization = tokenRate / (float64(replicas) * out.ceiling)
	}
	return out
}

// quantileOf returns the q-quantile of values by linear interpolation between
// the order statistics. values is not modified.
func quantileOf(values []float64, q float64) float64 {
	s := slices.Clone(values)
	slices.Sort(s)
	if len(s) == 1 {
		return s[0]
	}
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	if lo >= len(s)-1 {
		return s[len(s)-1]
	}
	return s[lo] + (s[lo+1]-s[lo])*(pos-float64(lo))
}

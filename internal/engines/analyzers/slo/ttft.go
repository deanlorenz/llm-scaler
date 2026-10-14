package slo

import (
	"math"
	"slices"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// TTFT, phase 2a: the inputs of the prefill curve, observed and published, and
// nothing priced from them. The proposal prices TTFT only once shadow data has
// shown the curve can be learned on real traffic; this is that shadow data.
// See docs/proposals/slo-tiers.md, "TTFT (phase 2)".

const (
	// ceilingMaxSamples and ceilingMaxAge bound the readings a variant's
	// prefill ceiling is estimated from.
	ceilingMaxSamples = 120
	ceilingMaxAge     = time.Hour
	// ceilingMinSamples is the fewest readings the ceiling is reported from.
	ceilingMinSamples = 10
	// ceilingQuantile is the share of saturated readings at or below the
	// ceiling. A high quantile, not the maximum: a running max of noisy rates
	// is biased up, which would under-read utilization and over-price prefill.
	ceilingQuantile = 0.9
)

// ceilingSample is one replica's prefill token rate on a cycle its engine
// queue was non-empty: a reading of what it computes when it has more work
// than it can take.
type ceilingSample struct {
	rate float64
	at   time.Time
}

// ttftInputs is one variant's prefill-curve inputs this cycle. Any field is
// NaN when it could not be read.
type ttftInputs struct {
	prefillSeconds float64
	queueSeconds   float64
	utilization    float64
	ceiling        float64
}

// observeTTFTInputs records the variant's saturated readings into its ceiling
// window and returns this cycle's inputs: the request-rate-weighted means of
// prefill time and engine queue time over the variant's ready, non-pool rows,
// and its prefill utilization against the learned ceiling.
func (a *Analyzer) observeTTFTInputs(key, variant string, rows []domain.ReplicaMetrics, now time.Time) ttftInputs {
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
			if rm.QueueLength > 0 {
				saturated = append(saturated, rm.PrefillComputedTokenRate)
			}
		}
	}
	if wPrefill > 0 {
		out.prefillSeconds = prefill / wPrefill
	}
	if wQueue > 0 {
		out.queueSeconds = queue / wQueue
	}

	a.mu.Lock()
	a.seen[key] = now
	w := a.ceilings[key]
	for _, r := range saturated {
		w = append(w, ceilingSample{rate: r, at: now})
	}
	w = slices.DeleteFunc(w, func(s ceilingSample) bool { return now.Sub(s.at) > ceilingMaxAge })
	if len(w) > ceilingMaxSamples {
		w = w[len(w)-ceilingMaxSamples:]
	}
	a.ceilings[key] = w
	samples := make([]float64, len(w))
	for i, s := range w {
		samples[i] = s.rate
	}
	a.mu.Unlock()

	if len(samples) >= ceilingMinSamples {
		out.ceiling = quantileOf(samples, ceilingQuantile)
		if replicas > 0 && out.ceiling > 0 {
			out.utilization = tokenRate / (float64(replicas) * out.ceiling)
		}
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

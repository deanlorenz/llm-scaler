package whatif

import (
	"math"
	"slices"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
)

// PromptSpread is the coefficient of variation of the effective prompt, and
// how it was had: what the slo analyzer and the what-if CLI pass as
// Prefill.PromptCV.
type PromptSpread struct {
	CV float64
	// Assumed is no histogram read: CV is AssumedPromptCV. Clamped is a
	// reading past what the histogram can say: CV is PromptCVStop.
	Assumed, Clamped bool
}

// PoolComputedBuckets adds the computed-token histograms of rows' serving rows
// to pooled, each bound's rate summed, and returns it (a new map from nil). A
// bucket that is no reading adds nothing, so a row carrying no histogram
// leaves the pool as it was: absent, not empty mass.
func PoolComputedBuckets(pooled map[float64]float64, rows []domain.ReplicaMetrics) map[float64]float64 {
	if pooled == nil {
		pooled = map[float64]float64{}
	}
	for _, rm := range rows {
		if !prefill.Serving(rm) {
			continue
		}
		for _, b := range rm.ComputedTokensBuckets {
			if !math.IsNaN(b.UpperBound) && Finite(b.Rate) && b.Rate >= 0 {
				pooled[b.UpperBound] += b.Rate
			}
		}
	}
	return pooled
}

// ReadPromptSpread reads the spread from pooled bucket rates, computed once
// over the pool. mean is the exact E[I_eff].
//
// The second moment uses each bucket's upper bound, so cv reads high, never
// low: that costs replicas, not safety. Mass past the last finite bound has no
// upper bound, so cv is the top and Clamped, as is a cv above it. With no
// histogram, no request in it, or no mean it is AssumedPromptCV, Assumed.
func ReadPromptSpread(pooled map[float64]float64, mean float64) PromptSpread {
	assumed := PromptSpread{CV: AssumedPromptCV, Assumed: true}
	total, ok := pooled[math.Inf(1)]
	if !ok || !(total > 0) || !(mean > 0) {
		return assumed
	}
	bounds := make([]float64, 0, len(pooled))
	for b := range pooled {
		if !math.IsInf(b, 1) {
			bounds = append(bounds, b)
		}
	}
	slices.Sort(bounds)
	var second, below float64
	for _, u := range bounds {
		// Rates over a window can dip between bounds; a dip is no mass.
		n := max(pooled[u]-below, 0)
		second += n * u * u
		below = max(below, pooled[u])
	}
	if total > below {
		return PromptSpread{CV: PromptCVStop, Clamped: true}
	}
	cv := math.Sqrt(max(second/total/(mean*mean)-1, 0))
	if !Finite(cv) || cv > PromptCVStop {
		return PromptSpread{CV: PromptCVStop, Clamped: true}
	}
	return PromptSpread{CV: cv}
}

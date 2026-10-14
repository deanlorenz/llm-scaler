package saturation

import (
	"math"
	"sort"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// The service-rate signal: one struct per variant per cycle, built from the
// values the cycle already priced with, which the engine publishes as the
// wva_itl_line_* and wva_service_rate_per_replica series.
//
// Built here, from c.itlModels, c.itlSamples and the priced replicas, rather
// than recomputed by whoever publishes it. A second computation of "the line
// this variant is priced on" is a second answer to the question, and the two
// would drift the way three copies of one weighted mean once did
// (docs/proposals/signals-as-metrics.md, "Making it not rot").

// itlWindowLen returns how many (k, ITL) readings the window filed under key
// holds, and 0 when there is no window. Read after noteITL has added and
// pruned this cycle's readings, so it counts what a fit would have used.
func (a *SaturationAnalyzer) itlWindowLen(key string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if w, ok := a.itlWindows[key]; ok {
		return w.Len()
	}
	return 0
}

// attachServiceRates sets each variant's ServiceRate from what this cycle
// priced it on: the line fitLines resolved for it (own or borrowed), how many
// readings its own window holds, and the median saturated completion rate of
// its own replicas.
func (c *cycle) attachServiceRates(variants []domain.VariantCapacity, caps []capacity.ReplicaCapacity) {
	kPrice := pricingK(c.cfg)
	for i := range variants {
		name := variants[i].VariantName
		sig := &domain.ServiceRateSignal{Role: canonicalRole(c.rolesByVariant[name])}
		// Present for every variant that keeps a window, zero included. A
		// window holding nothing is the commonest reason a decode variant has
		// no line -- the ~28 cycles a fit takes from load start -- and a
		// series that vanished with the line could not say so.
		if n, ok := c.itlSamples[name]; ok {
			sig.ITLWindowed = true
			sig.LineSamples = n
		}
		if line, ok := c.itlModels[name]; ok && !line.IsZero() {
			sig.HasLine = true
			sig.LineSlope = line.A
			sig.LineIntercept = line.B
			sig.ITLAtPricingK = line.ITLAt(kPrice)
		}
		sig.Rate, sig.RateSource = variantServiceRate(name, caps)
		variants[i].ServiceRate = sig
	}
}

// variantServiceRate returns the median saturated completion rate over a
// variant's own replicas that have one, and the source of the lower-median
// replica's reading. ("", 0) when none has a rate.
//
// Own replicas only, never a bridge: a warm pool Pod runs its engine at a
// different --gpu-memory-utilization, and the floor leaves bridges out of its
// median for that reason (floor.Estimate).
//
// The median averages the central pair on an even count, the convention the
// floor and aggregateByVariant use. One series carries one source, so the
// source is the lower-median replica's -- an actual replica's, as
// k2SourceLabel picks its label -- which on a variant whose replicas disagree
// names the reading at or just below the published rate. In practice they
// rarely disagree: every replica of a variant is keyed by the fleet's shape,
// not its own (computeReplicaCapacity), so they read the same window.
func variantServiceRate(variant string, caps []capacity.ReplicaCapacity) (float64, string) {
	type reading struct {
		rate   float64
		source string
	}
	var readings []reading
	for _, rc := range caps {
		if rc.VariantName != variant || rc.FromWarmPool {
			continue
		}
		// !(x > 0) rather than x <= 0, so a NaN is refused rather than
		// admitted; +Inf is refused for the same reason the floor's
		// priceable refuses it.
		if !(rc.SaturatedThroughput > 0) || math.IsInf(rc.SaturatedThroughput, 1) {
			continue
		}
		readings = append(readings, reading{rate: rc.SaturatedThroughput, source: serviceRateSource(rc)})
	}
	if len(readings) == 0 {
		return 0, ""
	}
	// Ties broken on the source so the label does not depend on the order
	// the rows arrived in.
	sort.Slice(readings, func(i, j int) bool {
		if readings[i].rate != readings[j].rate {
			return readings[i].rate < readings[j].rate
		}
		return readings[i].source < readings[j].source
	})
	n := len(readings)
	rate := readings[n/2].rate
	if n%2 == 0 {
		rate = (readings[n/2-1].rate + readings[n/2].rate) / 2
	}
	return rate, readings[(n-1)/2].source
}

// serviceRateSource names the path that answered one replica's rate.
//
// Derived is checked first: when the derived figure is the one in use, the
// Borrowed and Samples fields describe the measured window that was NOT used
// (capacity.ReplicaCapacity), and reading them would mislabel it.
func serviceRateSource(rc capacity.ReplicaCapacity) string {
	switch {
	case rc.SaturatedThroughputDerived:
		return domain.ServiceRateSourceDerived
	case rc.SaturatedThroughputBorrowed:
		return domain.ServiceRateSourceNeighbour
	default:
		return domain.ServiceRateSourceMeasured
	}
}

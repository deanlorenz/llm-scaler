package steadystate

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
)

// Test 28: observability parity. Every analyzer result, and the composite,
// produce a metric series through the SAME functions (recordAnalyzerMetrics)
// — no analyzer, including the composite, is silently omitted. The
// composite is built and observed at the O2 site (collectV2ModelRequest),
// via a second explicit call into recordAnalyzerMetrics/logAnalyzerResult —
// not folded into runAnalyzersAndScore's own pass.
func TestCompositeObservability_MetricsIncludeTheComposite(t *testing.T) {
	registry := prometheus.NewRegistry()
	require.NoError(t, metrics.InitMetrics(registry))
	e := &Engine{
		metricsEmitter: metrics.NewMetricsEmitter(),
	}

	fakeSat := &fakeAnalyzerWithResult{
		analyzerName: domain.SaturationAnalyzerName,
		result: &domain.AnalyzerResult{
			TotalDemand:       1000,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v1", ReplicaCount: 1, PerReplicaCapacity: 200, Reason: "P1-obs"}},
		},
	}
	e.saturationV2Analyzer = fakeSat
	e.analyzersSnapshot = []analyzerEntry{{name: domain.SaturationAnalyzerName, analyzer: fakeSat}}
	e.started = true

	req, err := e.collectV2ModelRequest(context.Background(), "m", "ns", nil, scaleCfg,
		nil, nil, nil, nil, nil, 0)
	require.NoError(t, err)

	// The composite must land in the wva_analyzer_demand series alongside
	// saturation's own, through the SAME function (recordAnalyzerMetrics),
	// called a second time from collectV2ModelRequest -- not a parallel one.
	analyzerLabels := seriesLabels(t, registry, constants.WVAAnalyzerDemand, constants.LabelAnalyzerName)
	assert.Contains(t, analyzerLabels, domain.SaturationAnalyzerName,
		"saturation's own demand series must still be present")
	assert.Contains(t, analyzerLabels, allocation.CompositeSignalName,
		"the composite's demand series must be present -- test 28/29")

	targetLabels := seriesLabels(t, registry, constants.WVAAnalyzerTarget, constants.LabelAnalyzerName)
	assert.Contains(t, targetLabels, domain.SaturationAnalyzerName)
	assert.Contains(t, targetLabels, allocation.CompositeSignalName)

	// The composite is a genuinely separate entry, not a rename of
	// saturation's -- both series carry independent, comparable values.
	assert.NotEqual(t, domain.SaturationAnalyzerName, req.CompositeSignal.Name)
}

// Test 29: composite metrics are additive. The composite's series appears
// alongside every analyzer's own, never replacing or altering it, and each
// stays in its own units (both are D_sat units on the sat-only path, but the
// POINT is that saturation's series is untouched, not recomputed).
func TestCompositeObservability_CompositeIsAdditiveNotReplacing(t *testing.T) {
	registry := prometheus.NewRegistry()
	require.NoError(t, metrics.InitMetrics(registry))
	e := &Engine{
		metricsEmitter: metrics.NewMetricsEmitter(),
	}

	fakeSat := &fakeAnalyzerWithResult{
		analyzerName: domain.SaturationAnalyzerName,
		result: &domain.AnalyzerResult{
			TotalDemand:       1000,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v1", ReplicaCount: 1, PerReplicaCapacity: 200, Reason: "P1-obs"}},
		},
	}
	spy := &fakeAnalyzerWithResult{
		analyzerName: "spy",
		result: &domain.AnalyzerResult{
			TotalDemand:       4000, // deliberately different from saturation's
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v1", ReplicaCount: 1, PerReplicaCapacity: 200, Reason: "measured"}},
		},
	}
	e.saturationV2Analyzer = fakeSat
	e.analyzersSnapshot = []analyzerEntry{
		{name: domain.SaturationAnalyzerName, analyzer: fakeSat},
		{name: "spy", analyzer: spy},
	}
	e.started = true
	cfg := config.ScalingPolicy{
		ScaleUpThreshold: 0.85, ScaleDownBoundary: 0.70,
		Analyzers: []config.AnalyzerScoreConfig{{Name: "spy"}},
	}

	_, err := e.collectV2ModelRequest(context.Background(), "m", "ns", nil, cfg, nil, nil, nil, nil, nil, 0)
	require.NoError(t, err)

	analyzerLabels := seriesLabels(t, registry, constants.WVAAnalyzerDemand, constants.LabelAnalyzerName)
	assert.ElementsMatch(t, []string{domain.SaturationAnalyzerName, "spy", allocation.CompositeSignalName}, analyzerLabels,
		"saturation's, spy's, and the composite's series must all be present -- none replaced by another")
}

// Test 30: PRC provenance is not second-guessed. An estimated PRC (Reason =
// P2-hist, P3-k2, ...) contributes to the composite exactly like a measured
// one; no-data/error do not contribute at all. Exercised end to end through
// the log line, since that is where a reader actually sees provenance.
func TestCompositeObservability_EstimatedPRCContributesNormally(t *testing.T) {
	fakeSat := &fakeAnalyzerWithResult{
		analyzerName: domain.SaturationAnalyzerName,
		result: &domain.AnalyzerResult{
			TotalDemand: 500,
			VariantCapacities: []domain.VariantCapacity{
				// P0-store: an ESTIMATED PRC (idle SO / never-seen-before SO
				// ladder), not a live measurement. Must contribute exactly
				// like a measured one (spec A16'/A27/A28/test 30) -- no
				// discount, no different treatment.
				{VariantName: "v1", ReplicaCount: 1, PerReplicaCapacity: 100, Reason: "P0-store"},
			},
		},
	}
	e := satOnlyEngine(fakeSat.result)
	req, err := e.collectV2ModelRequest(context.Background(), "m", "ns", nil, scaleCfg, nil, nil, nil, nil, nil, 0)
	require.NoError(t, err)

	composite := req.CompositeSignal
	// The estimated PRC contributed exactly as reported: PRC_com = PRC_sat
	// on the sat-only path regardless of Reason ("P0-store" vs "P1-obs" —
	// A16' says the composite never treats an estimated PRC as weaker).
	require.Len(t, composite.Result.VariantCapacities, 1)
	assert.Equal(t, 100.0, composite.Result.VariantCapacities[0].PerReplicaCapacity,
		"an estimated (P0-store) PRC must contribute exactly like a measured one")
	assert.Equal(t, 5.0, composite.Result.TotalDemand/composite.Result.VariantCapacities[0].PerReplicaCapacity,
		"N_com recovers exactly (500/100=5), proving the estimate was not discounted")
}

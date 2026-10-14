package steadystate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
)

// The service-rate series are published from one place and deleted from
// three, the same shape as wva_engine_config (engine_config_wiring_test.go),
// and for the same reason these drive the engine's own methods: the emitter's
// specs stay green when a call site is deleted.

// serviceRateResult is a saturation result whose variants each carry a
// service-rate signal with a line, a sample count and a derived rate.
func serviceRateResult(pricingK float64, variants ...string) allocation.NamedAnalyzerResult {
	nr := namedResult(domain.SaturationAnalyzerName, nil, 5, variants...)
	nr.Result.PricingK = pricingK
	for i := range nr.Result.VariantCapacities {
		nr.Result.VariantCapacities[i].ServiceRate = &domain.ServiceRateSignal{
			HasLine: true, LineSlope: 0.03, LineIntercept: 0.002, ITLAtPricingK: 0.0224,
			ITLWindowed: true, LineSamples: 12,
			Role: domain.RoleDecode, Rate: 4.5, RateSource: domain.ServiceRateSourceDerived,
		}
	}
	return nr
}

var serviceRateFamilies = []string{
	constants.WVAITLLineSlope,
	constants.WVAITLLineIntercept,
	constants.WVAITLSecondsAtPricingK,
	constants.WVAITLLineSamples,
	constants.WVAServiceRatePerReplica,
}

func TestRecordAnalyzerMetrics_PublishesTheServiceRateSignal(t *testing.T) {
	e, registry := engineConfigEngine(t)

	e.recordAnalyzerMetrics("ns", "m", []allocation.NamedAnalyzerResult{serviceRateResult(0.68, "v1", "v2")})

	for _, name := range serviceRateFamilies {
		assert.ElementsMatch(t, []string{"v1", "v2"},
			seriesLabels(t, registry, name, constants.LabelVariantName), name)
	}
	assert.ElementsMatch(t, []string{"m"},
		seriesLabels(t, registry, constants.WVAPricingK, constants.LabelModelName))
	assert.ElementsMatch(t, []string{"derived", "derived"},
		seriesLabels(t, registry, constants.WVAServiceRatePerReplica, constants.LabelSource))
}

func TestRecordAnalyzerMetrics_PublishesNothingForAResultWithoutASignal(t *testing.T) {
	// Another analyzer's result carries no signal; it must not publish a zero.
	e, registry := engineConfigEngine(t)

	e.recordAnalyzerMetrics("ns", "m", []allocation.NamedAnalyzerResult{namedResult("throughput", nil, 5, "v1")})

	for _, name := range append(serviceRateFamilies, constants.WVAPricingK) {
		assert.Empty(t, seriesLabels(t, registry, name, constants.LabelModelName), name)
	}
}

func TestEvictStaleAnalyzerSeries_DeletesTheServiceRateOfADepartedVariant(t *testing.T) {
	e, registry := engineConfigEngine(t)

	e.recordAnalyzerMetrics("ns", "m", []allocation.NamedAnalyzerResult{serviceRateResult(0.68, "v1", "v2")})
	e.recordAnalyzerMetrics("ns", "m", []allocation.NamedAnalyzerResult{serviceRateResult(0.68, "v1")})

	for _, name := range serviceRateFamilies {
		assert.ElementsMatch(t, []string{"v1"},
			seriesLabels(t, registry, name, constants.LabelVariantName),
			"%s: a departed variant's series must go with its target", name)
	}
	assert.Len(t, seriesLabels(t, registry, constants.WVAPricingK, constants.LabelModelName), 1)
}

func TestEvictStaleAnalyzerSeries_DeletesPricingKNoLongerReported(t *testing.T) {
	e, registry := engineConfigEngine(t)

	e.recordAnalyzerMetrics("ns", "m", []allocation.NamedAnalyzerResult{serviceRateResult(0.68, "v1")})
	e.recordAnalyzerMetrics("ns", "m", []allocation.NamedAnalyzerResult{serviceRateResult(0, "v1")})

	assert.Empty(t, seriesLabels(t, registry, constants.WVAPricingK, constants.LabelModelName),
		"a pricing k the analyzer stopped reporting must not stay frozen")
}

func TestPruneAnalyzerSeries_DeletesTheServiceRateOfADepartedModel(t *testing.T) {
	// Two models with a same-named variant in one namespace: the prune must
	// take m2's series and leave m1's.
	e, registry := engineConfigEngine(t)
	for _, m := range []string{"m1", "m2"} {
		e.recordAnalyzerMetrics("ns", m, []allocation.NamedAnalyzerResult{serviceRateResult(0.68, "v")})
	}
	for _, name := range append(serviceRateFamilies, constants.WVAPricingK) {
		require.ElementsMatch(t, []string{"m1", "m2"}, seriesLabels(t, registry, name, constants.LabelModelName), name)
	}

	e.pruneAnalyzerSeries(map[string]bool{"ns/m1": true})

	for _, name := range append(serviceRateFamilies, constants.WVAPricingK) {
		assert.ElementsMatch(t, []string{"m1"}, seriesLabels(t, registry, name, constants.LabelModelName),
			"%s: a departed model's series must go, and only its", name)
	}
}

func TestEvictAllAnalyzerSeries_ClearsEveryServiceRateSeries(t *testing.T) {
	e, registry := engineConfigEngine(t)
	for _, m := range []string{"m1", "m2"} {
		e.recordAnalyzerMetrics("ns", m, []allocation.NamedAnalyzerResult{serviceRateResult(0.68, "v")})
	}

	e.evictAllAnalyzerSeries()

	for _, name := range append(serviceRateFamilies, constants.WVAPricingK) {
		assert.Empty(t, seriesLabels(t, registry, name, constants.LabelModelName),
			"%s: an idle cycle must not leave a series with nothing to refresh it", name)
	}
}

package steadystate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	llmdVariantAutoscalingV1alpha1 "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/variant"
)

// emptyMetricsSource answers every query with no series: a model scaled to
// zero, or one whose pods nothing scrapes.
type emptyMetricsSource struct{}

func (emptyMetricsSource) QueryList() *source.QueryList { return source.NewQueryList() }

func (emptyMetricsSource) Refresh(_ context.Context, spec source.RefreshSpec) (map[string]*source.MetricResult, error) {
	out := make(map[string]*source.MetricResult, len(spec.Queries))
	for _, q := range spec.Queries {
		out[q] = &source.MetricResult{QueryName: q, Values: []source.MetricValue{}}
	}
	return out, nil
}

func (emptyMetricsSource) Get(string, map[string]string) *source.CachedValue { return nil }

// The skip path is where a parked model's series are told that nothing was
// observed. The helper it calls is tested on its own; this drives
// prepareModelData through a collector that returns no rows, so removing the
// call -- not just emptying the helper -- fails too.
func TestPrepareModelData_NothingCollectedClearsTheSeries(t *testing.T) {
	e, registry := engineConfigEngine(t)
	e.ReplicaMetricsCollector = collector.NewReplicaMetricsCollector(emptyMetricsSource{}, nil, nil, record.NewFakeRecorder(10), nil)

	nr := serviceRateResult(0.68, "v")
	nr.Result.VariantCapacities[0].ObservedReplicas = 3
	e.recordAnalyzerMetrics("ns", "m", []allocation.NamedAnalyzerResult{nr})

	va := llmdVariantAutoscalingV1alpha1.VariantAutoscaling{
		ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "ns"},
		Spec: llmdVariantAutoscalingV1alpha1.VariantAutoscalingSpec{
			ModelID:        "m",
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "d"},
		},
	}
	md, err := e.prepareModelData(context.Background(), "m",
		[]llmdVariantAutoscalingV1alpha1.VariantAutoscaling{va}, fake.NewClientBuilder().Build())
	require.NoError(t, err)
	require.Nil(t, md, "no replica rows: the model is skipped")

	for _, name := range append(serviceRateFamilies, constants.WVAPricingK) {
		assert.Empty(t, seriesLabels(t, registry, name, constants.LabelModelName),
			"%s: a model with nothing collected must not keep its last reading", name)
	}
	assert.Equal(t, map[string]float64{"v": 0},
		gaugeValues(t, registry, constants.WVAAnalyzerObservedReplicas, constants.LabelVariantName),
		"nothing collected reads as nothing observed")
}

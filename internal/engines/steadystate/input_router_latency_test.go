package steadystate

import (
	"context"
	"math"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// routerAndPoolSource answers the router latency and flow-control pool
// queries for model "m"; every other query has no series.
type routerAndPoolSource struct{ emptyMetricsSource }

func (routerAndPoolSource) Refresh(_ context.Context, spec source.RefreshSpec) (map[string]*source.MetricResult, error) {
	model := map[string]string{"target_model_name": "m"}
	with := func(kv ...string) map[string]string {
		out := map[string]string{"target_model_name": "m", "inference_pool": "pool"}
		for i := 0; i+1 < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return out
	}
	answers := map[string][]source.MetricValue{
		registration.QuerySLORouterTTFTBuckets:      {{Labels: map[string]string{"target_model_name": "m", "le": "+Inf"}, Value: 2}},
		registration.QuerySLORouterTPOTSum:          {{Labels: model, Value: 0.1}},
		registration.QuerySLORouterTPOTCount:        {{Labels: model, Value: 2}},
		registration.QuerySLORouterTPOTBuckets:      {{Labels: map[string]string{"target_model_name": "m", "le": "+Inf"}, Value: 2}},
		registration.QueryFlowControlPoolModels:     {{Labels: with(), Value: 0}},
		registration.QueryFlowControlPoolSaturation: {{Labels: with("stage", "decode"), Value: 0.5}},
	}
	out := make(map[string]*source.MetricResult, len(spec.Queries))
	for _, q := range spec.Queries {
		out[q] = &source.MetricResult{QueryName: q, Values: answers[q]}
	}
	return out, nil
}

// The input builder collects the router latency and the flow-control pools
// every cycle, whatever the policy lists, so a learner recording carries
// them for the TTFT model (analyzer-learner.md, 2.9). An engine with no
// collector leaves both nil.
func TestInputBuilder_CollectsTheRouterLatencyAndPoolsEveryCycle(t *testing.T) {
	policy := config.ScalingPolicy{ScaleUpThreshold: 0.85, ScaleDownBoundary: 0.70}

	e := &Engine{analyzers: testAnalyzers(nil)}
	e.ReplicaMetricsCollector = collector.NewReplicaMetricsCollector(routerAndPoolSource{}, nil, nil, nil, nil)
	in := e.buildAnalyzerInput(context.Background(), "m", "ns", nil, policy, nil, nil, nil, nil, nil, 0)
	if in.RouterLatency == nil {
		t.Fatal("no router latency collected")
	}
	if got := in.RouterLatency.TPOTBuckets; len(got) != 1 || !math.IsInf(got[0].UpperBound, 1) || got[0].Rate != 2 {
		t.Errorf("TPOTBuckets = %v", got)
	}
	want := []domain.FlowControlPool{{WindowSeconds: registration.FlowControlWindowSeconds,
		DecodeSaturation: 0.5, DecodeSaturationKnown: true}}
	if len(in.FlowControlPools) != 1 || in.FlowControlPools[0].DecodeSaturation != want[0].DecodeSaturation ||
		!in.FlowControlPools[0].DecodeSaturationKnown || in.FlowControlPools[0].WaitBuckets != nil {
		t.Errorf("FlowControlPools = %+v, want %+v", in.FlowControlPools, want)
	}

	none := &Engine{analyzers: testAnalyzers(nil)}
	in = none.buildAnalyzerInput(context.Background(), "m", "ns", nil, policy, nil, nil, nil, nil, nil, 0)
	if in.RouterLatency != nil || in.FlowControlPools != nil {
		t.Errorf("no collector: router %v, pools %v; want nil", in.RouterLatency, in.FlowControlPools)
	}

	// A router that exports nothing is nil, not a zero latency.
	silent := &Engine{analyzers: testAnalyzers(nil)}
	silent.ReplicaMetricsCollector = collector.NewReplicaMetricsCollector(emptyMetricsSource{}, nil, nil, nil, nil)
	in = silent.buildAnalyzerInput(context.Background(), "m", "ns", nil, policy, nil, nil, nil, nil, nil, 0)
	if in.RouterLatency != nil || in.FlowControlPools != nil {
		t.Errorf("silent router: router %v, pools %v; want nil", in.RouterLatency, in.FlowControlPools)
	}
}

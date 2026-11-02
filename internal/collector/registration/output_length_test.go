package registration

import (
	"context"
	"strings"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source/prometheus"
)

// The output-length histogram is read per bucket bound, over the window the
// mean output length is read over, so the two are one reading (issue #276).
func TestOutputLengthQueries(t *testing.T) {
	registry := source.NewSourceRegistry()
	src := prometheus.NewPrometheusSource(context.Background(), &mockPrometheusAPI{}, prometheus.DefaultPrometheusSourceConfig())
	if err := registry.Register("prometheus", src); err != nil {
		t.Fatalf("Register: %v", err)
	}
	RegisterSaturationQueries(registry)
	queries := registry.Get("prometheus").QueryList()
	q := queries.Get(QueryOutputTokensBuckets)
	if q == nil {
		t.Fatalf("query %q is not registered", QueryOutputTokensBuckets)
	}
	mean := queries.Get(QueryAvgOutputTokens)
	if mean == nil {
		t.Fatalf("query %q is not registered", QueryAvgOutputTokens)
	}
	for _, want := range []string{"vllm:request_generation_tokens_bucket", "le)", "[5m]"} {
		if !strings.Contains(q.Template, want) {
			t.Errorf("%s = %q, want %s in it", QueryOutputTokensBuckets, q.Template, want)
		}
	}
	if !strings.Contains(mean.Template, "[5m]") {
		t.Errorf("%s = %q: the mean is no longer over [5m]; read the histogram over its window", QueryAvgOutputTokens, mean.Template)
	}
}

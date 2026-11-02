package registration

import (
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
)

// QueryOutputTokensBuckets is the query name for the output-length histogram
// per instance (the model_name, instance and pod labels), one series per
// bucket bound (le), as requests a second over the [5m] window
// QueryAvgOutputTokens is taken over, so the two are one reading.
//
// The mean alone prices every request at the same length, and the KV a
// resident request holds depends on the spread too: a long request stays
// resident longer (issue #276). The collector reduces the buckets and the
// mean to a lower bound on E[O^2]
// (domain.ReplicaMetrics.AvgOutputTokensSquared).
//
// vLLM only, and so registered once under its bare name: SGLang publishes its
// length histograms only behind --collect-tokens-histogram, under other
// names. On an SGLang fleet the query returns no series and
// AvgOutputTokensSquaredKnown stays unset.
// Source: vllm:request_generation_tokens_bucket (histogram)
const QueryOutputTokensBuckets = "output_tokens_buckets"

// registerOutputLengthQueries registers QueryOutputTokensBuckets for vLLM.
func registerOutputLengthQueries(registry *source.QueryList) {
	registry.MustRegister(source.QueryTemplate{
		Name:        QueryOutputTokensBuckets,
		Type:        source.QueryTypePromQL,
		Template:    `sum by (model_name, instance, pod, le) (rate(vllm:request_generation_tokens_bucket{namespace="{{.namespace}}"}[5m]))`,
		Params:      []string{source.ParamNamespace},
		Description: "Output-length histogram per pod (5m rate, one series per bucket), the spread of output lengths behind the KV a resident request holds",
	})
}

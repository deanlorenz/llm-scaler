package metrics

import (
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
)

// The start estimate is published under one source at a time: default, seed
// or measured. A move to another source deletes the others, so a query
// without a source selector reads one figure, never two added.
func TestReplicaStartSecondsEstimateKeepsOneSource(t *testing.T) {
	_, registry := engineConfigEmitter(t)
	series := func(source string) (float64, bool) {
		return seriesValue(t, registry, constants.WVAReplicaStartSecondsEstimate, map[string]string{
			constants.LabelVariantName: "v", constants.LabelStartSource: source,
		})
	}
	for _, step := range []struct {
		source  string
		seconds float64
	}{
		{ReplicaStartSourceDefault, 70},
		{ReplicaStartSourceSeed, 120},
		{ReplicaStartSourceMeasured, 233.3},
		{ReplicaStartSourceDefault, 70},
	} {
		SetReplicaStartSecondsEstimate("ns", "v", step.seconds, step.source)
		for _, s := range replicaStartSources {
			v, ok := series(s)
			if s == step.source {
				if !ok || v != step.seconds {
					t.Fatalf("after %s: %s series = %v (present %v), want %v", step.source, s, v, ok, step.seconds)
				}
				continue
			}
			if ok {
				t.Fatalf("after %s: the %s series is still published (%v)", step.source, s, v)
			}
		}
	}
}

package controller

import (
	"slices"
	"testing"

	"github.com/go-logr/logr"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
)

// YAML .inf and .nan in priority and in an analyzer's score, typed or under
// parameters, are cleared and reported, and the default entry keeps its
// limiters (#171).
func TestNonFinitePriorityAndScoreAreClearedAtLoad(t *testing.T) {
	data := map[string]string{
		"default": "priority: .inf\n" +
			"analyzers:\n" +
			"  - name: saturation\n    score: .nan\n" +
			"  - name: throughput\n    parameters:\n      score: .inf\n" +
			"limiters:\n  - type: quota\n    name: team-quota\n    scope: cluster\n    quotas: { H100: 8 }\n",
		"team-b": "model_id: m\nnamespace: team-b\npriority: .nan\n",
	}
	configs, count, rejected, ignored := parseScalingPolicyConfigRejects(data, logr.Discard())
	if count != 2 || len(rejected) != 0 {
		t.Fatalf("parsed %d entries, rejected %v; want both kept", count, rejected)
	}
	fields := make([]string, 0, len(ignored[config.GlobalDefaultsKey]))
	for _, f := range ignored[config.GlobalDefaultsKey] {
		fields = append(fields, f.Field)
	}
	if want := []string{"priority", "analyzers[0].score", "analyzers[1].score"}; !slices.Equal(fields, want) {
		t.Errorf("default entry ignored %v, want %v", fields, want)
	}
	if len(ignored["team-b"]) != 1 || ignored["team-b"][0].Field != "priority" {
		t.Errorf("team-b ignored %+v, want priority", ignored["team-b"])
	}
	def := configs[config.GlobalDefaultsKey]
	if len(def.Limiters) != 1 {
		t.Errorf("default entry limiters = %+v, want the team-quota limiter", def.Limiters)
	}
	got := config.ResolveScalingPolicyForTier(configs, "m", "team-b", "")
	if got.Priority != config.DefaultPriority || got.AnalyzerScore("saturation") != 1 || got.AnalyzerScore("throughput") != 1 {
		t.Errorf("resolved priority %v, scores %v/%v; want the defaults",
			got.Priority, got.AnalyzerScore("saturation"), got.AnalyzerScore("throughput"))
	}
}

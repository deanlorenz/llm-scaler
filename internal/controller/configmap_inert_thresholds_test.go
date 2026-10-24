package controller

import (
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
)

// A band set on an analyzer other than saturation is accepted but does not
// move the combined decision, so the read warns about it, naming the entry
// and the analyzer. Saturation's own override does decide, and is not
// warned about.
func TestParseWarnsAboutInertAnalyzerThresholds(t *testing.T) {
	var lines []string
	logger := funcr.New(func(prefix, args string) { lines = append(lines, args) }, funcr.Options{})
	data := map[string]string{
		"default": "analyzers:\n  - name: saturation\n    scaleUpThreshold: 0.8\n" +
			"  - name: throughput\n    scaleUpThreshold: 0.9\n",
		"clean": "analyzers:\n  - name: saturation\n    scaleDownBoundary: 0.6\n  - name: throughput\n",
	}
	configs, _, rejected, _ := parseScalingPolicyConfigRejects(data, logger)
	if len(rejected) != 0 || len(configs) != 2 {
		t.Fatalf("setup: both entries apply, rejected %v", rejected)
	}

	var warned []string
	for _, l := range lines {
		if strings.Contains(l, config.InertAnalyzerThresholdsMessage) {
			warned = append(warned, l)
		}
	}
	if len(warned) != 1 {
		t.Fatalf("want one warning, for the default entry's throughput; got %d: %v", len(warned), lines)
	}
	for _, want := range []string{`"key"="default"`, `"analyzer"="throughput"`} {
		if !strings.Contains(warned[0], want) {
			t.Errorf("warning %q lacks %s", warned[0], want)
		}
	}
}

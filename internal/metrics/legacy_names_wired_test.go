package metrics

import (
	"strings"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/prometheus/client_golang/prometheus"
)

// TestInitMetrics_RealMetricUnderBothNames goes through InitMetrics with a real
// metric. hack/rename rewrites this file, so after the rename the constant has
// the new prefix and the old name has to appear; before it there is nothing to
// publish.
func TestInitMetrics_RealMetricUnderBothNames(t *testing.T) {
	reg := prometheus.NewRegistry()
	if err := InitMetrics(reg); err != nil {
		t.Fatalf("InitMetrics: %v", err)
	}
	desiredReplicas.WithLabelValues("v", "ns", "accel").Set(3)

	got := gatherFamilies(t, reg)
	name := constants.WVADesiredReplicas
	if got[name] == nil {
		t.Fatalf("%s was not published", name)
	}
	suffix, renamed := strings.CutPrefix(name, currentMetricPrefix)
	if !renamed {
		return
	}
	legacy := got[legacyMetricPrefix+suffix]
	if legacy == nil {
		t.Fatalf("%s%s was not published", legacyMetricPrefix, suffix)
	}
	if v := legacy.GetMetric()[0].GetGauge().GetValue(); v != 3 {
		t.Errorf("old name reads %v, want 3", v)
	}
}

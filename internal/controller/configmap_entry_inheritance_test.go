package controller

import (
	"testing"

	"github.com/go-logr/logr"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
)

// A tier or a per-model override that does not mention kvCacheThreshold,
// queueLengthThreshold or priority must inherit them from the default entry.
// Defaulting every parsed entry to 0.80 / 5 / 1.0 made each one "set", so the
// merge always took the entry's built-in default over the operator's tuning.
func TestParsedEntriesInheritCapacityThresholdsAndPriority(t *testing.T) {
	data := map[string]string{
		"default": "kvCacheThreshold: 0.9\nqueueLengthThreshold: 8\npriority: 3\n" +
			"analyzers:\n  - name: saturation\n",
		"batch":     "scaleUpThreshold: 0.95\n",
		"one-model": "model_id: m\nnamespace: ns\nscaleDownBoundary: 0.5\n",
	}
	configs, count := parseScalingPolicyConfig(data, logr.Discard())
	if count != 3 {
		t.Fatalf("parsed %d entries, want 3", count)
	}

	for _, tc := range []struct {
		name         string
		model, tier  string
		wantUp       float64
		wantDownBand float64
	}{
		{name: "tier", model: "other", tier: "batch", wantUp: 0.95, wantDownBand: 0.70},
		{name: "override", model: "m", tier: "", wantUp: 0.85, wantDownBand: 0.5},
	} {
		got := config.ResolveScalingPolicyForTier(configs, tc.model, "ns", tc.tier)
		if got.KvCacheThreshold != 0.9 {
			t.Errorf("%s: kvCacheThreshold = %v, want the default's 0.9", tc.name, got.KvCacheThreshold)
		}
		if got.QueueLengthThreshold != 8 {
			t.Errorf("%s: queueLengthThreshold = %v, want the default's 8", tc.name, got.QueueLengthThreshold)
		}
		if got.Priority != 3 {
			t.Errorf("%s: priority = %v, want the default's 3", tc.name, got.Priority)
		}
		if got.ScaleUpThreshold != tc.wantUp || got.ScaleDownBoundary != tc.wantDownBand {
			t.Errorf("%s: band = (%v, %v), want (%v, %v)", tc.name,
				got.ScaleUpThreshold, got.ScaleDownBoundary, tc.wantUp, tc.wantDownBand)
		}
	}

	// With nothing set anywhere, the resolved policy still gets the built-in values.
	bare, _ := parseScalingPolicyConfig(map[string]string{"default": "scaleUpThreshold: 0.8\n"}, logr.Discard())
	got := config.ResolveScalingPolicyForTier(bare, "m", "ns", "")
	if got.KvCacheThreshold != config.DefaultKvCacheThreshold ||
		got.QueueLengthThreshold != config.DefaultQueueLengthThreshold ||
		got.Priority != config.DefaultPriority {
		t.Errorf("unset fields resolved to (%v, %v, %v), want the built-in defaults",
			got.KvCacheThreshold, got.QueueLengthThreshold, got.Priority)
	}
}

// A bad scaleToZero.retentionPeriod in the default entry must not drop the
// entry. Dropping it drops its limiters, which turns off GPU quota enforcement
// over a typo in an unrelated field.
func TestDefaultEntryWithBadRetentionKeepsLimiters(t *testing.T) {
	data := map[string]string{
		"default": "scaleToZero:\n  retentionPeriod: 5 minutes\n" +
			"shapeChangeHoldSeconds: -10\n" +
			"limiters:\n  - type: quota\n    name: team-quota\n    scope: cluster\n    quotas: { H100: 8 }\n",
	}
	configs, count, rejected := parseScalingPolicyConfigRejects(data, logr.Discard())
	if count != 1 || len(rejected) != 0 {
		t.Fatalf("parsed %d entries, rejected %v; want the default entry kept", count, rejected)
	}
	def := configs[config.GlobalDefaultsKey]
	if len(def.Limiters) != 1 || def.Limiters[0].Name != "team-quota" {
		t.Errorf("default entry limiters = %+v, want the team-quota limiter", def.Limiters)
	}
	if def.ScaleToZero == nil || def.ScaleToZero.RetentionPeriod != "" {
		t.Errorf("bad retentionPeriod was kept: %+v", def.ScaleToZero)
	}
	if def.ShapeChangeHoldSeconds != 0 {
		t.Errorf("negative shapeChangeHoldSeconds was kept: %d", def.ShapeChangeHoldSeconds)
	}
	if got := config.ResolveScaleToZeroRetention(&def); got != config.DefaultScaleToZeroRetentionPeriod {
		t.Errorf("retention = %v, want the built-in default %v", got, config.DefaultScaleToZeroRetentionPeriod)
	}
}

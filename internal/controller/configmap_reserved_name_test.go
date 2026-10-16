package controller

import (
	"strings"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
)

// reservedNameDefault is a default entry written before the limiter names
// were reserved: its one quota entry is named gpu-limiter.
const reservedNameDefault = `limiters:
  - type: quota
    name: gpu-limiter
    scope: cluster
    quotas: { H100: 8 }
`

// Review 183-3: a reserved quota entry name failed Validate, which dropped the
// whole default entry, and a controller starting on it refused to start
// unbounded, stalling the upgrade. For this release the entry loads, with a
// warning, from the controller's own ConfigMap and from a policy namespace.
func TestReservedLimiterNameAtStartupStillLoads(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policyNS bool
	}{
		{"the controller's own ConfigMap", false},
		{"a separate policy namespace", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRejectsFixture(t)
			ns := rejectsSystemNS
			if tc.policyNS {
				f.cfg.SetPolicyNamespace(rejectsPolicyNS, config.PolicySourceWellKnown)
				ns = rejectsPolicyNS
			}
			if err := f.bootstrapWith(policyCM(ns, reservedNameDefault)); err != nil {
				t.Fatalf("startup with a quota entry named gpu-limiter: %v", err)
			}
			if got := f.cfg.EffectiveLimiterSummary(); got != "quota/gpu-limiter" {
				t.Errorf("limiters in force %q, want quota/gpu-limiter", got)
			}
			warnings := f.cfg.EffectiveLimiterWarnings()
			if len(warnings) != 1 || !strings.Contains(warnings[0], `"gpu-limiter" is reserved`) ||
				!strings.Contains(warnings[0], "rejected in a later") {
				t.Errorf("limiter warnings %q, want one naming the reserved name and the later rejection", warnings)
			}
		})
	}
}

package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
)

// The limiter warnings describe the bound the cluster runs under, so they are
// logged only for the ConfigMap the limiters are read from. A namespace-local
// ConfigMap's limiters are ignored whole; a warning about them would describe
// a bound nothing enforces.
func TestLimiterWarningsOnlyWhereLimitersAreRead(t *testing.T) {
	// A namespace in exclude that nothing bounds: no cluster quota, and no
	// physical limiter.
	const excludeDefault = `limiters:
  - type: quota
    name: tenants
    scope: namespace
    exclude: [ns-x]
    namespaceQuotas: { team-a: { A100: 8 } }
`
	// A second physical entry, which is not built.
	const twoPhysical = `limiters:
  - type: gpu-inventory
    name: gpu
  - type: inventory
    name: gpu2
`
	for _, policy := range []struct {
		yaml string
		line string
	}{
		{excludeDefault, "exclude [ns-x] have no GPU limit"},
		{twoPhysical, config.IgnoredPhysicalLimitersMessage},
	} {
		for _, c := range []struct {
			name      string
			namespace string
			isGlobal  bool
			want      bool
		}{
			{"the global ConfigMap", rejectsSystemNS, true, true},
			{"a namespace-local ConfigMap", "team-a", false, false},
		} {
			t.Run(c.name, func(t *testing.T) {
				f := newRejectsFixture(t)
				var logged strings.Builder
				ctx := log.IntoContext(context.Background(), funcr.New(func(_, args string) {
					logged.WriteString(args + "\n")
				}, funcr.Options{}))
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Namespace: c.namespace, Name: config.ScalingPolicyConfigMapName()},
					Data:       map[string]string{config.GlobalDefaultsKey: policy.yaml},
				}
				if err := f.r.handleScalingPolicyConfigMap(ctx, cm, c.namespace, c.isGlobal); err != nil {
					t.Fatal(err)
				}
				out := logged.String()
				if got := strings.Contains(out, policy.line); got != c.want {
					t.Errorf("logged %q = %v, want %v:\n%s", policy.line, got, c.want, out)
				}
			})
		}
	}
}

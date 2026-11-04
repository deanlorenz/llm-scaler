package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
)

// WVA_PASSIVE is read only at start: an edit of the manager ConfigMap that
// changes it is warned about once, as restart required, and an edit that
// changes something else is not.
func TestPassiveChangeWarnsRestartRequired(t *testing.T) {
	t.Setenv("WVA_PASSIVE", "")
	cfg := config.NewTestConfig() // started with WVA_PASSIVE false
	rec := record.NewFakeRecorder(10)
	r := &ConfigMapReconciler{Config: cfg, Recorder: rec}
	cm := func(body string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "wva-manager-config", Namespace: "wva"},
			Data: map[string]string{"config.yaml": body}}
	}
	events := func() []string {
		var out []string
		for len(rec.Events) > 0 {
			out = append(out, <-rec.Events)
		}
		return out
	}
	r.warnIfPassiveChanged(context.Background(), cm("GLOBAL_OPT_INTERVAL: 30s\nWVA_PASSIVE: \"false\"\n"))
	if ev := events(); len(ev) != 0 {
		t.Fatalf("an edit that leaves WVA_PASSIVE as read warned: %v", ev)
	}
	r.warnIfPassiveChanged(context.Background(), cm("WVA_PASSIVE: \"true\"\n"))
	r.warnIfPassiveChanged(context.Background(), cm("WVA_PASSIVE: \"true\"\nGLOBAL_OPT_INTERVAL: 20s\n"))
	ev := events()
	if len(ev) != 1 || !strings.Contains(ev[0], constants.K8SEventPassiveRestartRequired) {
		t.Fatalf("events %v, want one %s", ev, constants.K8SEventPassiveRestartRequired)
	}
	// Back to the value read: nothing to warn, and a later change warns again.
	r.warnIfPassiveChanged(context.Background(), cm("WVA_PASSIVE: \"false\"\n"))
	r.warnIfPassiveChanged(context.Background(), cm("WVA_PASSIVE: true\n"))
	if ev := events(); len(ev) != 1 {
		t.Fatalf("events %v, want one more warning after the value changed again", ev)
	}
}

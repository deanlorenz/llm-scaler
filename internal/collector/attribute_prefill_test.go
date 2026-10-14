package collector

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/locator"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// The two halves of TTFT get TTFT's guards: a not-Ready pod's reading is
// dropped, and so is one longer than the pod has existed.
func TestAttributeInstance_PrefillAndQueueTimeGuards(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	now := time.Now()
	pod := func(ready bool, startedAgo time.Duration) *corev1.Pod {
		cond := corev1.ConditionFalse
		if ready {
			cond = corev1.ConditionTrue
		}
		started := metav1.NewTime(now.Add(-startedAgo))
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-abc", Namespace: "ns"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &started,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}}},
		}
	}
	collectorFor := func(p *corev1.Pod) *ReplicaMetricsCollector {
		loc := &mockLocator{
			locateFunc: func(context.Context, string, string) (*locator.ManagedScaler, error) {
				return &locator.ManagedScaler{Namespace: "ns", Name: "decode"}, nil
			},
			getPodLabelsFunc: func(context.Context, string, string) map[string]string { return map[string]string{} },
		}
		return NewReplicaMetricsCollector(nil, nil, fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).Build(), nil, loc)
	}
	read := func(t *testing.T, c *ReplicaMetricsCollector, prefill, queue float64) (float64, float64) {
		t.Helper()
		d := &podMetricData{podName: "pod-abc", vaName: "decode", hasKv: true, kvUsage: 0.5,
			avgPrefillTime: prefill, avgQueueTime: queue}
		m, _, ok := c.attributeInstance(ctx, "m", "ns", "pod-abc:8000", d, now, nil, map[string]map[string]int{})
		if !ok {
			t.Fatal("expected a row")
		}
		return m.AvgPrefillTime, m.AvgQueueTime
	}

	if p, q := read(t, collectorFor(pod(true, time.Hour)), 0.4, 0.1); p != 0.4 || q != 0.1 {
		t.Errorf("kept (%v, %v), want (0.4, 0.1) on a Ready, long-running pod", p, q)
	}
	if p, q := read(t, collectorFor(pod(false, time.Hour)), 0.4, 0.1); p != 0 || q != 0 {
		t.Errorf("not-Ready pod kept (%v, %v), want both dropped", p, q)
	}
	if p, q := read(t, collectorFor(pod(true, 2*time.Minute)), 300, 400); p != 0 || q != 0 {
		t.Errorf("readings longer than the pod's uptime kept (%v, %v)", p, q)
	}
}

// Engine instances of one pod are merged weighted by request rate, as TTFT is.
func TestCollapseToPods_PrefillAndQueueTimeWeightedByRequestRate(t *testing.T) {
	rows := []domain.ReplicaMetrics{
		{PodName: "p", ModelID: "m", RequestRate: 3, AvgPrefillTime: 1, AvgQueueTime: 0},
		{PodName: "p", ModelID: "m", RequestRate: 1, AvgPrefillTime: 5, AvgQueueTime: 4},
	}
	got := collapseToPods(rows)
	if len(got) != 1 {
		t.Fatalf("collapsed to %d rows, want 1", len(got))
	}
	if got[0].AvgPrefillTime != 2 || got[0].AvgQueueTime != 1 {
		t.Errorf("merged (%v, %v), want (2, 1)", got[0].AvgPrefillTime, got[0].AvgQueueTime)
	}
}

package fixtures

import (
	"context"
	"maps"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A pod whose namespace is being deleted holds its GPUs until the namespace
// controller deletes it, and then frees them: it counts as terminating before
// it has a deletion timestamp of its own, whichever of the namespace's
// deletion timestamp or Terminating phase says so. A finished pod there holds
// no GPU and does not count; a pod in a live namespace counts only by its own
// deletion timestamp.
func TestTerminatingPodsOnNodesCountsPodsOfADeletedNamespace(t *testing.T) {
	const key, product, node = "gpu.product", "MI300X", "worker"
	now := metav1.Now()
	pod := func(ns, name string, deleted bool, phase corev1.PodPhase) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: corev1.PodSpec{NodeName: node},
			Status: corev1.PodStatus{Phase: phase}}
		if deleted {
			p.DeletionTimestamp = &now
			p.Finalizers = []string{"test/hold"}
		}
		return p
	}
	c := fake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node, Labels: map[string]string{key: product}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "live"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "both", DeletionTimestamp: &now,
			Finalizers: []string{"kubernetes"}}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "timestamp-only", DeletionTimestamp: &now,
			Finalizers: []string{"kubernetes"}}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "phase-only"},
			Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating}},
		pod("live", "running", false, corev1.PodRunning),
		pod("live", "deleting", true, corev1.PodRunning),
		pod("both", "not-reached-yet", false, corev1.PodRunning),
		pod("both", "succeeded", false, corev1.PodSucceeded),
		pod("timestamp-only", "not-reached-yet", false, corev1.PodRunning),
		pod("phase-only", "not-reached-yet", false, corev1.PodRunning),
		pod("phase-only", "failed", false, corev1.PodFailed),
	)

	got, err := TerminatingPodsOnNodes(context.Background(), c, key, product)
	if err != nil {
		t.Fatalf("TerminatingPodsOnNodes: %v", err)
	}
	want := map[string]int{"live": 1, "both": 1, "timestamp-only": 1, "phase-only": 1}
	if !maps.Equal(got, want) {
		t.Errorf("terminating by namespace = %v, want %v", got, want)
	}
}

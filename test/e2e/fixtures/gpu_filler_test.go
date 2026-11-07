package fixtures

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A pod whose namespace is being deleted holds its GPUs until the namespace
// controller deletes it, and then frees them: it counts as terminating before
// it has a deletion timestamp of its own. A pod in a live namespace does not.
func TestTerminatingPodsOnNodesCountsPodsOfADeletedNamespace(t *testing.T) {
	const key, product, node = "gpu.product", "MI300X", "worker"
	now := metav1.Now()
	pod := func(ns, name string, deleted bool) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: corev1.PodSpec{NodeName: node}}
		if deleted {
			p.DeletionTimestamp = &now
			p.Finalizers = []string{"test/hold"}
		}
		return p
	}
	c := fake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node, Labels: map[string]string{key: product}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "live"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "earlier-spec", DeletionTimestamp: &now,
			Finalizers: []string{"kubernetes"}}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating}},
		pod("live", "running", false),
		pod("live", "deleting", true),
		pod("earlier-spec", "not-reached-yet", false),
	)

	n, err := TerminatingPodsOnNodes(context.Background(), c, key, product)
	if err != nil {
		t.Fatalf("TerminatingPodsOnNodes: %v", err)
	}
	if n != 2 {
		t.Errorf("terminating = %d, want 2: the pod with a deletion timestamp, and the one in the namespace being deleted", n)
	}
}

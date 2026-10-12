package saturation

import (
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// A role's busiest queue is its deepest per-pod waiting queue against the
// saturation queue: one pod queued deep among idle siblings reads busy,
// however calm the fleet. On a P/D decode a queue counts only while the pod's
// KV is full, since requests waiting for their remote KV sit in the same
// count; prefill's counts as it is.
func TestBusiestQueueLoadByRole(t *testing.T) {
	agg := map[string]string{"v": ""}
	rows := []domain.ReplicaMetrics{
		{PodName: "a", VariantName: "v", QueueLength: 0},
		{PodName: "b", VariantName: "v", QueueLength: 235},
		{PodName: "c", VariantName: "v", QueueLength: 1},
	}
	if got := busiestQueueLoadByRole(rows, agg, 5, 0.8)[domain.RoleBoth]; got != 47 {
		t.Errorf("busiest load %v, want 235/5 = 47 from the one deep pod", got)
	}
	if got := busiestQueueLoadByRole(rows, agg, 0, 0.8); got != nil {
		t.Errorf("with no saturation queue it reported %v", got)
	}

	pd := map[string]string{"d": domain.RoleDecode, "p": domain.RolePrefill}
	rows = []domain.ReplicaMetrics{
		// Decode, queued while its KV is half full: requests waiting for
		// their remote KV, not a busy pod.
		{PodName: "d1", VariantName: "d", QueueLength: 20, TokensInUse: 500, TotalKvCapacityTokens: 1000},
		{PodName: "p1", VariantName: "p", QueueLength: 4},
	}
	got := busiestQueueLoadByRole(rows, pd, 5, 0.8)
	if got[domain.RoleDecode] != 0 {
		t.Errorf("a P/D decode queued with its KV half full reads %v busy, want 0", got[domain.RoleDecode])
	}
	if got[domain.RolePrefill] != 0.8 {
		t.Errorf("prefill reads %v, want 4/5", got[domain.RolePrefill])
	}
	// Control: the same decode with its KV full is busy.
	rows[0].TokensInUse = 900
	if got := busiestQueueLoadByRole(rows, pd, 5, 0.8)[domain.RoleDecode]; got != 4 {
		t.Errorf("a P/D decode queued with its KV full reads %v, want 20/5", got)
	}
}

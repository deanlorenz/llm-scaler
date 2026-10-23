package balance

import (
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

func imbalanced() map[string]Distribution {
	return map[string]Distribution{domain.RolePrefill: {Role: domain.RolePrefill, Ready: 4, Saturated: 1, Idle: 3}}
}

func balanced() map[string]Distribution {
	return map[string]Distribution{domain.RolePrefill: {Role: domain.RolePrefill, Ready: 4}}
}

func only(t *testing.T, rs []Reading) Reading {
	t.Helper()
	if len(rs) != 1 {
		t.Fatalf("%d readings, want 1", len(rs))
	}
	return rs[0]
}

// One cycle's burst does not warn; the second consecutive cycle does, once;
// a balanced cycle clears it, once.
func TestTrackerWarnsOncePerChangeAfterTwoCycles(t *testing.T) {
	tr := NewTracker()
	steps := []struct {
		in                 map[string]Distribution
		sustained, changed bool
	}{
		{imbalanced(), false, false},
		{imbalanced(), true, true},
		{imbalanced(), true, false},
		{imbalanced(), true, false},
		{balanced(), false, true},
		{balanced(), false, false},
	}
	for i, s := range steps {
		r := only(t, tr.Observe("ns/m", s.in))
		if r.Sustained != s.sustained || r.Changed != s.changed {
			t.Errorf("cycle %d: sustained/changed = %v/%v, want %v/%v", i+1, r.Sustained, r.Changed, s.sustained, s.changed)
		}
	}
}

// An imbalanced cycle between balanced ones never reaches the threshold.
func TestTrackerResetsTheStreakOnABalancedCycle(t *testing.T) {
	tr := NewTracker()
	for i, in := range []map[string]Distribution{imbalanced(), balanced(), imbalanced(), balanced()} {
		if r := only(t, tr.Observe("ns/m", in)); r.Sustained || r.Changed {
			t.Errorf("cycle %d warned on an imbalance that never held two cycles", i+1)
		}
	}
}

// A role that parks and returns starts from zero, and so does a forgotten
// model; models are tracked apart.
func TestTrackerForgetsAbsentRolesAndModels(t *testing.T) {
	tr := NewTracker()
	tr.Observe("ns/m", imbalanced())
	tr.Observe("ns/m", imbalanced())
	if rs := tr.Observe("ns/m", nil); len(rs) != 0 {
		t.Fatalf("readings for a model with no ready replica: %v", rs)
	}
	if r := only(t, tr.Observe("ns/m", imbalanced())); r.Sustained {
		t.Error("a role back from zero kept its old streak")
	}
	tr.Observe("ns/other", imbalanced())
	tr.Retain(map[string]bool{"ns/other": true})
	if r := only(t, tr.Observe("ns/m", imbalanced())); r.Sustained {
		t.Error("Retain kept a model it was not given")
	}
	if r := only(t, tr.Observe("ns/other", imbalanced())); !r.Sustained {
		t.Error("Retain dropped a model it was given")
	}
	tr.Forget("ns/other")
	if r := only(t, tr.Observe("ns/other", imbalanced())); r.Sustained {
		t.Error("Forget kept the model's streak")
	}
}

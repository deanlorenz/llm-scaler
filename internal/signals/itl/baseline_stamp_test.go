package itl

import (
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learn"
)

// An intercept with no reading and no slope is as old as its stamp: the one a
// snapshot carried (RestoreBaselineAt), or unknown, and the stamp goes with
// the intercept on every path that drops it.
func TestBaselineStamp_ReportAndLifecycle(t *testing.T) {
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	report := func(tr *Tracker, key string) time.Time {
		_, stored := tr.Baseline(key)
		return tr.Report(learn.Report{}, key, Reading{BaselineStored: stored}).LastAdmitted
	}

	tr := NewTracker()
	tr.RestoreBaselineAt("k", 0.01, at)
	if got := report(tr, "k"); !got.Equal(at) {
		t.Fatalf("a restored intercept reports %v, want its stamp %v", got, at)
	}
	if got := tr.BaselinesAdmittedAt(); len(got) != 1 || !got["k"].Equal(at) {
		t.Fatalf("BaselinesAdmittedAt = %v", got)
	}
	tr.RestoreBaselineAt("k", 0.02, at.Add(time.Hour))
	if b, _ := tr.Baseline("k"); b != 0.01 || !report(tr, "k").Equal(at) {
		t.Fatalf("a second restore replaced the held intercept or its stamp: %v %v", b, report(tr, "k"))
	}

	tr.RestoreBaseline("unknown", 0.01)
	if got := report(tr, "unknown"); !got.IsZero() {
		t.Fatalf("an intercept restored with no stamp reports %v, want zero", got)
	}
	if _, ok := tr.BaselinesAdmittedAt()["unknown"]; ok {
		t.Fatal("an unknown age was exported as a stamp")
	}

	tr.SetBaseline("k", 0.03)
	if got := report(tr, "k"); !got.IsZero() {
		t.Fatalf("an intercept put back from a stash kept the old stamp: %v", got)
	}

	for name, drop := range map[string]func(*Tracker){
		"sweep":  func(tr *Tracker) { tr.Sweep(at, func(k string) bool { return k == "k" }) },
		"forget": func(tr *Tracker) { tr.ForgetKeys(at, learn.ByOperator, func(k string) bool { return k == "k" }) },
		"all":    func(tr *Tracker) { tr.ForgetBaselines() },
	} {
		tr := NewTracker()
		tr.RestoreBaselineAt("k", 0.01, at)
		drop(tr)
		if got := tr.BaselineAdmittedAt("k"); !got.IsZero() {
			t.Errorf("%s: the stamp outlived its intercept: %v", name, got)
		}
		tr.RestoreBaseline("k", 0.01)
		if got := report(tr, "k"); !got.IsZero() {
			t.Errorf("%s: a stamp came back with a new intercept of unknown age: %v", name, got)
		}
		if len(tr.baselineAt) != 0 {
			t.Errorf("%s: stamps left: %v", name, tr.baselineAt)
		}
	}
}

// A full fit from the readings the steady window carries past an empty main
// window prices a line with no reading in the window: its report reads the
// intercept's stamp, where it read zero.
func TestBaselineStamp_AFullFitPastAnEmptyWindow(t *testing.T) {
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	tr := NewTracker()
	tr.RestoreBaselineAt("k", 0.01, at)
	got := tr.Report(learn.Report{}, "k", Reading{Tier: TierOLS, BaselineStored: true, SteadyHeld: 10})
	if got.PricingKey != "k" || !got.LastAdmitted.Equal(at) {
		t.Errorf("a full fit with an empty window: %+v, want key k at %v", got, at)
	}
}

// A full fit stamps the intercept with the newest reading it was fitted from,
// not the cycle's clock: a refit from readings the steady window carries does
// not make it younger.
func TestBaselineStamp_NewestOf(t *testing.T) {
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	obs := []Observation{{Timestamp: at}, {Timestamp: at.Add(2 * time.Minute)}, {Timestamp: at.Add(time.Minute)}}
	if got := newestOf(obs, at.Add(time.Hour)); !got.Equal(at.Add(2 * time.Minute)) {
		t.Errorf("newestOf = %v", got)
	}
	if got := newestOf(nil, at); !got.Equal(at) {
		t.Errorf("newestOf(nil) = %v", got)
	}
}

package itl

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// aboveRows is n Ready replicas of variant v, Ready since an hour before
// start, each at k on the line 0.05 k + 0.010.
func aboveRows(n int, k float64, start time.Time) []domain.ReplicaMetrics {
	out := make([]domain.ReplicaMetrics, 0, n)
	for i := range n {
		out = append(out, domain.ReplicaMetrics{PodName: fmt.Sprintf("pod-%d", i), VariantName: "v", Ready: true,
			ReadySince: start.Add(-time.Hour), KvCacheUsage: k, KvUsageInstant: k, AvgITL: 0.05*k + 0.010})
	}
	return out
}

// With AboveBandFallback, a key that never fitted and whose replicas all sit
// above the band fits from those readings once there are DefaultMinSamples,
// and its A is not remembered as the slope a later hold holds. Without it,
// as before, the key has no line. The first fit in the band replaces it.
func TestTracker_AboveBandFallbackFitsAKeyThatNeverFitted(t *testing.T) {
	start := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	run := func(fallback bool) (first int, last Reading, tr *Tracker) {
		tr = NewTracker()
		adm := Admission{AboveBandFallback: fallback}
		first = -1
		for c := range 8 {
			last = tr.Observe("k", "v", aboveRows(3, 0.90, start), adm, start.Add(time.Duration(c)*15*time.Second))
			if last.Tier != "" && first < 0 {
				first = c
			}
		}
		return first, last, tr
	}
	first, last, tr := run(true)
	if first != 3 {
		t.Fatalf("the fallback's first line on cycle %d, want 3 (the tenth reading, three replicas a cycle)", first)
	}
	if !last.AboveBandFit || last.Tier != TierPinnedB || last.AboveBandHeld != min(24, DefaultWindowMaxSize) || last.Held != 0 {
		t.Fatalf("not the fallback's line: %+v", last)
	}
	// Pinned at DefaultBaselineSec: A = (0.055 - 0.006) / 0.90.
	if want := (0.05*0.90 + 0.010 - DefaultBaselineSec) / 0.90; math.Abs(last.Model.A-want) > 1e-9 {
		t.Errorf("A %g, want %g", last.Model.A, want)
	}
	if _, held := tr.Slopes()["k"]; held {
		t.Error("the fallback's A was remembered as the held slope")
	}
	if _, stored := tr.Baseline("k"); stored {
		t.Error("the fallback set the intercept")
	}

	if first, _, _ := run(false); first >= 0 {
		t.Errorf("negative control: without the fallback the key had a line on cycle %d", first)
	}

	// The band fits: its line replaces the fallback's at once.
	now := start.Add(8 * 15 * time.Second)
	adm := Admission{AboveBandFallback: true}
	var r Reading
	for c := range 4 {
		r = tr.Observe("k", "v", aboveRows(3, 0.40, start), adm, now.Add(time.Duration(c)*15*time.Second))
	}
	if r.AboveBandFit || r.Tier != TierPinnedB {
		t.Fatalf("the band's readings did not replace the fallback: %+v", r)
	}
}

// The fallback never replaces a line the band gives or a slope the key may
// hold: a key that fitted in the band and then went above it keeps fitting
// from the band's old readings until they age out, then holds the last A
// (SlopeHeld), and only once the hold ends (SlopeHoldFor) does the fallback
// price it.
func TestTracker_AboveBandFallbackNeverReplacesAHeldSlope(t *testing.T) {
	start := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	tr := NewTracker()
	adm := Admission{AboveBandFallback: true, SlopeHoldFor: 40 * time.Minute}
	now := start
	for range 6 {
		tr.Observe("k", "v", aboveRows(3, 0.40, start), adm, now)
		now = now.Add(15 * time.Second)
	}
	reasons := map[string]int{}
	var lastBandFit time.Time
	for now.Before(start.Add(80 * time.Minute)) {
		r := tr.Observe("k", "v", aboveRows(3, 0.90, start), adm, now)
		switch {
		case r.AboveBandFit:
			reasons["above"]++
			if lastBandFit.IsZero() || now.Sub(lastBandFit) <= adm.SlopeHoldFor {
				t.Fatalf("the fallback priced %s after the band's last fit, inside the %s hold", now.Sub(lastBandFit), adm.SlopeHoldFor)
			}
		case r.SlopeHeld:
			reasons["held"]++
		case r.Tier == "":
			reasons["none"]++
		default:
			reasons["band"]++
			lastBandFit = now
		}
		now = now.Add(15 * time.Second)
	}
	if reasons["band"] == 0 || reasons["held"] == 0 || reasons["above"] == 0 || reasons["none"] != 0 {
		t.Fatalf("want the band's fits, then the held slope, then the fallback, and never no line: %v", reasons)
	}
}

// ForgetSlope drops the key's held slope and keeps its windows and
// intercept: with it, a key whose window was cleared has no slope to hold.
func TestTracker_ForgetSlopeDropsOnlyTheSlope(t *testing.T) {
	start := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	tr := NewTracker()
	for c := range 6 {
		tr.Observe("k", "v", aboveRows(3, 0.40, start), Admission{}, start.Add(time.Duration(c)*15*time.Second))
	}
	tr.SetBaseline("k", 0.007)
	tr.ForgetSlope("k")
	if _, ok := tr.Slopes()["k"]; ok {
		t.Error("the slope was kept")
	}
	if n := tr.WindowLen("k"); n != 18 {
		t.Errorf("the window holds %d readings, want 18", n)
	}
	if b, ok := tr.Baseline("k"); !ok || b != 0.007 {
		t.Errorf("the intercept was dropped: %v %v", b, ok)
	}
}

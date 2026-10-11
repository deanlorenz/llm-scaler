package allocation

import (
	"math"
	"testing"
	"time"
)

// recordFor records needs every 30s from start for d, with peak at its middle.
func recordFor(l *ShareLedger, start time.Time, d time.Duration, needs, peak map[string]float64) time.Time {
	tm := spareTimings()
	now := start
	peakAt := (d / 2).Truncate(30 * time.Second)
	for at := time.Duration(0); at <= d; at += 30 * time.Second {
		now = start.Add(at)
		n := needs
		if at == peakAt {
			n = peak
		}
		l.RecordNeeds(n, now, tm)
	}
	return now
}

// A ledger rebuilt from a stored history knows the peaks the old one knew: the
// cap is not forced on for a window after a restart where the peaks fit.
func TestShareNeedHistoryRestoresKnownPeaks(t *testing.T) {
	tm := spareTimings()
	in := spareRun(2, 0.4, 7)
	old := NewShareLedger()
	t0 := time.Unix(1_000_000, 0)
	last := recordFor(old, t0, tm.SwingWindow+5*time.Minute,
		map[string]float64{"L": 2, "Q": 0.4}, map[string]float64{"L": 3, "Q": 0.4})
	h := old.NeedHistory(last, tm)

	restart := last.Add(90 * time.Second)
	if p := PlanShareTransfers(NewShareLedger(), in, restart, tm); !p.HeadroomCapped {
		t.Fatal("setup: a fresh ledger must cap (peaks unknown)")
	}
	l := NewShareLedger()
	if known := l.RestoreNeedHistory(h, restart, tm); known != 2 {
		t.Fatalf("restored the start of %d roles, want 2", known)
	}
	for _, r := range []string{"L", "Q"} {
		if !l.PeakKnown(r, restart, tm) {
			t.Fatalf("%s: peak not known after restoring a whole window of history", r)
		}
	}
	// The stored history keeps each minute's highest reading, and a peak
	// counts once two readings in a row reach it: the restored ledger knows
	// the peak the old one knew.
	if got, want := l.PeakNeed("L", 0), old.PeakNeed("L", 0); got != want {
		t.Fatalf("restored peak of L = %v, the old ledger's %v", got, want)
	}
	if p := PlanShareTransfers(l, in, restart, tm); p.HeadroomCapped {
		t.Fatalf("restored ledger capped on peak claims %d, though the restored peaks (3 + 0.4) fit", p.PeakClaimGPUs)
	}
}

// A stored sample that is not a usable need, is older than the window, or lies
// in the future is dropped on restore.
func TestShareNeedHistoryDropsInvalidSamples(t *testing.T) {
	tm := spareTimings()
	now := time.Unix(1_000_000, 0)
	h := ShareNeedHistory{"L": {Since: now.Add(-2 * tm.SwingWindow), Samples: []ShareNeedSample{
		{At: now.Add(-tm.SwingWindow - time.Minute), Need: 50},
		{At: now.Add(-3 * time.Minute), Need: math.NaN()},
		{At: now.Add(-2 * time.Minute), Need: math.Inf(1)},
		{At: now.Add(-90 * time.Second), Need: -4},
		{At: now.Add(-time.Minute), Need: 2.5},
		{At: now.Add(10 * time.Minute), Need: 40},
	}}, "gone": {Since: now.Add(-2 * tm.SwingWindow), Samples: []ShareNeedSample{
		{At: now.Add(-tm.SwingWindow - time.Second), Need: 9},
	}}}
	l := NewShareLedger()
	l.RestoreNeedHistory(h, now, tm)
	if !l.PeakKnown("L", now, tm) {
		t.Fatal("L: its Since was not restored")
	}
	if l.PeakKnown("gone", now, tm) || l.PeakNeed("gone", 0) != 0 {
		t.Fatal("a role with only expired samples was restored")
	}
	if got := l.NeedHistory(now, tm)["L"].Samples; len(got) != 1 || got[0].Need != 2.5 {
		t.Fatalf("stored again: %+v, want the one valid sample", got)
	}
}

// A history that ends long before now was not observed through the gap: its
// peaks still count, but the role's peak is not known until a window passes.
func TestShareNeedHistoryWithAGapIsNotKnown(t *testing.T) {
	tm := spareTimings()
	now := time.Unix(1_000_000, 0)
	lastSeen := now.Add(-ShareHistoryMaxGap - time.Minute)
	h := ShareNeedHistory{"L": {Since: now.Add(-2 * tm.SwingWindow), Samples: []ShareNeedSample{
		{At: lastSeen.Add(-time.Minute), Need: 6}, {At: lastSeen, Need: 6}}}}
	l := NewShareLedger()
	if known := l.RestoreNeedHistory(h, now, tm); known != 0 {
		t.Fatalf("restored the start of %d roles across a gap, want 0", known)
	}
	if l.PeakKnown("L", now, tm) {
		t.Fatal("peak taken as known across an unobserved gap")
	}
	if got := l.PeakNeed("L", 0); got != 6 {
		t.Fatalf("peak of L = %v, want the restored 6 kept", got)
	}
	// Recorded from now on, its peak is known a window later: the gap is
	// covered, not left unknown for good.
	at := now
	for ; at.Sub(now) <= tm.SwingWindow; at = at.Add(30 * time.Second) {
		l.RecordNeeds(map[string]float64{"L": 1}, at, tm)
	}
	if !l.PeakKnown("L", at, tm) {
		t.Fatal("a window after a gap, L's peak is still not known")
	}
}

// A stored history keeps one sample, the highest, per minute.
func TestShareNeedHistoryKeepsTheMaxPerMinute(t *testing.T) {
	tm := spareTimings()
	l := NewShareLedger()
	t0 := time.Unix(1_000_020, 0).Truncate(time.Minute)
	for i := range 60 { // 10 minutes at 10s
		need := 1.0
		if i%6 == 3 {
			need = float64(2 + i/6)
		}
		l.RecordNeeds(map[string]float64{"L": need}, t0.Add(time.Duration(i)*10*time.Second), tm)
	}
	now := t0.Add(10 * time.Minute)
	got := l.NeedHistory(now, tm)["L"]
	if len(got.Samples) != 10 {
		t.Fatalf("stored %d samples for 10 minutes, want 10", len(got.Samples))
	}
	for i, s := range got.Samples {
		if s.Need != float64(2+i) {
			t.Fatalf("minute %d stored %v, want its max %d", i, s.Need, 2+i)
		}
	}
	if !got.Since.Equal(t0) {
		t.Fatalf("since = %v, want %v", got.Since, t0)
	}
}

// Flushing forgets every peak: as after a restart with nothing stored.
func TestShareForgetNeedHistory(t *testing.T) {
	tm := spareTimings()
	l := NewShareLedger()
	knownFor(l, time.Unix(1000, 0), "L")
	l.RecordNeeds(map[string]float64{"L": 7}, time.Unix(1000, 0), tm)
	l.ForgetNeedHistory()
	if l.PeakKnown("L", time.Unix(1000, 0), tm) || l.PeakNeed("L", 0) != 0 {
		t.Fatal("history survived ForgetNeedHistory")
	}
}

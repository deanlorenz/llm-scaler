package allocation

import (
	"testing"
	"time"
)

// firstReturn plans n cycles 15s apart from a ledger with no peak known and
// returns the index of the first cycle that started a return, or -1. Q holds
// 7 at a need of 0.4 beside L at 2: counted at their ceilings (8 + 8 of a
// quota of 10) the cap is on, though nothing recorded says the quota is short.
func firstReturn(t *testing.T, l *ShareLedger, tm ShareTimings, n int) int {
	t.Helper()
	in := spareRun(2, 0.4, 7)
	for i := range n {
		p := PlanShareTransfers(l, in, time.Unix(int64(1000+15*i), 0), tm)
		if !p.HeadroomCapped || len(p.UnknownPeaks) == 0 {
			t.Fatalf("cycle %d: capped %v, unknown peaks %v; the premise is a cap on unknown peaks",
				i+1, p.HeadroomCapped, p.UnknownPeaks)
		}
		if p.Returns > 0 {
			return i
		}
	}
	return -1
}

// While the cap is on only because no peak is known, and the needs recorded
// so far fit the quota together, a return waits its role's scale-down window
// as well as the confirm cycles: a long-drain model's dip that outlasts the
// confirm cycles is not returned and cold-started again as it comes back.
func TestShareReturnWaitsItsWindowWhileContentionIsAssumed(t *testing.T) {
	tm := spareTimings()
	tm.Window = 60 * time.Second
	got := firstReturn(t, NewShareLedger(), tm, 8)
	// Over from cycle 1 (t=1000); its window has passed at t=1060, cycle 5.
	if got != 4 {
		t.Fatalf("first return on cycle %d, want 5 (after the 60s window, not the confirm cycles)", got+1)
	}
}

// Once the needs recorded already exceed the quota together, the contention
// is seen, not assumed: a return waits the confirm cycles only, though no
// peak is known yet.
func TestShareReturnSkipsTheWindowOnceContentionIsSeen(t *testing.T) {
	tm := spareTimings()
	tm.Window = 60 * time.Second
	l := NewShareLedger()
	// Both burst to 8 (two readings each, so the peaks count), 16 of 10.
	for _, at := range []int64{900, 915} {
		l.RecordNeeds(map[string]float64{"L": 8, "Q": 8}, time.Unix(at, 0), tm)
	}
	if got := firstReturn(t, l, tm, 8); got != 1 {
		t.Fatalf("first return on cycle %d, want 2 (the confirm cycles)", got+1)
	}
}

// OverFor is how long a role has been over its target, and starts again when
// it is not.
func TestShareOverForStartsAgain(t *testing.T) {
	l := NewShareLedger()
	t0 := time.Unix(1000, 0)
	l.MarkOver(map[string]bool{"Q": true}, t0)
	l.MarkOver(map[string]bool{"Q": true}, t0.Add(15*time.Second))
	if got := l.OverFor("Q", t0.Add(30*time.Second)); got != 30*time.Second {
		t.Fatalf("over for %v, want 30s", got)
	}
	l.MarkOver(map[string]bool{"Q": false}, t0.Add(45*time.Second))
	if got := l.OverFor("Q", t0.Add(45*time.Second)); got != 0 {
		t.Fatalf("over for %v after a cycle not over, want 0", got)
	}
	l.MarkOver(map[string]bool{"Q": true}, t0.Add(60*time.Second))
	if got := l.OverFor("Q", t0.Add(75*time.Second)); got != 15*time.Second {
		t.Fatalf("over for %v, want 15s from when it was over again", got)
	}
}

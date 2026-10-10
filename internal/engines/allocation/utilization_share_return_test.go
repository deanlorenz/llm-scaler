package allocation

import (
	"math"
	"testing"
	"time"
)

// returnsFromQ runs spareRun(lNeed, 0.4, 7) -- Q above its target of 2 -- for
// four cycles after mut, and counts the replicas Q returned.
func returnsFromQ(lNeed float64, mut func(in *SharePlanInput, l *ShareLedger)) int {
	in := spareRun(lNeed, 0.4, 7)
	l := NewShareLedger()
	if mut != nil {
		mut(&in, l)
	}
	n := 0
	for _, p := range planEach(l, in, 4) {
		for _, tr := range p.Started {
			if tr.Returned && tr.Donor == "Q" {
				n++
			}
		}
	}
	return n
}

// What holds a return back, and what does not.
func TestShareReturnGuards(t *testing.T) {
	if n := returnsFromQ(2, nil); n != 5 {
		t.Fatalf("control: Q returned %d, want 5", n)
	}
	for _, c := range []struct {
		name  string
		lNeed float64
		mut   func(*SharePlanInput, *ShareLedger)
		want  int
	}{
		{"Q backing off", 2, func(_ *SharePlanInput, l *ShareLedger) { l.giveAfter["Q"] = time.Unix(1e9, 0) }, 0},
		{"Q received within the reversal hold", 2, func(_ *SharePlanInput, l *ShareLedger) {
			l.recordMove("Q", true, "x", time.Unix(990, 0), spareTimings())
		}, 0},
		{"Q has no variant to give", 2, func(in *SharePlanInput, _ *ShareLedger) {
			in.Give = map[string]ShareVariant{"L": {Name: "l", GPUs: 1}}
		}, 0},
		{"a short role that cannot grow does not block", 5, func(in *SharePlanInput, _ *ShareLedger) {
			in.Give = map[string]ShareVariant{"Q": {Name: "q", GPUs: 1}}
			in.Grow = map[string]ShareVariant{"Q": {Name: "q", GPUs: 1}}
		}, 5},
		{"a short role held from filling does not block", 5, func(_ *SharePlanInput, l *ShareLedger) {
			l.HoldFill("L", time.Unix(1e9, 0))
		}, 5},
		{"transfers in flight do not hold a return", 0.4, func(in *SharePlanInput, l *ShareLedger) {
			for range ShareMaxConcurrentTransfers {
				l.Start(ShareTransfer{Donor: "L", DonorGPUs: 1, Entitled: true}, in.Held, time.Unix(990, 0), spareTimings())
			}
		}, 5},
	} {
		if got := returnsFromQ(c.lNeed, c.mut); got != c.want {
			t.Errorf("%s: Q returned %d, want %d", c.name, got, c.want)
		}
	}
}

// Only a return is cancelled when its donor turns short: a reserve refill is
// entitled, and a set contributor goes only with its primary.
func TestShareCancelsOnlyReturns(t *testing.T) {
	l := NewShareLedger()
	in := spareRun(2, 0.4, 7)
	l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 1, Entitled: true}, in.Held, time.Unix(1000, 0), spareTimings())
	p := PlanShareTransfers(l, spareRun(2, 8, 7), time.Unix(1015, 0), spareTimings())
	if len(p.Cancelled) != 0 {
		t.Fatalf("cancelled %v: a refill is not a return", p.Cancelled)
	}
}

// GPUs a return has not released are not idle: the donor's pod still runs on
// them, and the quota still counts it.
func TestShareUnreleasedIsNotIdle(t *testing.T) {
	l := NewShareLedger()
	held := map[string]int{"Q": 7}
	tm := spareTimings()
	l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 1, Entitled: true, Returned: true}, held, time.Unix(1000, 0), tm)
	l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 1, Entitled: true}, held, time.Unix(1000, 0), tm)
	if n := l.Unreleased(); n != 2 {
		t.Fatalf("unreleased %d, want 2 (a return and a refill)", n)
	}
	in := SharePlanInput{Budget: 10, PhysicalFree: math.MaxInt}
	if idle := shareIdle(l, in, l.Committed(map[string]int{"L": 3, "Q": 7})); idle != 0 {
		t.Fatalf("idle %d while both still release, want 0", idle)
	}
	held["Q"] = 5
	l.Observe(held, time.Unix(1015, 0), tm)
	if n := l.Unreleased(); n != 0 {
		t.Fatalf("unreleased %d after both released, want 0", n)
	}
}

// Transfers of one donor that abort together back it off once, not once each:
// five starts of one hard imbalance must not reach the 16x back-off at once.
func TestShareAbortsOfOneCycleBackOffOnce(t *testing.T) {
	l := NewShareLedger()
	tm := spareTimings()
	held := map[string]int{"L": 3, "Q": 7}
	for range 5 {
		tr := l.Start(ShareTransfer{Donor: "Q", Receiver: "L", GPUs: 1, DonorGPUs: 1}, held, time.Unix(1000, 0), tm)
		l.ConfirmStarted(tr.ID, nil)
	}
	at := time.Unix(1000, 0).Add(tm.ReleaseTimeout)
	l.Observe(held, at, tm)
	if l.aborts["Q"] != 1 {
		t.Fatalf("aborts %d, want 1", l.aborts["Q"])
	}
	if got := l.GiveAfter("Q"); !got.Equal(at.Add(tm.ReleaseTimeout)) {
		t.Fatalf("back-off until %v, want one release timeout (%v)", got, at.Add(tm.ReleaseTimeout))
	}
}

// An unpaced hard imbalance goes on to the next donor when the first reaches
// its target: L, short of 7, is funded by both Q and C within two cycles.
func TestShareHardImbalanceUsesEveryCalmDonor(t *testing.T) {
	roles := ShareCapSpare([]ShareRole{
		{Key: "L", Weight: 1, Need: 11, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
		{Key: "Q", Weight: 1, Need: 0.4, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
		{Key: "C", Weight: 1, Need: 0.4, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
	}, 1)
	in := SharePlanInput{Roles: roles, Held: map[string]int{"L": 1, "Q": 4, "C": 5},
		Thresholds: map[string]float64{"L": 0.85, "Q": 0.85, "C": 0.85}, Budget: 10, Tolerance: 0.15,
		PhysicalFree: math.MaxInt, Headroom: ShareHeadroom{Capped: true, Replicas: 1}}
	from := map[string]int{}
	for _, p := range planEach(NewShareLedger(), in, 2) {
		for _, tr := range p.Started {
			if tr.Receiver == "L" {
				from[tr.Donor]++
			}
		}
	}
	if from["Q"] == 0 || from["C"] == 0 || from["Q"]+from["C"] != 7 {
		t.Fatalf("L funded %v, want 7 replicas from both Q and C", from)
	}
}

// A multi-pod receiver funded by donor sets is unpaced too: once its hard
// imbalance is confirmed, it takes set after set in one cycle.
func TestShareHardImbalanceUnpacesDonorSets(t *testing.T) {
	roles := ShareCapSpare([]ShareRole{
		{Key: "B", Weight: 1, Need: 10, Floor: 2, Ceiling: 10, ReplicaGPUs: 2},
		{Key: "A", Weight: 1, Need: 0.4, Floor: 1, Ceiling: 10, ReplicaGPUs: 1},
	}, 1)
	in := SharePlanInput{Roles: roles, Held: map[string]int{"B": 2, "A": 8},
		Thresholds: map[string]float64{"A": 0.85, "B": 0.85}, Budget: 10, Tolerance: 0.15,
		Give:         map[string]ShareVariant{"A": {Name: "a", GPUs: 1, PodGPUs: []int{1}}},
		Grow:         map[string]ShareVariant{"B": {Name: "b", GPUs: 2, PodGPUs: []int{1, 1}}},
		PhysicalFree: math.MaxInt, Headroom: ShareHeadroom{Capped: true, Replicas: 1}}
	// Two ordinary moves fill the concurrency limit; an unpaced set does not
	// count it.
	l := NewShareLedger()
	for range ShareMaxConcurrentTransfers {
		l.Start(ShareTransfer{Donor: "A", DonorGPUs: 1, Entitled: true}, in.Held, time.Unix(900, 0), spareTimings())
	}
	plans := planEach(l, in, 2)
	sets := func(p SharePlan) int {
		n := 0
		for _, tr := range p.Started {
			if tr.Receiver == "B" {
				n++
			}
		}
		return n
	}
	if n := sets(plans[0]); n > 1 {
		t.Fatalf("first cycle funded %d sets, want at most 1: unconfirmed, it is paced", n)
	}
	if n := sets(plans[1]); n < 2 {
		t.Fatalf("second cycle funded %d sets, want the rest of the shortfall (2 or more)", n)
	}
	for _, p := range plans {
		for _, tr := range p.Started {
			if !tr.SkippedWaits {
				t.Fatal("a hard-imbalance set member did not skip the waits")
			}
		}
	}
}

// The idle fill's credit is node-aware: idle GPUs split 4 + 4 across nodes
// cannot place B's 8-GPU pod, so B is funded by a donor set rather than left
// to a fill that cannot land -- and A, above its target, does not return what
// B needs.
func TestShareIdleCreditNeedsAFit(t *testing.T) {
	plan := func(free map[string]int) []ShareTransfer {
		nodes := map[string]ShareNode{}
		for n, f := range free {
			nodes[n] = ShareNode{Free: f}
		}
		four := func(name string) ShareUnit { return ShareUnit{Pods: []SharePod{{Name: name, Node: "n1", GPUs: 4}}} }
		in := SharePlanInput{
			Roles: []ShareRole{
				{Key: "A", Weight: 1, Need: 4, Ceiling: 16, ReplicaGPUs: 4},
				{Key: "B", Weight: 1, Need: 64, Ceiling: 24, ReplicaGPUs: 8},
			},
			Held: map[string]int{"A": 24, "B": 16}, Thresholds: map[string]float64{"A": 0.8, "B": 0.8},
			Budget: 48, Tolerance: 0.15,
			Give:  map[string]ShareVariant{"A": {Name: "a", GPUs: 4, PodGPUs: []int{4}}},
			Grow:  map[string]ShareVariant{"B": {Name: "b", GPUs: 8, PodGPUs: []int{8}}},
			Nodes: nodes, DonorUnits: map[string][]ShareUnit{"A": {four("ns/a-0"), four("ns/a-1")}},
			PhysicalFree: math.MaxInt,
		}
		started := make([]ShareTransfer, 0, 4)
		for _, p := range planEach(NewShareLedger(), in, 3) {
			started = append(started, p.Started...)
		}
		return started
	}
	got := plan(map[string]int{"n1": 4, "n2": 4})
	funded, returned := false, false
	for _, tr := range got {
		funded = funded || tr.Receiver == "B"
		returned = returned || tr.Returned
	}
	if !funded || returned {
		t.Fatalf("fragmented idle: B funded %v (want true), A returned %v (want false): %v", funded, returned, got)
	}
	// Control: 8 free on one node places B's pod, and the fill funds it.
	for _, tr := range plan(map[string]int{"n1": 8}) {
		if tr.Receiver == "B" {
			t.Fatalf("a donor funded B though its pod fits free GPUs: %+v", tr)
		}
	}
}

// The unpaced path takes a receiver to its need, not to its headroom: L needs
// 5 and holds 3 (target 6). The first cycle moves one at the pace; the second,
// unpaced, takes L to its need of 5 -- the replica of headroom above it waits
// for an ordinary, paced move.
func TestShareUnpacedStopsAtNeed(t *testing.T) {
	moved := 0
	for _, p := range planEach(NewShareLedger(), spareRun(5, 0.4, 7), 2) {
		moved += qToL(p.Started)
	}
	if moved != 2 {
		t.Fatalf("Q -> L moved %d over two cycles, want 2 (3 to its need of 5)", moved)
	}
}

// An ordinary move takes one replica per donor per cycle.
func TestShareOrdinaryMoveIsOnePerDonor(t *testing.T) {
	in := spareRun(11, 0.4, 7)
	in.SkipWaits = ShareSkipWaits{Off: true}
	for i, p := range planEach(NewShareLedger(), in, 3) {
		if n := qToL(p.Started); n > 1 {
			t.Fatalf("cycle %d moved %d from one donor, want at most 1", i+1, n)
		}
	}
}

// A return is cancelled when its role turns short of its target alone, and
// when it turns busy alone. Q holds 7 and has returned some; then its demand
// rises.
func TestShareCancelsAReturnOnEitherTrigger(t *testing.T) {
	cancelled := func(returns int, qNeed float64) int {
		l := NewShareLedger()
		in := spareRun(2, qNeed, 7)
		for range returns {
			l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 1, Entitled: true, Returned: true}, in.Held, time.Unix(1000, 0), spareTimings())
		}
		return len(PlanShareTransfers(l, in, time.Unix(1015, 0), spareTimings()).Cancelled)
	}
	// 5 returned leave Q 2; a need of 1.1 makes its target 3 (2 + its headroom)
	// while it stays calm at 2 (1.1 <= 0.6 x 2).
	if n := cancelled(5, 1.1); n != 1 {
		t.Errorf("short of its target only: cancelled %d, want 1", n)
	}
	// 3 returned leave Q 4, its target at a need of 2.5; but 2.5 > 0.6 x 4.
	if n := cancelled(3, 2.5); n != 1 {
		t.Errorf("busy only: cancelled %d, want 1", n)
	}
	if n := cancelled(3, 1.5); n != 0 {
		t.Errorf("control, neither: cancelled %d, want 0", n)
	}
}

// The idle fill's credit is unpaced below need: L needs 6 and holds 2, with 4
// GPUs unassigned. All 4 are credited, so Q gives only the replica of
// headroom; credited at the pace of 2, Q would give 2 for GPUs that were free.
func TestShareIdleCreditIsUnpacedBelowNeed(t *testing.T) {
	in := spareRun(6, 0.4, 4)
	in.Held = map[string]int{"L": 2, "Q": 4}
	moved := 0
	for _, p := range planEach(NewShareLedger(), in, 2) {
		moved += qToL(p.Started)
	}
	if moved != 1 {
		t.Fatalf("Q -> L moved %d, want 1: the idle 4 cover L to its need", moved)
	}
}

// A receiver the last fill left short is not credited idle GPUs: whatever
// kept it short, the fill sees and the planner does not, so a donor funds it.
func TestShareFillShortReceiverIsFundedByADonor(t *testing.T) {
	in := spareRun(4, 0.4, 5) // L target 5, holding 3; 2 GPUs unassigned
	l := NewShareLedger()
	l.SetFillShort([]string{"L"})
	moved := 0
	for _, p := range planEach(l, in, 3) {
		moved += qToL(p.Started)
	}
	if moved == 0 {
		t.Fatal("a receiver the fill left short was credited idle GPUs; no donor funded it")
	}
}

// Neither a returned spare nor a move that skipped the waits counts against
// the concurrency limit: both are unpaced.
func TestShareInFlightCountsOrdinaryMovesOnly(t *testing.T) {
	l := NewShareLedger()
	held := map[string]int{"L": 3, "Q": 7}
	tm := spareTimings()
	l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 1, Entitled: true, Returned: true}, held, time.Unix(1000, 0), tm)
	l.Start(ShareTransfer{Donor: "Q", Receiver: "L", GPUs: 1, DonorGPUs: 1, SkippedWaits: true}, held, time.Unix(1000, 0), tm)
	if n := l.InFlight(); n != 0 {
		t.Fatalf("in flight %d, want 0", n)
	}
	l.Start(ShareTransfer{Donor: "Q", Receiver: "L", GPUs: 1, DonorGPUs: 1}, held, time.Unix(1000, 0), tm)
	if n := l.InFlight(); n != 1 {
		t.Fatalf("in flight %d, want 1: the ordinary move", n)
	}
}

func TestShareFillPace(t *testing.T) {
	if got := ShareFillPace(6, 2); got != math.MaxInt {
		t.Errorf("below its need: pace %d, want unbounded", got)
	}
	if got := ShareFillPace(2, 3); got != ShareMaxReplicasPerCycle {
		t.Errorf("at or above its need: pace %d, want %d", got, ShareMaxReplicasPerCycle)
	}
}

// The cap scales by weight in the planner: an important model (weight 2)
// keeps two replicas of headroom where a standard one keeps one.
func TestShareHeadroomScalesByWeight(t *testing.T) {
	roles := []ShareRole{
		{Key: "L", Weight: 2, Need: 2, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
		{Key: "Q", Weight: 1, Need: 2, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
	}
	p := PlanShareTransfers(NewShareLedger(), SharePlanInput{Roles: roles, Held: map[string]int{"L": 2, "Q": 2},
		Thresholds: map[string]float64{"L": 0.85, "Q": 0.85}, Budget: 10, Tolerance: 0.15,
		PhysicalFree: math.MaxInt, Headroom: ShareHeadroom{Capped: true, Replicas: 1}}, time.Unix(1000, 0), spareTimings())
	integ := map[string]int{}
	for _, v := range p.Evaluation.Roles {
		integ[v.Key] = v.Integer
	}
	if integ["L"] != 4 || integ["Q"] != 3 {
		t.Fatalf("targets %v, want L=4 (need 2 + 2 for weight 2), Q=3 (need 2 + 1)", integ)
	}
}

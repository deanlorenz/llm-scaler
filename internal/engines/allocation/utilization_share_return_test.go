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
	roles := []ShareRole{
		{Key: "L", Weight: 1, Need: 11, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
		{Key: "Q", Weight: 1, Need: 0.4, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
		{Key: "C", Weight: 1, Need: 0.4, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
	}
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
	roles := []ShareRole{
		{Key: "B", Weight: 1, Need: 10, Floor: 2, Ceiling: 10, ReplicaGPUs: 2},
		{Key: "A", Weight: 1, Need: 0.4, Floor: 1, Ceiling: 10, ReplicaGPUs: 1},
	}
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
		burstHistory(l)
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

// A returned spare does not count against the concurrency limit, handed to a
// receiver or not. A move that skipped the waits does: uncounted, ordinary
// headroom moves started beside a burst's and drained its donor for nothing.
func TestShareInFlightCountsEveryMoveButReturns(t *testing.T) {
	l := NewShareLedger()
	held := map[string]int{"L": 3, "Q": 7}
	tm := spareTimings()
	r := l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 1, Entitled: true, Returned: true}, held, time.Unix(1000, 0), tm)
	if !l.AssignReturn(r.ID, "L", "l", 1) {
		t.Fatal("setup: the return was not handed to L")
	}
	if n := l.InFlight(); n != 0 {
		t.Fatalf("in flight %d, want 0: a return, handed over or not, is not counted", n)
	}
	l.Start(ShareTransfer{Donor: "Q", Receiver: "L", GPUs: 1, DonorGPUs: 1, SkippedWaits: true}, held, time.Unix(1000, 0), tm)
	if n := l.InFlight(); n != 1 {
		t.Fatalf("in flight %d, want 1: the move that skipped the waits", n)
	}
}

// What a releasing transfer takes beyond its receiver's replica is not idle:
// an 8-GPU return handed to a 4-GPU receiver still runs on all eight.
func TestShareUnreleasedCountsWhatNoReceiverGets(t *testing.T) {
	l := NewShareLedger()
	held := map[string]int{"L": 4, "Q": 16}
	tm := spareTimings()
	r := l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 8, Entitled: true, Returned: true}, held, time.Unix(1000, 0), tm)
	if n := l.Unreleased(); n != 8 {
		t.Fatalf("unreleased %d, want 8: the return goes to nobody yet", n)
	}
	if !l.AssignReturn(r.ID, "L", "l", 4) {
		t.Fatal("setup: the return was not handed to L")
	}
	if n := l.Unreleased(); n != 4 {
		t.Fatalf("unreleased %d, want 4: L gets four of the eight", n)
	}
}

// While the quota covers every role at its recent peak plus its headroom, the
// spare is not capped and nothing is returned: no burst can find the GPUs it
// needs parked elsewhere, and a return would only make the next burst cold
// start. Control: once both have peaked at 8 (16 of 10), the cap binds.
func TestShareKeepsHeadroomWhileThePeaksFit(t *testing.T) {
	in := spareRun(2, 0.4, 7)
	in.Roles = []ShareRole{ // uncapped: the planner decides
		{Key: "L", Weight: 1, Need: 2, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
		{Key: "Q", Weight: 1, Need: 0.4, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
	}
	l := NewShareLedger()
	for i := range 4 {
		p := PlanShareTransfers(l, in, time.Unix(int64(1000+15*i), 0), spareTimings())
		if p.HeadroomCapped || p.Returns != 0 {
			t.Fatalf("cycle %d: capped %v, returned %d, with every peak fitting the quota", i+1, p.HeadroomCapped, p.Returns)
		}
	}
	returned := 0
	for _, p := range planEach(NewShareLedger(), in, 4) {
		returned += p.Returns
	}
	if returned == 0 {
		t.Fatal("control: nothing returned once the peaks contend")
	}
}

// A role still giving is not a receiver (ShareLedger.Giving): a move that
// skips the waits must not send GPUs back to it while its own release to the
// other way still drains.
func TestShareNoReverseWhileReleasing(t *testing.T) {
	moved := func(reverse bool) int {
		l := NewShareLedger()
		in := spareRun(11, 0.4, 7)
		if reverse {
			tr := l.Start(ShareTransfer{Donor: "L", Receiver: "Q", GPUs: 1, DonorGPUs: 1, SkippedWaits: true},
				map[string]int{"L": 4, "Q": 6}, time.Unix(900, 0), spareTimings()) // past its window: no longer cancellable
			l.ConfirmStarted(tr.ID, nil)
		}
		n := 0
		for _, p := range planEach(l, in, 2) {
			n += qToL(p.Started)
		}
		return n
	}
	if n := moved(true); n != 0 {
		t.Fatalf("Q -> L moved %d while L -> Q still releases", n)
	}
	if moved(false) == 0 {
		t.Fatal("control: Q -> L did not move")
	}
}

// Returns are not blocked by a short role no donor can fund: no single
// donor replica fits L's 2-GPU pod, and no set can be formed, so L is
// unfunded -- and Q's spare, held back for it, would sit parked for good.
func TestShareUnfundedRoleDoesNotBlockReturns(t *testing.T) {
	in := spareRun(4, 0.4, 7)
	in.Held = map[string]int{"L": 2, "Q": 7}
	in.Roles[0].ReplicaGPUs = 2
	in.Give = map[string]ShareVariant{"Q": {Name: "q", GPUs: 1}}
	in.Grow = map[string]ShareVariant{"L": {Name: "l", GPUs: 2}}
	returned := 0
	for _, p := range planEach(NewShareLedger(), in, 5) {
		returned += p.Returns
	}
	if returned == 0 {
		t.Fatal("Q kept its spare parked for a role no donor can fund")
	}
}

// A return still releasing goes to a role that turns short, as a transfer's
// GPUs would, instead of to the quota it would wait on with no reason shown.
func TestShareHandsAReturnToAShortReceiver(t *testing.T) {
	l := NewShareLedger()
	plans := planEach(l, spareRun(2, 0.4, 7), 3)
	if plans[2].Returns != 5 {
		t.Fatalf("setup: Q returned %d, want 5", plans[2].Returns)
	}
	p := PlanShareTransfers(l, spareRun(5, 0.4, 7), time.Unix(1045, 0), spareTimings())
	if len(p.Assigned) != 3 {
		t.Fatalf("assigned %d returns to L, want 3 (3 up to its target of 6)", len(p.Assigned))
	}
	for _, id := range p.Assigned {
		if tr, _ := l.Transfer(id); tr.Receiver != "L" || !tr.Returned {
			t.Fatalf("assigned %+v, want a return handed to L", tr)
		}
	}
	if qToL(p.Started) != 0 {
		t.Fatal("Q started new transfers to L besides its returns")
	}
}

// With no headroom, a return and its cancel are still a replica apart: a
// return at need 2.9 is not cancelled at 3.05.
func TestShareCancelKeepsAReplicaGapAtNoHeadroom(t *testing.T) {
	l := NewShareLedger()
	burstHistory(l)
	in := spareRun(2, 2.9, 7)
	in.Headroom.Replicas = 0
	in.Roles = []ShareRole{
		{Key: "L", Weight: 1, Need: 2, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
		{Key: "Q", Weight: 1, Need: 2.9, Floor: 1, Ceiling: 8, ReplicaGPUs: 1},
	}
	// Q at 7 returns down to 5 (2.9 <= 0.6 x 5); cancelled only past 0.6 x 6.
	for range 2 {
		l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 1, Entitled: true, Returned: true}, in.Held, time.Unix(1000, 0), spareTimings())
	}
	in.Roles[1].Need = 3.05
	if p := PlanShareTransfers(l, in, time.Unix(1010, 0), spareTimings()); len(p.Cancelled) != 0 {
		t.Fatalf("cancelled %v at need 3.05, within a replica of the return", p.Cancelled)
	}
	in.Roles[1].Need = 3.7
	if p := PlanShareTransfers(l, in, time.Unix(1020, 0), spareTimings()); len(p.Cancelled) == 0 {
		t.Fatal("control: not cancelled at 3.7 (> 0.6 x 6)")
	}
}

// A free node the idle credit has given to one replica is not counted again
// for the next: B's first replica is credited to n1's 8 free GPUs, and its
// second is funded by a donor set on the nodes A's pods free -- not deferred
// to a fill that has no GPUs left to place it.
func TestShareIdleCreditSpendsItsNode(t *testing.T) {
	four := func(name, node string) ShareUnit {
		return ShareUnit{Pods: []SharePod{{Name: name, Node: node, GPUs: 4}}}
	}
	in := SharePlanInput{
		Roles: []ShareRole{
			{Key: "A", Weight: 1, Need: 4, Ceiling: 16, ReplicaGPUs: 4},
			{Key: "B", Weight: 1, Need: 64, Ceiling: 32, ReplicaGPUs: 8},
		},
		Held:       map[string]int{"A": 24, "B": 16},
		Thresholds: map[string]float64{"A": 0.8, "B": 0.8}, Budget: 56, Tolerance: 0.15,
		Give:         map[string]ShareVariant{"A": {Name: "a", GPUs: 4, PodGPUs: []int{4}}},
		Grow:         map[string]ShareVariant{"B": {Name: "b", GPUs: 8, PodGPUs: []int{8}}},
		Nodes:        map[string]ShareNode{"n1": {Free: 8}, "n2": {Free: 4}, "n3": {Free: 4}},
		DonorUnits:   map[string][]ShareUnit{"A": {four("ns/a-0", "n2"), four("ns/a-1", "n3"), four("ns/a-2", "n2")}},
		PhysicalFree: 16,
	}
	p := PlanShareTransfers(NewShareLedger(), in, time.Unix(1000, 0), simTimings())
	toB := 0
	for _, tr := range p.Started {
		if tr.Receiver == "B" {
			toB++
		}
	}
	if toB == 0 {
		t.Fatalf("B's second replica was deferred to a fill that cannot place it: %+v", p.Started)
	}
}

// What keeps a role's headroom, when a return is due, is counted.
func TestShareReturnWithheldReasons(t *testing.T) {
	withheld := map[string]int{}
	for _, p := range planEach(NewShareLedger(), spareRun(2, 3, 7), 6) {
		for k, v := range p.Withheld {
			withheld[k] += v
		}
	}
	if withheld[ShareWithheldReturnNotCalm] == 0 {
		t.Errorf("Q kept spare above its target while not calm, and nothing said so: %v", withheld)
	}
	withheld = map[string]int{}
	in := spareRun(7, 0.4, 7)
	in.SkipWaits = ShareSkipWaits{Off: true}
	for _, p := range planEach(NewShareLedger(), in, 4) {
		for k, v := range p.Withheld {
			withheld[k] += v
		}
	}
	if withheld[ShareWithheldReturnRoleShort] == 0 {
		t.Errorf("returns held for a short role, and nothing said so: %v", withheld)
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
	// Both have peaked, at 8 and 7: the quota is contended, and the cap binds
	// -- neither's room beside the other's peak (2 and 3) is above its cap.
	l := NewShareLedger()
	l.RecordNeeds(map[string]float64{"L": 7, "Q": 8}, time.Unix(990, 0), spareTimings())
	p := PlanShareTransfers(l, SharePlanInput{Roles: roles, Held: map[string]int{"L": 2, "Q": 2},
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

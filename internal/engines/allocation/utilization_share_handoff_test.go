package allocation

import (
	"slices"
	"testing"
	"time"
)

// A wake that claims a return handed to a receiver (Redirect) ends it as a
// return: left Returned with no receiver, the next plan would hand it to
// another receiver, or cancel it as a return, while the wake waits on its
// GPUs.
func TestShareRedirectEndsAReturn(t *testing.T) {
	l := NewShareLedger()
	tm := spareTimings()
	held := map[string]int{"L": 3, "Q": 7}
	r := l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 1, Entitled: true, Returned: true, DonorPodGPUs: []int{1}},
		held, time.Unix(1000, 0), tm)
	if !l.AssignReturn(r.ID, "L", "l", 1) {
		t.Fatal("setup: the return was not handed to L")
	}
	if prev, ok := l.Redirect(r.ID, time.Minute); !ok || prev.Receiver != "L" {
		t.Fatalf("setup: redirect %v of %+v, want the return handed to L", ok, prev)
	}
	if tr, _ := l.Transfer(r.ID); tr.Returned || tr.Receiver != "" {
		t.Fatalf("claimed by a wake, it is still a return: %+v", tr)
	}
	if l.AssignReturn(r.ID, "C", "c", 1) {
		t.Fatal("a return a wake claimed was handed to another receiver")
	}
	// L is short again, Q calm: a plan does not hand the claimed GPUs over.
	p := PlanShareTransfers(l, spareRun(5, 0.4, 7), time.Unix(1010, 0), tm)
	if slices.Contains(p.Assigned, r.ID) {
		t.Fatalf("the plan handed a claimed return to a receiver: %v", p.Assigned)
	}
	// Q's demand is back in full: what a return would be called off for. The
	// wake's GPUs are not.
	p = PlanShareTransfers(l, spareRun(5, 8, 7), time.Unix(1020, 0), tm)
	if slices.Contains(p.Cancelled, r.ID) {
		t.Fatalf("the plan cancelled a claimed return as a return: %v", p.Cancelled)
	}
	if _, ok := l.Transfer(r.ID); !ok {
		t.Fatal("the claimed transfer left the ledger")
	}
}

// returnFitPlan plans one cycle for R, short of its need (14 on 8) and a 1x8
// pod, beside Q, which has one 8-GPU return still releasing: shape is the
// returned pod's GPUs as the return recorded it, nil when unknown.
func returnFitPlan(shape []int) (SharePlan, string) {
	tm := simTimings()
	t0 := time.Unix(1000, 0)
	l := NewShareLedger()
	held := map[string]int{"R": 8, "Q": 24}
	roles := []ShareRole{
		{Key: "R", Weight: 1, Need: 14, Floor: 8, Ceiling: 64, ReplicaGPUs: 8},
		{Key: "Q", Weight: 1, Need: 2, Floor: 8, Ceiling: 64, ReplicaGPUs: 8},
	}
	tr := l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 8, DonorVariant: "q", Entitled: true, Returned: true, DonorPodGPUs: shape},
		held, t0.Add(-30*time.Second), tm)
	l.ConfirmStarted(tr.ID, []string{"p"})
	// Q gives from a 1x8 variant now: what it returned is judged on the shape
	// it was returned from, not on what it would give today.
	give := map[string]ShareVariant{"Q": {Name: "q", GPUs: 8, PodGPUs: []int{8}}}
	grow := map[string]ShareVariant{"R": {Name: "r", GPUs: 8, PodGPUs: []int{8}}}
	in := SharePlanInput{Roles: roles, Held: held, Thresholds: map[string]float64{"R": 0.8, "Q": 0.8}, Budget: 32,
		Tolerance: 0.15, Give: give, Grow: grow, Headroom: ShareHeadroom{Capped: true, Replicas: 1}}
	return PlanShareTransfers(l, in, t0, tm), tr.ID
}

// A return is handed over only where the returned pod's own shape covers the
// receiver's pod: a 1x8 return frees a node for a 1x8 pod, a 2x4 one does not,
// and one whose shape is not known -- read back from its marks after a
// restart -- is not handed over at all. Judged on the donor's give variant
// instead, a 2x4 return would be handed over whenever the donor gives 1x8.
func TestShareReturnHandedOnlyWhereItsShapeFits(t *testing.T) {
	for _, c := range []struct {
		name   string
		shape  []int
		handed bool
	}{
		{"1x8 return, 1x8 receiver", []int{8}, true},
		{"2x4 return, 1x8 receiver", []int{4, 4}, false},
		{"return of unknown shape", nil, false},
	} {
		p, id := returnFitPlan(c.shape)
		if got := slices.Contains(p.Assigned, id); got != c.handed {
			t.Errorf("%s: handed to R %v, want %v (assigned %v)", c.name, got, c.handed, p.Assigned)
		}
	}
}

// A return records the shape of the replica it gives back, for the hand-off
// to judge (fromReturns): the give variant's pods, or one pod of the whole
// replica when they are not known.
func TestShareReturnRecordsItsShape(t *testing.T) {
	in := spareRun(2, 0.4, 7)
	in.Give = map[string]ShareVariant{"Q": {Name: "q", GPUs: 1}, "L": {Name: "l", GPUs: 1}}
	in.Grow = map[string]ShareVariant{"Q": {Name: "q", GPUs: 1}, "L": {Name: "l", GPUs: 1}}
	n := 0
	for _, p := range planEach(NewShareLedger(), in, 3) {
		for _, tr := range p.Started {
			if !tr.Returned {
				continue
			}
			n++
			if !slices.Equal(tr.DonorPodGPUs, []int{1}) {
				t.Fatalf("a return recorded pods %v, want [1]: %+v", tr.DonorPodGPUs, tr)
			}
		}
	}
	if n == 0 {
		t.Fatal("setup: Q returned nothing")
	}
}

// lwsReceiverPlan plans one cycle for R, short of its need (40 on 16) with a
// replica of two 8-GPU pods, beside D, which gives 1x8 replicas: no single
// donor replica fits R, so R is funded by a donor set -- unless returns D has
// started, handed to nobody, are already freeing a replica of R.
func lwsReceiverPlan(returns int) SharePlan {
	return lwsReceiverPlanTo(returns, "", 96)
}

// lwsReceiverPlanTo is lwsReceiverPlan with the returns handed to receiver
// to when it is not "": a 1x8 role short of its need of 20 on 8.
func lwsReceiverPlanTo(returns int, to string, rCeiling int) SharePlan {
	tm := simTimings()
	t0 := time.Unix(100000, 0)
	l := NewShareLedger()
	held := map[string]int{"R": 16, "D": 64}
	roles := []ShareRole{
		{Key: "R", Weight: 1, Need: 40, Floor: 16, Ceiling: rCeiling, ReplicaGPUs: 16},
		{Key: "D", Weight: 1, Need: 2, Floor: 8, Ceiling: 96, ReplicaGPUs: 8},
	}
	th := map[string]float64{"R": 0.8, "D": 0.8}
	budget := 80
	if to != "" {
		roles = append(roles, ShareRole{Key: to, Weight: 1, Need: 20, Floor: 8, Ceiling: 64, ReplicaGPUs: 8})
		held[to], th[to], budget = 8, 0.8, 88
	}
	for range returns {
		r := l.Start(ShareTransfer{Donor: "D", DonorGPUs: 8, DonorVariant: "d", Entitled: true, Returned: true, DonorPodGPUs: []int{8}},
			held, t0.Add(-30*time.Second), tm)
		if to != "" && !l.AssignReturn(r.ID, to, "x", 8) {
			panic("setup: a return was not handed to " + to)
		}
	}
	in := SharePlanInput{Roles: roles, Held: held, Thresholds: th, Budget: budget,
		Tolerance: 0.15, Headroom: ShareHeadroom{Capped: true, Replicas: 1},
		Give: map[string]ShareVariant{"D": {Name: "d", GPUs: 8, PodGPUs: []int{8}}},
		Grow: map[string]ShareVariant{"R": {Name: "r", GPUs: 16, PodGPUs: []int{8, 8}}}}
	return PlanShareTransfers(l, in, t0, tm)
}

// A receiver whose replica spans several pods waits on returns that will free
// a replica of it: they land in the unassigned GPUs, where the idle fill
// places its pods, and a donor set now would drain a donor for GPUs already
// on their way. Control: with nothing returning, a set funds it.
func TestShareLWSReceiverWaitsOnReturns(t *testing.T) {
	toR := func(p SharePlan) int {
		n := 0
		for _, tr := range p.Started {
			if tr.Receiver == "R" || (tr.SetID != "" && !tr.Returned) {
				n++
			}
		}
		return n
	}
	// R's ceiling of 32 leaves it one replica short: two returns of 8 cover
	// it, and no set drains D.
	p := lwsReceiverPlanTo(2, "", 32)
	if n := toR(p); n != 0 {
		t.Fatalf("a donor set started for R while 16 returned GPUs, all it lacks, release: %+v", p.Started)
	}
	if p.Withheld[ShareWithheldAwaitingReturn] == 0 || p.Unfunded["R"] != ShareUnfundedAwaitingReturn {
		t.Fatalf("R waits on the returns and nothing said so: withheld %v, unfunded %v", p.Withheld, p.Unfunded)
	}
	// Short of more than the returns cover, R is credited with them and a set
	// funds the rest: waiting its whole shortfall on one replica of returns
	// held it back a release.
	if p := lwsReceiverPlan(2); p.Withheld[ShareWithheldAwaitingReturn] == 0 || toR(p) == 0 {
		t.Fatalf("R, short past the returns, was not funded for the rest: withheld %v, started %+v", p.Withheld, p.Started)
	}
	// One return frees half a replica of R: not enough to wait on.
	if p := lwsReceiverPlan(1); p.Withheld[ShareWithheldAwaitingReturn] != 0 {
		t.Fatalf("R waited on 8 returned GPUs, half its replica: %v", p.Withheld)
	}
	// Returns already handed to another receiver free nothing for R.
	if p := lwsReceiverPlanTo(2, "X", 96); p.Withheld[ShareWithheldAwaitingReturn] != 0 || toR(p) == 0 {
		t.Fatalf("R waited on returns handed to X: withheld %v, cancelled %v, started %+v", p.Withheld, p.Cancelled, p.Started)
	}
	if n := toR(lwsReceiverPlan(0)); n == 0 {
		t.Fatal("control: with nothing returning, no donor set funded R")
	}
}

// A return handed to a receiver is a transfer now, and is judged as one when
// its donor's demand comes back: Q's need of 2.5 is busier than the calm load
// on the 4 it keeps -- reason enough to call off a return -- but not short of
// its target, and L still needs the GPUs, so the move stands. Control: the
// same return, handed to nobody, is called off.
func TestShareAssignedReturnIsCancelledAsATransfer(t *testing.T) {
	cancelled := func(assign bool) bool {
		l := NewShareLedger()
		burstHistory(l)
		in := spareRun(5, 2.5, 7)
		var first string
		for i := range 3 {
			tr := l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 1, Entitled: true, Returned: true}, in.Held, time.Unix(1000, 0), spareTimings())
			if i == 0 {
				first = tr.ID
			}
		}
		if assign && !l.AssignReturn(first, "L", "l", 1) {
			t.Fatal("setup: the return was not handed to L")
		}
		return slices.Contains(PlanShareTransfers(l, in, time.Unix(1015, 0), spareTimings()).Cancelled, first)
	}
	if cancelled(true) {
		t.Fatal("a return handed to L was called off as a return, though neither Q is short nor L fine")
	}
	if !cancelled(false) {
		t.Fatal("control: the same return, handed to nobody, was not called off")
	}
}

// What is released is counted once per receiver replica: a donor set whose two
// members together fund one replica leaves nothing unassigned; a transfer
// already filling has released what it gave; and a member that gives less than
// its receiver's replica takes nothing from another's excess.
func TestShareUnreleasedPerReceiverReplica(t *testing.T) {
	tm := spareTimings()
	t0 := time.Unix(1000, 0)
	held := map[string]int{"A": 8, "B": 8, "R": 8, "Q": 8}

	l := NewShareLedger()
	p := l.Start(ShareTransfer{Donor: "A", Receiver: "R", GPUs: 8, DonorGPUs: 4, SetID: "pending"}, held, t0, tm)
	c := l.Start(ShareTransfer{Donor: "B", DonorGPUs: 4, SetID: "pending"}, held, t0, tm)
	l.LinkSet(p.ID, c.ID)
	if n := l.Unreleased(); n != 0 {
		t.Fatalf("a set of 4 + 4 funding one 8-GPU replica leaves %d unreleased, want 0", n)
	}

	l = NewShareLedger()
	l.Start(ShareTransfer{Donor: "Q", Receiver: "R", GPUs: 4, DonorGPUs: 8}, held, t0, tm)
	if n := l.Unreleased(); n != 4 {
		t.Fatalf("setup: an 8-GPU replica funding a 4-GPU one leaves %d unreleased, want 4", n)
	}
	l.Observe(map[string]int{"A": 8, "B": 8, "R": 8, "Q": 0}, t0.Add(15*time.Second), tm)
	if tr := l.Transfers(); len(tr) != 1 || tr[0].State != ShareFilling {
		t.Fatalf("setup: want the transfer filling, got %+v", tr)
	}
	if n := l.Unreleased(); n != 0 {
		t.Fatalf("a filling transfer counts %d unreleased, want 0: its donor has released", n)
	}

	l = NewShareLedger()
	l.Start(ShareTransfer{Donor: "A", Receiver: "R", GPUs: 8, DonorGPUs: 4, SetID: "pending"}, held, t0, tm)
	l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 4, Entitled: true, Returned: true}, held, t0, tm)
	if n := l.Unreleased(); n != 4 {
		t.Fatalf("unreleased %d, want 4: the return's, which a set primary's shortfall does not offset", n)
	}
}

// A return waits the confirm cycles in force, counted in cycles -- not its
// scale-down window: the release that follows waits that window again. With
// three confirm cycles and a 300 s window, Q returns on the third cycle, 30 s
// in.
func TestShareReturnWaitsTheConfirmCycles(t *testing.T) {
	tm := spareTimings()
	tm.Window = 300 * time.Second
	in := spareRun(2, 0.4, 7)
	in.ConfirmCycles = 3
	l := NewShareLedger()
	burstHistory(l)
	for i := range 3 {
		p := PlanShareTransfers(l, in, time.Unix(int64(1000+15*i), 0), tm)
		if i < 2 && p.Returns != 0 {
			t.Fatalf("cycle %d returned %d before three confirm cycles", i+1, p.Returns)
		}
		if i == 2 && p.Returns == 0 {
			t.Fatal("cycle 3 returned nothing after three confirm cycles")
		}
	}
}

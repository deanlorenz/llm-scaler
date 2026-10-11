package allocation

import (
	"testing"
	"time"
)

// swingingL makes L a swinging role whose mean, over many quiet readings, is
// far below a burst it reads now: the case where judging it on the mean and
// judging it on the reading disagree.
func swingingL(l *ShareLedger, quiet float64) {
	for i := 20; i > 0; i-- {
		l.RecordNeeds(map[string]float64{"L": quiet}, time.Unix(int64(1000-15*i), 0), spareTimings())
	}
	l.swingUntil["L"] = time.Unix(1_000_000, 0)
}

// knownFor gives roles a whole swing window of need history before now, so
// their recorded peaks are taken as known (ShareLedger.PeakKnown).
func knownFor(l *ShareLedger, now time.Time, roles ...string) {
	needs := map[string]float64{}
	for _, r := range roles {
		needs[r] = 0
	}
	l.RecordNeeds(needs, now.Add(-spareTimings().SwingWindow), spareTimings())
}

// Until a role has a swing window of history, its peak is not known and is
// taken at its ceiling: after a restart, the cap is on until the history
// shows the peaks fit. Judged on the history it had, the cap stayed off until
// a model burst inside the window -- and the first burst found the spare
// parked on another model.
func TestShareCapsUntilThePeaksAreKnown(t *testing.T) {
	in := spareRun(2, 0.4, 7)
	l := NewShareLedger()
	t0 := time.Unix(1000, 0)
	if p := PlanShareTransfers(l, in, t0, spareTimings()); !p.HeadroomCapped || p.PeakClaimGPUs != 16 {
		t.Fatalf("fresh ledger: capped %v on peak claims %d, want capped on 16 (both ceilings)", p.HeadroomCapped, p.PeakClaimGPUs)
	}
	later := t0.Add(spareTimings().SwingWindow + time.Second)
	if p := PlanShareTransfers(l, in, later, spareTimings()); p.HeadroomCapped {
		t.Fatalf("a window later: capped on peak claims %d, though the peaks (2 + 1) fit", p.PeakClaimGPUs)
	}
}

// A model is not capped below what the others' peaks leave of the quota: Q
// peaked at 7 and is calm on 6, L peaked at 3.5 and holds 4 of 10. No peak of
// L's can use what Q would return, so Q keeps it warm for its own next burst.
func TestShareCapLeavesWhatNoOtherPeakUses(t *testing.T) {
	in := spareRun(3.5, 1.5, 6)
	in.Held["L"] = 4
	l := NewShareLedger()
	knownFor(l, time.Unix(990, 0), "L", "Q")
	l.RecordNeeds(map[string]float64{"L": 3.5, "Q": 7}, time.Unix(990, 0), spareTimings())
	returned := 0
	for i := range 5 {
		p := PlanShareTransfers(l, in, time.Unix(int64(1000+15*i), 0), spareTimings())
		if !p.HeadroomCapped {
			t.Fatalf("cycle %d: setup: the peaks (7 + 4 of 10) contend, and the cap is off", i)
		}
		returned += p.Returns
	}
	if returned != 0 {
		t.Fatalf("Q returned %d replicas no other model's peak could use", returned)
	}
}

// A swinging role at the crest of a burst is capped on its reading, not on
// its mean: capped on the mean, it is held below its demand beside GPUs only
// it would use.
func TestShareCapsASwingingRoleOnItsReading(t *testing.T) {
	in := spareRun(8, 2, 7)
	l := NewShareLedger()
	swingingL(l, 1)
	l.RecordNeeds(map[string]float64{"Q": 8}, time.Unix(990, 0), spareTimings())
	p := PlanShareTransfers(l, in, time.Unix(1000, 0), spareTimings())
	if !p.HeadroomCapped {
		t.Fatal("setup: the peaks (8 + 8 of 10) contend, and the cap is off")
	}
	for _, v := range p.Evaluation.Roles {
		if v.Key == "L" && v.Integer < 7 {
			t.Fatalf("L, reading 8, has target %d: capped on its mean", v.Integer)
		}
	}
}

// A swinging role at the crest of a burst is a hard imbalance on its reading:
// judged on its mean, it never skips the waits, and fills one replica a
// confirm at a time while it is short.
func TestShareSwingingReceiverSkipsTheWaits(t *testing.T) {
	in := spareRun(8, 0.5, 7)
	l := NewShareLedger()
	swingingL(l, 1)
	skipped := 0
	for i := range 3 {
		p := PlanShareTransfers(l, in, time.Unix(int64(1000+15*i), 0), spareTimings())
		for _, tr := range p.Started {
			if tr.Receiver == "L" && tr.SkippedWaits {
				skipped++
			}
		}
	}
	if skipped == 0 {
		t.Fatal("L, reading 8 on 3, never skipped the waits: judged on its mean")
	}
	if got := l.Committed(in.Held)["L"]; got < 7 {
		t.Fatalf("L committed %d after three cycles, want at least 7", got)
	}
}

// A donor set judges a swinging donor's calm as a single move does, on the
// higher of its reading and its mean: D reads 40 on a mean of 2, and a single
// move from it is refused, so the set that would take two whole nodes from it
// must be too.
func TestShareDonorSetJudgesASwingingDonorOnItsReading(t *testing.T) {
	tm := simTimings()
	t0 := time.Unix(100000, 0)
	v8 := ShareVariant{Name: "v", GPUs: 8, PodGPUs: []int{8}}
	lws := ShareVariant{Name: "l", GPUs: 16, PodGPUs: []int{8, 8}}
	l := NewShareLedger()
	for i := 60; i > 0; i-- {
		l.RecordNeeds(map[string]float64{"D": 2, "R": 40, "C": 1}, t0.Add(-time.Duration(30*i)*time.Second), tm)
	}
	l.recordMove("D", true, "x", t0.Add(-10*time.Second), tm)
	l.swingUntil["D"] = t0.Add(time.Hour)
	held := map[string]int{"R": 16, "D": 64, "C": 16}
	roles := []ShareRole{
		{Key: "R", Weight: 1, Need: 40, Floor: 16, Ceiling: 96, ReplicaGPUs: 16},
		{Key: "D", Weight: 1, Need: 40, Floor: 8, Ceiling: 96, ReplicaGPUs: 8},
		{Key: "C", Weight: 1, Need: 1, Floor: 16, Ceiling: 96, ReplicaGPUs: 8},
	}
	vs := map[string]ShareVariant{"R": lws, "D": v8, "C": v8}
	th := map[string]float64{"R": 0.8, "D": 0.8, "C": 0.8}
	in := SharePlanInput{Roles: roles, Held: held, Thresholds: th, Budget: 96, Tolerance: 0.15,
		Give: vs, Grow: vs, Headroom: ShareHeadroom{Capped: true, Replicas: 1}}
	for i := range 2 {
		p := PlanShareTransfers(l, in, t0.Add(time.Duration(30*i)*time.Second), tm)
		for _, tr := range p.Started {
			if tr.Donor == "D" {
				t.Fatalf("cycle %d: a set drained D, reading 40, as calm on its mean: %+v", i, tr)
			}
		}
	}
}

// A return is handed only to a receiver whose pod the returned pod's hole
// holds: a 2x4 replica going does not free a node for a 1x8 pod.
func TestShareReturnHandedOnlyWhereThePodFits(t *testing.T) {
	tm := simTimings()
	t0 := time.Unix(1000, 0)
	rv := ShareVariant{Name: "r", GPUs: 8, PodGPUs: []int{8}}
	qv := ShareVariant{Name: "q", GPUs: 8, PodGPUs: []int{4, 4}}
	l := NewShareLedger()
	held := map[string]int{"R": 8, "Q": 24}
	roles := []ShareRole{
		{Key: "R", Weight: 1, Need: 14, Floor: 8, Ceiling: 64, ReplicaGPUs: 8},
		{Key: "Q", Weight: 1, Need: 2, Floor: 8, Ceiling: 64, ReplicaGPUs: 8},
	}
	l.RecordNeeds(map[string]float64{"R": 40, "Q": 40}, t0.Add(-60*time.Second), tm)
	tr := l.Start(ShareTransfer{Donor: "Q", DonorGPUs: 8, DonorVariant: "q", Entitled: true, Returned: true}, held, t0.Add(-30*time.Second), tm)
	l.ConfirmStarted(tr.ID, []string{"p"})
	vs := map[string]ShareVariant{"Q": qv, "R": rv}
	in := SharePlanInput{Roles: roles, Held: held, Thresholds: map[string]float64{"R": 0.8, "Q": 0.8}, Budget: 32,
		Tolerance: 0.15, Give: vs, Grow: vs, Headroom: ShareHeadroom{Capped: true, Replicas: 1}}
	if p := PlanShareTransfers(l, in, t0, tm); len(p.Assigned) != 0 {
		t.Fatalf("a 2x4 return was handed to a 1x8 receiver: %v", p.Assigned)
	}
}

// A huge weight does not overflow the headroom it buys.
func TestShareHeadroomReplicasIsBounded(t *testing.T) {
	if got := ShareHeadroomReplicas(1e300, 10); got != MaxShareBudgetGPUs {
		t.Fatalf("headroom %d, want %d", got, MaxShareBudgetGPUs)
	}
}

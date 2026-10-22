package allocation

import (
	"cmp"
	"math"
	"slices"
	"testing"
	"time"
)

// stallPhase is a stretch of constant need per role, in GPUs at the scale-up
// threshold.
type stallPhase struct {
	dur  time.Duration
	need map[string]float64
}

// stallCycle is what one cycle of stallLoop left.
type stallCycle struct {
	at        time.Duration
	held      map[string]int
	swinging  []string
	donorPeak int
	started   int
}

// stallLoop runs two roles of 1-GPU replicas in a quota of 4 through phases,
// cycle by cycle, closing the loop as runShareSim does -- a donor's replica
// leaves simRelease after its transfer starts, a receiver is raised the cycle
// its transfer fills -- and with the idle fill the actuator runs after each
// plan (fillIdleShare). With lag, each role's need is read as the mean of the
// samples from 2·simLag to simLag ago, as runShareSim reads it.
func stallLoop(tm ShareTimings, initial map[string]int, lag bool, phases []stallPhase) []stallCycle {
	const budget = 4
	keys := []string{"A", "B"}
	l := NewShareLedger()
	t0 := time.Unix(1_000_000, 0)
	held := map[string]int{}
	for k, v := range initial {
		held[k] = v
	}
	released, granted := map[string]bool{}, map[string]bool{}
	samples := map[string][]float64{}
	var total time.Duration
	for _, ph := range phases {
		total += ph.dur
	}
	var out []stallCycle
	start, pi := time.Duration(0), 0
	for t := time.Duration(0); t < total; t += simCycle {
		if t >= start+phases[pi].dur {
			start += phases[pi].dur
			pi++
		}
		now := t0.Add(t)
		for _, tr := range l.Transfers() {
			switch {
			case tr.State == ShareReleasing && tr.Donor != "" && !released[tr.ID] && now.Sub(tr.Started) >= simRelease:
				held[tr.Donor] -= tr.DonorGPUs
				released[tr.ID] = true
			case tr.State == ShareFilling && tr.Receiver != "" && !granted[tr.ID]:
				held[tr.Receiver] += tr.GPUs
				granted[tr.ID] = true
			}
		}
		l.Observe(held, now, tm)
		l.TakeReleased()
		in := SharePlanInput{Held: held, Thresholds: map[string]float64{}, DownThresholds: map[string]float64{},
			Budget: budget, Tolerance: 0.15, PhysicalFree: math.MaxInt,
			Headroom: ShareHeadroom{Capped: true, Replicas: 1, ReturnLoad: 0.6}}
		for _, k := range keys {
			need := phases[pi].need[k]
			samples[k] = append(samples[k], need)
			if lag {
				s, n := samples[k], int(simLag/simCycle)
				lo, hi := max(0, len(s)-1-2*n), max(0, len(s)-1-n)
				sum := 0.0
				for _, v := range s[lo : hi+1] {
					sum += v
				}
				need = sum / float64(hi-lo+1)
			}
			in.Roles = append(in.Roles, ShareRole{Key: k, Weight: 1, Need: need, Floor: 1, MinFloor: 1, ReplicaGPUs: 1})
			in.Thresholds[k], in.DownThresholds[k] = 0.85, 0.70
		}
		plan := PlanShareTransfers(l, in, now, tm)

		// The idle fill, as fillIdleShare runs it without node information.
		committed := l.Committed(held)
		idle := budget - l.WakeHeld(now) - l.Unreleased()
		for _, c := range committed {
			idle -= c
		}
		integer := map[string]int{}
		for _, v := range plan.Evaluation.Roles {
			integer[v.Key] = v.Integer
		}
		byKey := map[string]ShareRole{}
		for _, r := range in.Roles {
			byKey[r.Key] = r
		}
		var receivers []string
		for _, k := range keys {
			if committed[k] < integer[k] && !l.FillHeld(k, now) && !l.Giving(k) {
				receivers = append(receivers, k)
			}
		}
		z := func(k string) float64 { return byKey[k].Score(float64(committed[k])) }
		slices.SortFunc(receivers, func(a, b string) int { return cmp.Or(cmp.Compare(z(a), z(b)), cmp.Compare(a, b)) })
		for _, rc := range receivers {
			for n := 0; n < ShareFillPace(byKey[rc].Need, committed[rc]) && idle >= 1 && committed[rc] < integer[rc]; n++ {
				l.StartFill(rc, "v", 1, float64(committed[rc]) < byKey[rc].Need, held, now, tm)
				idle--
				committed[rc]++
			}
		}
		l.SetFillShort(slices.DeleteFunc(slices.Clone(receivers), func(rc string) bool { return committed[rc] >= integer[rc] }))

		out = append(out, stallCycle{at: t, held: map[string]int{"A": held["A"], "B": held["B"]},
			swinging: plan.Swinging, donorPeak: plan.Withheld[ShareWithheldDonorPeak], started: len(plan.Started)})
	}
	return out
}

// stallReached is when, at or after from, the loop first held a:b, and whether it
// ever did.
func stallReached(cycles []stallCycle, from time.Duration, a, b int) (time.Duration, bool) {
	for _, c := range cycles {
		if c.at >= from && c.held["A"] == a && c.held["B"] == b {
			return c.at - from, true
		}
	}
	return 0, false
}

// stallHeldAt is the loop's split at the last cycle before at.
func stallHeldAt(cycles []stallCycle, at time.Duration) (int, int) {
	var last stallCycle
	for _, c := range cycles {
		if c.at >= at {
			break
		}
		last = c
	}
	return last.held["A"], last.held["B"]
}

// stallSwap returns phases with the two roles' needs exchanged: the mirror order.
func stallSwap(phases []stallPhase) []stallPhase {
	out := slices.Clone(phases)
	for i, ph := range out {
		out[i].need = map[string]float64{"A": ph.need["B"], "B": ph.need["A"]}
	}
	return out
}

func stallNeeds(a, b float64) map[string]float64 { return map[string]float64{"A": a, "B": b} }

// A donor whose load fell is not held at its peak over the swing window. A
// new group plans with its headroom capped until it has a swing window of
// history, so the first swap, 1:1 to 3:1, goes through. By the swap back the
// peaks are known and fit the quota (2 + 2 of 4), so the spare is spread, and
// the receiver, already at its need, takes no replica that leaves its donor
// below the donor's peak (donor-peak). Judged on its peak over the swing
// window, the donor kept its second replica until that peak aged out: the
// pair sat at 2:2 for 95 minutes. Judged on its peak over its scale-down
// window, the second replica moves once that window has passed. Both orders.
func TestShareSwapBackIsNotHeldOnTheDonorsOldPeak(t *testing.T) {
	const m = time.Minute
	phases := []stallPhase{{60 * m, stallNeeds(1, 1)}, {60 * m, stallNeeds(1.5, 0.5)}, {120 * m, stallNeeds(0.5, 1.5)}}
	for _, order := range []struct {
		name   string
		phases []stallPhase
		first  [2]int
		final  [2]int
	}{
		{"A then B", phases, [2]int{3, 1}, [2]int{1, 3}},
		{"B then A", stallSwap(phases), [2]int{1, 3}, [2]int{3, 1}},
	} {
		cycles := stallLoop(simTimings(), map[string]int{"A": 1, "B": 1}, false, order.phases)
		if a, b := stallHeldAt(cycles, 120*m); a != order.first[0] || b != order.first[1] {
			t.Fatalf("%s, setup: the first swap left %d:%d, want %d:%d", order.name, a, b, order.first[0], order.first[1])
		}
		held := 0
		for _, c := range cycles {
			if c.at >= 120*m {
				held += c.donorPeak
			}
		}
		if held == 0 {
			t.Fatalf("%s, setup: donor-peak never held the swap back; the scenario no longer reaches the rule", order.name)
		}
		after, ok := stallReached(cycles, 120*m, order.final[0], order.final[1])
		if !ok || after > 20*m {
			t.Errorf("%s: the swap back reached %d:%d after %v (reached %v), want within 20m: the donor was held on its old peak",
				order.name, order.final[0], order.final[1], after, ok)
			continue
		}
		t.Logf("%s: swap back reached %d:%d after %v, donor-peak held %d cycles", order.name, order.final[0], order.final[1], after, held)
	}
}

// A load that moves for good is not planned on its mean. Two models swap
// 1:3 for 3:1 and back within a swing window: the first move back is each
// role's second change of direction, and the swing rule plans both on their
// mean need. On their means the split is 2:2, and the busy model sat there
// past its threshold -- a need of 3 on 2 GPUs -- for 85 minutes. Its need
// has stayed well above its mean, and the other's well below, for longer than
// a move takes: a step, so each is planned on its current need again. Both
// orders.
func TestShareStepAfterASwapIsNotPlannedOnTheMean(t *testing.T) {
	const m = time.Minute
	tm, _ := DeriveShareTimings(ShareTimingInputs{Cycle: simCycle}, nil)
	phases := []stallPhase{{240 * m, stallNeeds(2, 2)}, {30 * m, stallNeeds(1, 3)}, {30 * m, stallNeeds(3, 1)}, {180 * m, stallNeeds(1, 3)}}
	for _, order := range []struct {
		name   string
		phases []stallPhase
		final  [2]int
	}{
		{"A then B", phases, [2]int{1, 3}},
		{"B then A", stallSwap(phases), [2]int{3, 1}},
	} {
		cycles := stallLoop(tm, map[string]int{"A": 2, "B": 2}, true, order.phases)
		swung := false
		for _, c := range cycles {
			if c.at >= 300*m && len(c.swinging) > 0 {
				swung = true
			}
		}
		if !swung {
			t.Fatalf("%s, setup: the swap back never started the swing rule; the scenario no longer reaches it", order.name)
		}
		after, ok := stallReached(cycles, 300*m, order.final[0], order.final[1])
		if !ok || after > 30*m {
			t.Errorf("%s: the step reached %d:%d after %v (reached %v), want within 30m: it was planned on its mean",
				order.name, order.final[0], order.final[1], after, ok)
			continue
		}
		t.Logf("%s: step reached %d:%d after %v", order.name, order.final[0], order.final[1], after)
	}
}

// A load that keeps swinging is still planned on its mean. The two models
// swap every 8 minutes, faster than a move lands (about 13 minutes): the need
// never stays on one side of its mean for a move's latency, so the swing rule
// holds once it starts, and the transfers stop chasing each swap. Control:
// with the swing window at zero -- the rule off -- the same load starts more
// transfers.
func TestShareFastSwapsStayPlannedOnTheMean(t *testing.T) {
	const m = time.Minute
	tm, _ := DeriveShareTimings(ShareTimingInputs{Cycle: simCycle}, nil)
	phases := []stallPhase{{240 * m, stallNeeds(2, 2)}}
	for i := range 30 {
		if i%2 == 0 {
			phases = append(phases, stallPhase{8 * m, stallNeeds(1, 3)})
		} else {
			phases = append(phases, stallPhase{8 * m, stallNeeds(3, 1)})
		}
	}
	run := func(tm ShareTimings) (swinging, cycles, started int) {
		for _, c := range stallLoop(tm, map[string]int{"A": 2, "B": 2}, true, phases) {
			if c.at < 300*m {
				continue
			}
			cycles++
			if len(c.swinging) > 0 {
				swinging++
			}
			started += c.started
		}
		return swinging, cycles, started
	}
	swinging, cycles, started := run(tm)
	if swinging*3 < cycles {
		t.Fatalf("swaps every 8 minutes were planned on the mean in %d of %d cycles, want at least a third", swinging, cycles)
	}
	off := tm
	off.SwingWindow = 0
	_, _, startedOff := run(off)
	if started >= startedOff {
		t.Fatalf("control: the swing rule started %d transfers, without it %d; want fewer with it", started, startedOff)
	}
	t.Logf("swinging %d of %d cycles; %d transfers started, %d without the rule", swinging, cycles, started, startedOff)
}

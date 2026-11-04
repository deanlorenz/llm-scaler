package saturation

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// Learner PR 5b (analyzer-learner.md, PR 5, change B): the start guard opens
// on startConfirmPods distinct Pods over maxStartOutlierFactor times the
// estimate, counted across the run, never on one Pod repeating; when it
// opens, the estimate is the run's median. The tests the switch decides run
// it on and off and assert both figures, so each fails with
// SwitchStartConfirmsByDistinctPod off.

const (
	confirmVariant = "decode-v"
	confirmKey     = "ns|model|" + confirmVariant + "|H100|1"
)

func confirmReplica(pod string, startSeconds float64) domain.ReplicaMetrics {
	rm := makeReplicaMetrics(pod, confirmVariant, 400_000, tracedKv, 10, 1000, 6000)
	rm.Ready = true
	rm.Namespace = "ns"
	rm.StartSeconds = startSeconds
	return rm
}

func confirmRows(rows ...domain.ReplicaMetrics) []domain.ReplicaMetrics { return rows }

// confirmAnalyzer is an analyzer whose estimate for confirmKey is a measured
// 70 s, from Pod d0, with the switch set to on, on a clock the test moves.
func confirmAnalyzer(t *testing.T, on bool) *SaturationAnalyzer {
	t.Helper()
	a := NewSaturationAnalyzer(capacity.NewStore())
	clock := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }
	if err := a.SetSwitchesForReplay(map[string]bool{SwitchStartConfirmsByDistinctPod: on}); err != nil {
		t.Fatal(err)
	}
	a.noteReplicaStart(confirmKey, "ns", confirmVariant, confirmRows(confirmReplica("d0", 70)), logr.Discard())
	if got := a.startSeconds[confirmKey]; got != 70 {
		t.Fatalf("precondition: estimate = %v, want 70", got)
	}
	return a
}

// confirmCycle is one noteReplicaStart for confirmKey, the clock 30 s on.
func confirmCycle(a *SaturationAnalyzer, logger logr.Logger, rows ...domain.ReplicaMetrics) {
	at := a.now().Add(30 * time.Second)
	a.now = func() time.Time { return at }
	a.noteReplicaStart(confirmKey, "ns", confirmVariant, rows, logger)
}

func near(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// (a) The plan's negative control: estimate 70, one Pod whose Ready condition
// blipped reporting its age, 2700 s and growing 30 s a cycle. The parent
// folds it on cycle 4; the PR keeps 70 on as many cycles as it reports.
func TestStartConfirm_OnePodRepeatingNeverOpensTheGuard(t *testing.T) {
	run := func(on bool, cycles int) float64 {
		a := confirmAnalyzer(t, on)
		for i := range cycles {
			confirmCycle(a, logr.Discard(), confirmReplica("d0", 70), confirmReplica("blip", 2700+30*float64(i)))
		}
		return a.startSeconds[confirmKey]
	}
	if got := run(true, 60); got != 70 {
		t.Fatalf("on: estimate = %v after 60 cycles of one blipped Pod, want 70", got)
	}
	want := (1-startSecondsAlpha)*70 + startSecondsAlpha*(2700+90)
	if got := run(false, 4); !near(got, want) {
		t.Fatalf("off: estimate = %v, want the parent's %v (folded on cycle 4)", got, want)
	}
}

// (b) A one-replica variant rolled to an image that starts in 300 s, ten
// times, one Pod at a time with a surge of one: never more than two Pods up,
// and only one of them new. The run counts the Pods that left, so the third
// rollout opens it at the median, 300; pruned as they left, the estimate
// stayed at 70 for good.
func TestStartConfirm_AOneReplicaVariantLearnsASlowerImage(t *testing.T) {
	rollouts := func(a *SaturationAnalyzer) []float64 {
		var after []float64
		prev := "d0"
		for i := 1; i <= 10; i++ {
			cur := fmt.Sprintf("a%d", i)
			for range 3 {
				confirmCycle(a, logr.Discard(), confirmReplica(prev, 300), confirmReplica(cur, 300))
			}
			for range 5 {
				confirmCycle(a, logr.Discard(), confirmReplica(cur, 300))
			}
			prev = cur
			after = append(after, a.startSeconds[confirmKey])
		}
		return after
	}
	on := rollouts(confirmAnalyzer(t, true))
	if on[1] != 70 || on[2] != 300 || on[9] != 300 {
		t.Fatalf("on: estimate after each rollout = %v, want 70 until the third, then 300", on)
	}
	off := rollouts(confirmAnalyzer(t, false))
	t.Logf("on: %v; off (the parent): %v", on, off)
	if !(off[9] > 290 && off[9] < 300) {
		t.Fatalf("off: estimate after ten rollouts = %v, want the parent's 293.5", off[9])
	}
}

// (b), restored: an estimate the snapshot restores (installLocked, the only
// writer of startSeconds besides the fold; learned-state reuse lends k2 and
// ITL lines, never a start estimate) is corrected the same way. No Pod is
// counted after a restart, so the old Pod is one of the run.
func TestStartConfirm_ARestoredEstimateIsCorrectedByRollouts(t *testing.T) {
	a := NewSaturationAnalyzer(capacity.NewStore())
	a.startSeconds[confirmKey] = 70
	prev := "old"
	for i := 1; i <= 3; i++ {
		cur := fmt.Sprintf("a%d", i)
		confirmCycle(a, logr.Discard(), confirmReplica(prev, 300), confirmReplica(cur, 300))
		confirmCycle(a, logr.Discard(), confirmReplica(cur, 300))
		prev = cur
	}
	if got := a.startSeconds[confirmKey]; got != 300 {
		t.Fatalf("restored estimate = %v after the rollouts, want 300", got)
	}
}

// (c) Two steady slow Pods do not open it, however long; a third slow Pod
// arriving later does. The parent folds the two steady Pods on its own
// count (187.3 after 50 cycles).
func TestStartConfirm_TwoSteadyPodsAndALaterThirdOpenIt(t *testing.T) {
	a := confirmAnalyzer(t, true)
	for range 50 {
		confirmCycle(a, logr.Discard(), confirmReplica("s1", 300), confirmReplica("s2", 300))
	}
	if got := a.startSeconds[confirmKey]; got != 70 {
		t.Fatalf("on: estimate = %v after 50 cycles of two Pods, want 70", got)
	}
	confirmCycle(a, logr.Discard(), confirmReplica("s1", 300), confirmReplica("s2", 300), confirmReplica("s3", 300))
	if got := a.startSeconds[confirmKey]; got != 300 {
		t.Fatalf("on: estimate = %v after the third Pod, want 300", got)
	}

	off := confirmAnalyzer(t, false)
	for range 50 {
		confirmCycle(off, logr.Discard(), confirmReplica("s1", 300), confirmReplica("s2", 300))
	}
	if got := off.startSeconds[confirmKey]; math.Abs(got-187.3) > 0.05 {
		t.Fatalf("off: estimate = %v, want the parent's 187.3", got)
	}
}

// (d) A real large step, 70 to 3000 s on three Pods, reaches 3000 on the
// cycle the third is seen; folding only the Pod that made three at alpha
// stalled near 949, and the Pods, then counted, never moved it further.
func TestStartConfirm_ALargeStepReachesTheNewLevel(t *testing.T) {
	run := func(on bool) float64 {
		a := confirmAnalyzer(t, on)
		for range 30 {
			confirmCycle(a, logr.Discard(), confirmReplica("x1", 3000), confirmReplica("x2", 3000), confirmReplica("x3", 3000))
		}
		return a.startSeconds[confirmKey]
	}
	if got := run(true); got != 3000 {
		t.Fatalf("on: estimate = %v, want 3000", got)
	}
	off := run(false)
	t.Logf("off (the parent): %v", off)
	if off >= 3000 {
		t.Fatalf("off: estimate = %v, want the parent short of 3000", off)
	}
}

// The median, not the mean: a blipped Pod in the run with two real slow
// starts gives the real level.
func TestStartConfirm_TheMedianIgnoresABlipInTheRun(t *testing.T) {
	a := confirmAnalyzer(t, true)
	confirmCycle(a, logr.Discard(), confirmReplica("s1", 300), confirmReplica("blip", 2700), confirmReplica("s2", 300))
	if got := a.startSeconds[confirmKey]; got != 300 {
		t.Fatalf("estimate = %v, want the median 300", got)
	}
	// The run's Pods are counted now, the blip with them, so none is folded
	// again on the next cycle.
	for _, pod := range []string{"s1", "blip", "s2"} {
		if _, counted := a.startSeenPods[confirmKey+"|ns|"+pod]; !counted {
			t.Fatalf("%s is not counted after the run confirmed", pod)
		}
	}
	confirmCycle(a, logr.Discard(), confirmReplica("s1", 300), confirmReplica("blip", 2730), confirmReplica("s2", 300))
	if got := a.startSeconds[confirmKey]; got != 300 {
		t.Fatalf("estimate = %v on the next cycle, want 300", got)
	}
	if n := len(a.startOutlierPods[confirmKey]); n != 0 {
		t.Fatalf("the blip opened a new run (%d Pods): it was not counted", n)
	}
}

// An in-range start from a Pod not in the run ends it, applied after the
// cycle's samples are judged, so the Pods' order in the cycle does not
// matter: two Pods refused, then a cycle with a new Pod in range and the two
// still refused, then a third slow Pod with the two gone is one Pod, not
// three.
func TestStartConfirm_AStartInRangeEndsTheRunInAnyOrder(t *testing.T) {
	for _, order := range []string{"in-range first", "in-range last"} {
		a := confirmAnalyzer(t, true)
		confirmCycle(a, logr.Discard(), confirmReplica("s1", 300), confirmReplica("s2", 300))
		if order == "in-range first" {
			confirmCycle(a, logr.Discard(), confirmReplica("n1", 70), confirmReplica("s1", 300), confirmReplica("s2", 300))
		} else {
			confirmCycle(a, logr.Discard(), confirmReplica("s1", 300), confirmReplica("s2", 300), confirmReplica("n1", 70))
		}
		if _, left := a.startOutlierPods[confirmKey]; left {
			t.Fatalf("%s: the run outlived an in-range start", order)
		}
		confirmCycle(a, logr.Discard(), confirmReplica("n1", 70), confirmReplica("s3", 300))
		if got := a.startSeconds[confirmKey]; !near(got, 70) {
			t.Fatalf("%s: estimate = %v, want 70: the run restarted", order, got)
		}
	}
}

// A run Pod whose start is now in range leaves the run without ending it:
// it is no new evidence that the variant is as fast as the estimate says.
func TestStartConfirm_ARunPodBackInRangeLeavesTheRunOpen(t *testing.T) {
	a := confirmAnalyzer(t, true)
	confirmCycle(a, logr.Discard(), confirmReplica("s1", 300), confirmReplica("s2", 300))
	confirmCycle(a, logr.Discard(), confirmReplica("s1", 70), confirmReplica("s2", 300))
	if got := len(a.startOutlierPods[confirmKey]); got != 1 {
		t.Fatalf("run = %d Pods, want s2 alone", got)
	}
	confirmCycle(a, logr.Discard(), confirmReplica("s2", 300), confirmReplica("s3", 300), confirmReplica("s4", 300))
	if got := a.startSeconds[confirmKey]; got != 300 {
		t.Fatalf("estimate = %v, want 300: s2 stayed in the run", got)
	}
}

// Between cycles a run holds fewer than startConfirmPods Pods, whatever
// arrives: the cycle that brings it there opens the guard.
func TestStartConfirm_TheRunIsBounded(t *testing.T) {
	a := confirmAnalyzer(t, true)
	sample := 300.0
	for i := range 40 {
		rows := confirmRows(confirmReplica(fmt.Sprintf("p%d", i), sample))
		if i%3 == 0 {
			rows = append(rows, confirmReplica(fmt.Sprintf("q%d", i), sample))
		}
		confirmCycle(a, logr.Discard(), rows...)
		if n := len(a.startOutlierPods[confirmKey]); n >= startConfirmPods {
			t.Fatalf("cycle %d: the run holds %d Pods between cycles", i, n)
		}
		if a.startSeconds[confirmKey] >= sample {
			sample *= 4
		}
	}
}

// Refusals under one key do not count toward another's: a variant whose
// accelerator re-resolved has two keys.
func TestStartConfirm_KeysAreIsolated(t *testing.T) {
	a := confirmAnalyzer(t, true)
	other := "ns|model|" + confirmVariant + "|H200|1"
	a.noteReplicaStart(other, "ns", confirmVariant, confirmRows(confirmReplica("d0", 70)), logr.Discard())
	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		confirmRows(confirmReplica("p1", 300), confirmReplica("p2", 300)), logr.Discard())
	a.noteReplicaStart(other, "ns", confirmVariant, confirmRows(confirmReplica("q1", 300)), logr.Discard())
	if got := a.startSeconds[other]; got != 70 {
		t.Fatalf("other key = %v: it opened on the first key's refusals", got)
	}
	if got := a.startSeconds[confirmKey]; got != 70 {
		t.Fatalf("first key = %v, want 70", got)
	}
	if n := len(a.startOutlierPods[other]); n != 1 {
		t.Fatalf("other key's run = %d Pods, want q1 alone", n)
	}
}

// The rejection line names the run's distinct Pods and how many open the
// guard, with a neutral reason, once per Pod and again each
// startOutlierRelog while it is refused; the opening is logged with the
// median. Off, it is a line per refusal without the new fields.
func TestStartConfirm_TheLinesCountDistinctPods(t *testing.T) {
	run := func(on bool) []string {
		var lines []string
		logger := funcr.New(func(prefix, args string) {
			if strings.Contains(args, `"msg"="replica-start-`) && !strings.Contains(args, `"msg"="replica-start-measured"`) {
				lines = append(lines, args)
			}
		}, funcr.Options{Verbosity: 10})
		a := confirmAnalyzer(t, on)
		for range 3 {
			confirmCycle(a, logger, confirmReplica("s1", 300))
		}
		confirmCycle(a, logger, confirmReplica("s1", 300), confirmReplica("s2", 300))
		if on {
			// Ten minutes on, both are logged again; then a third opens it.
			at := a.now().Add(startOutlierRelog)
			a.now = func() time.Time { return at }
			confirmCycle(a, logger, confirmReplica("s1", 300), confirmReplica("s2", 300))
			confirmCycle(a, logger, confirmReplica("s1", 300), confirmReplica("s2", 300), confirmReplica("s3", 300))
		}
		return lines
	}

	on := run(true)
	want := []string{`"distinctPods"=1`, `"distinctPods"=2`, `"distinctPods"=2`, `"distinctPods"=2`}
	if len(on) != len(want)+1 {
		t.Fatalf("on: %d lines, want %d rejections and the confirmation: %v", len(on), len(want), on)
	}
	for i, w := range want {
		if !strings.Contains(on[i], w) || !strings.Contains(on[i], `"needed"=3`) ||
			!strings.Contains(on[i], `"reason"="start-above-bound"`) {
			t.Errorf("on: line %d = %s, want %s, \"needed\"=3 and the neutral reason", i, on[i], w)
		}
	}
	last := on[len(on)-1]
	if !strings.Contains(last, `"msg"="replica-start-change-confirmed"`) || !strings.Contains(last, `"medianSeconds"=300`) ||
		!strings.Contains(last, `"distinctPods"=3`) {
		t.Errorf("on: last line = %s, want the confirmation at a median of 300 over 3 Pods", last)
	}

	off := run(false)
	if len(off) != 3 {
		t.Fatalf("off: %d lines, want the parent's 3 (then folded): %v", len(off), off)
	}
	for _, l := range off {
		if strings.Contains(l, "distinctPods") {
			t.Errorf("off: the parent's line gained distinctPods: %s", l)
		}
	}
}

// The run goes with its variant: a sweep of a variant nothing reports drops
// it with the estimate, counted once, and a forget drops it.
func TestStartConfirm_TheRefusedPodsGoWithTheVariant(t *testing.T) {
	base := time.Now()
	a := NewSaturationAnalyzer(capacity.NewStore())
	a.now = func() time.Time { return base }
	a.noteReplicaStart(confirmKey, "ns", confirmVariant, confirmRows(confirmReplica("d0", 70)), logr.Discard())
	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		confirmRows(confirmReplica("d0", 70), confirmReplica("s1", 300)), logr.Discard())
	if len(a.startOutlierPods[confirmKey]) != 1 {
		t.Fatalf("precondition: s1 was refused")
	}
	// The estimate is gone (as a stale variant's would be after another
	// sweep), so only the belt loops can reach the run: one key, one count.
	delete(a.startSeconds, confirmKey)
	a.now = func() time.Time { return base.Add(2 * capacity.HistoryEvictionTimeout) }
	ev := a.EvictStaleHistory()
	if _, left := a.startOutlierPods[confirmKey]; left {
		t.Fatalf("the sweep left the run of a variant nothing reports")
	}
	if ev.StartOutliers != 1 {
		t.Fatalf("StartOutliers = %d, want the one key counted once", ev.StartOutliers)
	}

	f := NewSaturationAnalyzer(capacity.NewStore())
	f.noteReplicaStart(confirmKey, "ns", confirmVariant,
		confirmRows(confirmReplica("d0", 70), confirmReplica("s1", 300)), logr.Discard())
	if len(f.startOutlierPods[confirmKey]) != 1 {
		t.Fatalf("precondition: s1 was refused")
	}
	f.mu.Lock()
	f.dropVariantLocked("ns", "model", confirmVariant)
	f.mu.Unlock()
	if _, left := f.startOutlierPods[confirmKey]; left {
		t.Fatalf("a forget of the variant left its run")
	}
}

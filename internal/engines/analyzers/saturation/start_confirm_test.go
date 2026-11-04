package saturation

import (
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
// estimate, never on one Pod repeating. Each test runs the switch on, and the
// ones the switch decides also run it off and assert the parent's figure, so
// each fails with SwitchStartConfirmsByDistinctPod off.

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

// confirmAnalyzer is an analyzer whose estimate for confirmKey is a measured
// 70 s, from Pod d0, with the switch set to on.
func confirmAnalyzer(t *testing.T, on bool) *SaturationAnalyzer {
	t.Helper()
	a := NewSaturationAnalyzer(capacity.NewStore())
	if err := a.SetSwitchesForReplay(map[string]bool{SwitchStartConfirmsByDistinctPod: on}); err != nil {
		t.Fatal(err)
	}
	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("d0", 70)}, logr.Discard())
	if got := a.startSeconds[confirmKey]; got != 70 {
		t.Fatalf("precondition: estimate = %v, want 70", got)
	}
	return a
}

func near(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// The plan's negative control: estimate 70, one Pod reporting 2700 s (a
// readiness blip 45 minutes in) on four cycles. The parent folds it on cycle
// 4, to 0.7*70 + 0.3*2700 = 859; the PR keeps 70, on as many cycles as the
// Pod reports.
func TestStartConfirm_OnePodRepeatingNeverOpensTheGuard(t *testing.T) {
	blip := []domain.ReplicaMetrics{confirmReplica("d0", 70), confirmReplica("blip", 2700)}

	on := confirmAnalyzer(t, true)
	for range 20 {
		on.noteReplicaStart(confirmKey, "ns", confirmVariant, blip, logr.Discard())
	}
	if got := on.startSeconds[confirmKey]; got != 70 {
		t.Fatalf("on: estimate = %v after 20 cycles of one blipped Pod, want 70", got)
	}
	if n := len(on.startOutlierPods[confirmKey]); n != 1 {
		t.Fatalf("on: %d refused Pods, want the one", n)
	}

	off := confirmAnalyzer(t, false)
	for range 4 {
		off.noteReplicaStart(confirmKey, "ns", confirmVariant, blip, logr.Discard())
	}
	if got := off.startSeconds[confirmKey]; !near(got, 859) {
		t.Fatalf("off: estimate = %v, want the parent's 859 (folded on cycle 4)", got)
	}
}

// Three distinct Pods over 3x open it: the first two are refused, and the
// third is admitted and folded at alpha. The run then ends, and the first two,
// reporting again, are measured against the new estimate (139), which their
// 300 s no longer exceeds threefold.
func TestStartConfirm_DistinctPodsOpenTheGuard(t *testing.T) {
	a := confirmAnalyzer(t, true)
	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("s1", 300), confirmReplica("s2", 300)}, logr.Discard())
	if got := a.startSeconds[confirmKey]; got != 70 {
		t.Fatalf("estimate = %v after two distinct Pods, want 70: the guard needs %d", got, startConfirmPods)
	}

	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("s1", 300), confirmReplica("s2", 300), confirmReplica("s3", 300)},
		logr.Discard())
	// s1 and s2 are refused again (still two distinct), s3 makes three and is
	// folded; that ends the run.
	want := (1-startSecondsAlpha)*70 + startSecondsAlpha*300
	if got := a.startSeconds[confirmKey]; !near(got, want) {
		t.Fatalf("estimate = %v after the third distinct Pod, want %v", got, want)
	}
	if _, left := a.startOutlierPods[confirmKey]; left {
		t.Fatalf("the refused Pods outlived the admission that ended their run")
	}

	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("s1", 300), confirmReplica("s2", 300), confirmReplica("s3", 300)},
		logr.Discard())
	want = (1-startSecondsAlpha)*want + startSecondsAlpha*300
	want = (1-startSecondsAlpha)*want + startSecondsAlpha*300
	if got := a.startSeconds[confirmKey]; !near(got, want) {
		t.Fatalf("estimate = %v, want %v: s1 and s2 fold against the new estimate", got, want)
	}

	// Off, the parent counts refusals, not Pods: s1 and s2, then s3 alone,
	// are three refusals in a row, and its guard is still shut.
	off := confirmAnalyzer(t, false)
	off.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("s1", 300), confirmReplica("s2", 300)}, logr.Discard())
	off.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("s3", 300)}, logr.Discard())
	if got := off.startSeconds[confirmKey]; got != 70 {
		t.Fatalf("off: estimate = %v, want 70: the parent refuses three in a row whoever they are", got)
	}
}

// A refused Pod that leaves is no longer evidence: the set is pruned with the
// Pods, as startSeenPods is, so two Pods that came and went and one new one
// are not three.
func TestStartConfirm_APodThatLeavesIsNoLongerCounted(t *testing.T) {
	a := confirmAnalyzer(t, true)
	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("s1", 300), confirmReplica("s2", 300)}, logr.Discard())
	// s1 and s2 are gone (scaled down); s3 reports.
	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("s3", 300)}, logr.Discard())
	if got := a.startSeconds[confirmKey]; got != 70 {
		t.Fatalf("estimate = %v, want 70: s1 and s2 left, so s3 is one Pod, not the third", got)
	}
	pods := a.startOutlierPods[confirmKey]
	if len(pods) != 1 {
		t.Fatalf("refused Pods = %v, want s3 alone", pods)
	}
	if _, ok := pods[confirmKey+"|ns|s3"]; !ok {
		t.Fatalf("refused Pods = %v, want s3", pods)
	}

	// Every refused Pod gone: the key's set goes too.
	a.noteReplicaStart(confirmKey, "ns", confirmVariant, nil, logr.Discard())
	if _, left := a.startOutlierPods[confirmKey]; left {
		t.Fatalf("an empty set of refused Pods was kept")
	}
}

// An admitted sample in range ends the run, as it reset the parent's counter:
// the guard needs consecutive evidence, so two Pods refused, then a new Pod
// starting at the estimate, then one more refused is one Pod, not three.
func TestStartConfirm_AStartInRangeEndsTheRun(t *testing.T) {
	a := confirmAnalyzer(t, true)
	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("s1", 300), confirmReplica("s2", 300)}, logr.Discard())
	// s1 and s2 stay, not Ready (no sample), so the prune keeps whatever is
	// remembered of them: only the end of the run forgets them.
	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("s1", 0), confirmReplica("s2", 0), confirmReplica("n1", 70)},
		logr.Discard())
	if _, left := a.startOutlierPods[confirmKey]; left {
		t.Fatalf("a start in range left the refused Pods in place")
	}
	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("s1", 0), confirmReplica("s2", 0), confirmReplica("s3", 300)},
		logr.Discard())
	if got := a.startSeconds[confirmKey]; got != 70 {
		t.Fatalf("estimate = %v, want 70: the run restarted at n1", got)
	}
}

// The rejection line names the distinct Pods and how many open the guard, and
// is logged once per Pod in a run, not once per cycle the Pod reports. Off, it
// is the parent's line, once per refusal, without the new fields.
func TestStartConfirm_TheRejectionLineCountsDistinctPods(t *testing.T) {
	run := func(on bool) []string {
		var lines []string
		logger := funcr.New(func(prefix, args string) {
			if strings.Contains(args, `"msg"="replica-start-outlier-rejected"`) {
				lines = append(lines, args)
			}
		}, funcr.Options{Verbosity: 10})
		a := confirmAnalyzer(t, on)
		for range 3 {
			a.noteReplicaStart(confirmKey, "ns", confirmVariant,
				[]domain.ReplicaMetrics{confirmReplica("s1", 300)}, logger)
		}
		a.noteReplicaStart(confirmKey, "ns", confirmVariant,
			[]domain.ReplicaMetrics{confirmReplica("s1", 300), confirmReplica("s2", 300)}, logger)
		return lines
	}

	on := run(true)
	if len(on) != 2 {
		t.Fatalf("on: %d rejection lines, want 2 (s1 once, s2 once): %v", len(on), on)
	}
	for i, want := range []string{`"distinctPods"=1`, `"distinctPods"=2`} {
		if !strings.Contains(on[i], want) || !strings.Contains(on[i], `"needed"=3`) {
			t.Errorf("on: line %d = %s, want %s and \"needed\"=3", i, on[i], want)
		}
	}

	off := run(false)
	if len(off) != 3 {
		t.Fatalf("off: %d rejection lines, want the parent's 3 (then folded): %v", len(off), off)
	}
	for _, l := range off {
		if strings.Contains(l, "distinctPods") {
			t.Errorf("off: the parent's line gained distinctPods: %s", l)
		}
	}
}

// The refused Pods go with their variant: a sweep of a variant nothing
// reports drops them with the estimate, and a forget drops them.
func TestStartConfirm_TheRefusedPodsGoWithTheVariant(t *testing.T) {
	base := time.Now()
	a := NewSaturationAnalyzer(capacity.NewStore())
	a.now = func() time.Time { return base }
	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("d0", 70)}, logr.Discard())
	a.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("d0", 70), confirmReplica("s1", 300)}, logr.Discard())
	if len(a.startOutlierPods[confirmKey]) != 1 {
		t.Fatalf("precondition: s1 was refused")
	}
	a.now = func() time.Time { return base.Add(2 * capacity.HistoryEvictionTimeout) }
	ev := a.EvictStaleHistory()
	if _, left := a.startOutlierPods[confirmKey]; left {
		t.Fatalf("the sweep left the refused Pods of a variant nothing reports")
	}
	if !ev.Any() {
		t.Fatalf("the sweep evicted the start state and counted nothing")
	}

	f := NewSaturationAnalyzer(capacity.NewStore())
	f.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("d0", 70), confirmReplica("s1", 300)}, logr.Discard())
	f.noteReplicaStart(confirmKey, "ns", confirmVariant,
		[]domain.ReplicaMetrics{confirmReplica("d0", 70), confirmReplica("s1", 300)}, logr.Discard())
	if len(f.startOutlierPods[confirmKey]) != 1 {
		t.Fatalf("precondition: s1 was refused")
	}
	f.mu.Lock()
	f.dropVariantLocked("ns", "model", confirmVariant)
	f.mu.Unlock()
	if _, left := f.startOutlierPods[confirmKey]; left {
		t.Fatalf("a forget of the variant left its refused Pods")
	}
}

package steadystate

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest/observer"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
)

// historyMap reads the need history ConfigMap; nil when absent.
func historyMap(t *testing.T, c client.Client) *corev1.ConfigMap {
	t.Helper()
	var cm corev1.ConfigMap
	err := c.Get(context.Background(), client.ObjectKey{Namespace: config.SystemNamespace(), Name: shareHistoryConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return &cm
}

// observeEngine captures the engine's logs from here on.
func observeEngine(t *testing.T, se *shareEngine) *observer.ObservedLogs {
	t.Helper()
	var logs *observer.ObservedLogs
	se.ctx, logs = observe(t)
	return logs
}

// capped is whether the engine's single group had its headroom capped when it
// last planned, and whether it has planned since its ledger was built.
func (se *shareEngine) capped() (capped, planned bool) {
	for _, c := range se.e.utilizationShare.headroomCapped {
		return c, true
	}
	return false, false
}

// untilUncapped cycles until the planner, its peaks known, finds that they fit
// and lifts the cap.
func (se *shareEngine) untilUncapped() {
	se.t.Helper()
	for range 1000 {
		se.cycle()
		if c, planned := se.capped(); planned && !c {
			return
		}
	}
	se.t.Fatal("setup: the cap never came off; the fleet's peaks do not fit, so a restart cannot show anything")
}

// firstPlan cycles a newly built engine through its quiet period to its first
// planned cycle, and returns whether that cycle capped the headroom.
func (se *shareEngine) firstPlan() bool {
	se.t.Helper()
	for range 40 {
		se.cycle()
		if c, planned := se.capped(); planned {
			return c
		}
	}
	se.t.Fatal("no planned cycle after the quiet period")
	return false
}

// After a restart the ledger restores the need history the old controller
// stored, so the peaks it knew are known at once: the cap is not forced on for
// a window where the peaks fit.
func TestUtilizationShareHistorySurvivesARestart(t *testing.T) {
	f := newShareFleet()
	c := sharePods(t, f)
	se := newShareEngine(t, f, c, time.Unix(0, 0))
	se.untilUncapped()
	for range 4 { // past the write interval
		se.cycle()
	}
	cm := historyMap(t, c)
	if cm == nil || len(cm.Data) != 1 {
		t.Fatalf("history ConfigMap after a window of acting: %+v, want one group's entry", cm)
	}

	// Without the store a restart caps: the control that this fleet needs
	// the history at all.
	blind := newShareEngine(t, f, sharePods(t, f), se.clock.Add(time.Minute))
	if !blind.firstPlan() {
		t.Fatal("setup: a restart with no stored history did not cap; nothing to restore")
	}

	restarted := newShareEngine(t, f, c, se.clock.Add(time.Minute))
	logs := observeEngine(t, restarted)
	if restarted.firstPlan() {
		t.Fatal("restarted with the stored history, the first plan capped the headroom: the peaks were not restored")
	}
	if logs.FilterMessageSnippet("need history restored").Len() != 1 {
		t.Fatal("want the restore logged once")
	}
}

// A stored entry is used only when it is this format's version and was written
// for this group; a key is a valid ConfigMap data key whatever the group.
func TestUtilizationShareHistoryDecodeValidates(t *testing.T) {
	const group = "|A100"
	h := allocation.ShareNeedHistory{"ns/A/both": {Since: time.Unix(0, 0),
		Samples: []allocation.ShareNeedSample{{At: time.Unix(60, 0), Need: 3}}}}
	raw, _, err := encodeShareHistory(group, h, time.Unix(60, 0), allocation.ShareTimings{SwingWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	got, _, why := decodeShareHistory(raw, group)
	if why != "" || len(got) != 1 || got["ns/A/both"].Samples[0].Need != 3 || !got["ns/A/both"].Samples[0].At.Equal(time.Unix(60, 0)) {
		t.Fatalf("round trip: %+v (%s)", got, why)
	}
	if _, _, why := decodeShareHistory(raw, "ns|A100"); why == "" {
		t.Fatal("another group's entry was used")
	}
	if _, _, why := decodeShareHistory(strings.Replace(raw, `"version":1`, `"version":2`, 1), group); why == "" {
		t.Fatal("an entry of an unknown version was used")
	}
	if _, _, why := decodeShareHistory("{", group); why == "" {
		t.Fatal("an unparseable entry was used")
	}
	for _, g := range []string{"|A100", "team/a|NVIDIA H200", strings.Repeat("x", 400)} {
		if errs := validation.IsConfigMapKey(shareHistoryKey(g)); len(errs) != 0 {
			t.Fatalf("key for %q is not a valid data key: %v", g, errs)
		}
	}
	if shareHistoryKey("a|b") == shareHistoryKey("a_b") {
		t.Fatal("two groups share a key")
	}
}

// forbidHistory makes every read of the history ConfigMap Forbidden.
func forbidHistory(c client.Client) client.Client {
	return interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok && key.Name == shareHistoryConfigMap {
				return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, key.Name, nil)
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	})
}

// A Forbidden read is not an empty history: nothing is restored -- the ledger
// starts as it would with no store -- the failure is logged once, at Error,
// and the history the read could not see is not overwritten.
func TestUtilizationShareForbiddenHistoryIsNotEmpty(t *testing.T) {
	f := newShareFleet()
	c := sharePods(t, f)
	se := newShareEngine(t, f, c, time.Unix(0, 0))
	se.untilUncapped()
	for range 4 {
		se.cycle()
	}
	before := historyMap(t, c)
	if before == nil {
		t.Fatal("setup: no history stored")
	}

	restarted := newShareEngine(t, f, forbidHistory(c), se.clock.Add(time.Minute))
	logs := observeEngine(t, restarted)
	if !restarted.firstPlan() {
		t.Fatal("history restored through a Forbidden read")
	}
	for range 10 { // the read is retried every minute
		restarted.cycle()
	}
	failed := logs.FilterMessageSnippet("could not read the stored need history")
	if failed.Len() != 1 || failed.All()[0].Level.String() != "error" {
		t.Fatalf("Forbidden read logged %d times (%v), want once at error", failed.Len(), failed.All())
	}
	if after := historyMap(t, c); after.ResourceVersion != before.ResourceVersion {
		t.Fatal("the history was written over although it could not be read")
	}
}

// Deleting the ConfigMap is the flush: the running controller forgets its
// in-memory history too, rather than writing it straight back.
func TestUtilizationShareDeletingTheHistoryFlushesIt(t *testing.T) {
	f := newShareFleet()
	c := sharePods(t, f)
	se := newShareEngine(t, f, c, time.Unix(0, 0))
	se.untilUncapped()
	for range 4 {
		se.cycle()
	}
	cm := historyMap(t, c)
	if cm == nil {
		t.Fatal("setup: no history stored")
	}
	if err := c.Delete(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	logs := observeEngine(t, se)
	capped := false
	for range 6 {
		se.cycle()
		if c, _ := se.capped(); c {
			capped = true
		}
	}
	if logs.FilterMessageSnippet("need history ConfigMap was deleted").Len() != 1 {
		t.Fatal("want the flush logged once")
	}
	if !capped {
		t.Fatal("after the flush the peaks are still known: the cap stayed off")
	}
}

// A group dropped as vanished takes its stored history with it.
func TestUtilizationShareVanishedGroupDeletesItsHistory(t *testing.T) {
	f := newShareFleet()
	c := sharePods(t, f)
	se := newShareEngine(t, f, c, time.Unix(0, 0))
	for range 10 {
		se.cycle()
	}
	if cm := historyMap(t, c); cm == nil || len(cm.Data) != 1 {
		t.Fatalf("setup: history %+v, want one entry", cm)
	}
	for range 10 { // the group's models are gone, past the absence grace
		se.clock = se.clock.Add(30 * time.Second)
		se.e.evaluateUtilizationShare(se.ctx, nil, fullQuota(), nil)
	}
	if cm := historyMap(t, c); cm == nil || len(cm.Data) != 0 {
		t.Fatalf("vanished group's history kept: %+v", cm)
	}
}

package balance

import (
	"math"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

func row(pod, variant string, kv float64, queue int) domain.ReplicaMetrics {
	return domain.ReplicaMetrics{PodName: pod, VariantName: variant, KvCacheUsage: kv, QueueLength: queue, Ready: true}
}

var pd = map[string]string{"pre": domain.RolePrefill, "dec": domain.RoleDecode}

// The measured case (docs/reference/router-configuration.md): 439 requests
// queued on one prefill replica while three sit idle.
func TestMeasureFlagsOneQueuedPrefillBesideIdleOnes(t *testing.T) {
	d := Measure([]domain.ReplicaMetrics{
		row("pre-0", "pre", 0.20, 439),
		row("pre-1", "pre", 0.01, 0),
		row("pre-2", "pre", 0.00, 0),
		row("pre-3", "pre", 0.02, 0),
	}, pd, 5)[domain.RolePrefill]

	if d.Ready != 4 || d.Saturated != 1 || d.Idle != 3 {
		t.Fatalf("ready/saturated/idle = %d/%d/%d, want 4/1/3", d.Ready, d.Saturated, d.Idle)
	}
	if !d.Imbalanced() || d.IdleWhileSaturated() != 3 {
		t.Errorf("Imbalanced=%v IdleWhileSaturated=%d, want true and 3", d.Imbalanced(), d.IdleWhileSaturated())
	}
	if r, ok := d.QueueMaxToMean(); !ok || r != 4 {
		t.Errorf("queue max/mean = %v (ok %v), want 4: every waiting request on one of four", r, ok)
	}
	if d.BusiestPod != "pre-0" {
		t.Errorf("busiest pod %q, want pre-0", d.BusiestPod)
	}
	if want := []string{"pre-1", "pre-2", "pre-3"}; len(d.IdlePods) != 3 || d.IdlePods[0] != want[0] || d.IdlePods[2] != want[2] {
		t.Errorf("idle pods %v, want %v", d.IdlePods, want)
	}
}

// The same total load, spread: nothing saturated, nothing to warn about, and
// both ratios at one.
func TestMeasureReadsAnEvenRoleAsBalanced(t *testing.T) {
	d := Measure([]domain.ReplicaMetrics{
		row("pre-0", "pre", 0.06, 110),
		row("pre-1", "pre", 0.06, 110),
		row("pre-2", "pre", 0.06, 110),
		row("pre-3", "pre", 0.06, 109),
	}, pd, 5)[domain.RolePrefill]
	if d.Imbalanced() || d.Idle != 0 {
		t.Errorf("an evenly queued role read imbalanced: saturated %d idle %d", d.Saturated, d.Idle)
	}
	if r, _ := d.KvMaxToMean(); math.Abs(r-1) > 1e-9 {
		t.Errorf("kv max/mean = %v, want 1", r)
	}
}

// A decode replica's waiting count includes requests waiting for their remote
// KV, so a queue alone does not make it saturated; its KV does.
func TestMeasureJudgesDecodeOnKvAlone(t *testing.T) {
	dists := Measure([]domain.ReplicaMetrics{
		row("dec-0", "dec", 0.40, 50),
		row("dec-1", "dec", 0.10, 0),
	}, pd, 5)
	if d := dists[domain.RoleDecode]; d.Saturated != 0 || d.Imbalanced() {
		t.Errorf("decode queued at 0.40 KV read saturated: %+v", d)
	}
	dists = Measure([]domain.ReplicaMetrics{
		row("dec-0", "dec", 0.95, 0),
		row("dec-1", "dec", 0.10, 0),
	}, pd, 5)
	if d := dists[domain.RoleDecode]; d.Saturated != 1 || !d.Imbalanced() {
		t.Errorf("decode at 0.95 KV beside an idle one not flagged: %+v", d)
	}
}

// A pod of several engine ranks is judged on its busiest rank: one full rank
// among idle ones is saturated, whatever the pod's mean.
func TestMeasureReadsTheBusiestRank(t *testing.T) {
	hot := row("agg-0", "agg", 0.15, 0)
	hot.EngineRanks, hot.MaxRankKvCacheUsage = 8, 0.95
	d := Measure([]domain.ReplicaMetrics{hot, row("agg-1", "agg", 0.05, 0)}, nil, 5)[domain.RoleBoth]
	if d.Saturated != 1 || d.Idle != 1 || d.MaxKvUsage != 0.95 {
		t.Errorf("busiest rank not read: %+v", d)
	}
}

// Not Ready is not routed to; a NaN or out-of-range reading is no load.
func TestMeasureSkipsUnreadyAndBadReadings(t *testing.T) {
	notReady := row("agg-2", "agg", 0.99, 40)
	notReady.Ready = false
	d := Measure([]domain.ReplicaMetrics{
		row("agg-0", "agg", math.NaN(), 0),
		row("agg-1", "agg", 1.7, 0),
		notReady,
	}, nil, 5)[domain.RoleBoth]
	if d.Ready != 2 {
		t.Fatalf("ready %d, want 2 (the not-Ready pod left out)", d.Ready)
	}
	if d.MaxKvUsage != 1 || d.MeanKvUsage != 0.5 {
		t.Errorf("max %v mean %v, want 1 and 0.5 (NaN read as 0, 1.7 clamped to 1)", d.MaxKvUsage, d.MeanKvUsage)
	}
}

func TestRatiosAbsentWhereTheySayNothing(t *testing.T) {
	one := Measure([]domain.ReplicaMetrics{row("a", "agg", 0.5, 3)}, nil, 5)[domain.RoleBoth]
	if _, ok := one.KvMaxToMean(); ok {
		t.Error("a ratio over one replica was published")
	}
	idle := Measure([]domain.ReplicaMetrics{row("a", "agg", 0, 0), row("b", "agg", 0, 0)}, nil, 5)[domain.RoleBoth]
	if _, ok := idle.KvMaxToMean(); ok {
		t.Error("a kv ratio over a mean of zero was published")
	}
	if _, ok := idle.QueueMaxToMean(); ok {
		t.Error("a queue ratio with nothing waiting was published")
	}
}

func TestQueueThresholdOffDisablesTheQueueTest(t *testing.T) {
	d := Measure([]domain.ReplicaMetrics{row("a", "agg", 0.2, 100), row("b", "agg", 0.0, 0)}, nil, 0)[domain.RoleBoth]
	if d.Saturated != 0 {
		t.Errorf("saturated on its queue with queueLengthThreshold 0: %+v", d)
	}
}

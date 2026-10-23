package saturation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// Uneven routing: the router loads one replica of a role while a sibling of
// the same role sits idle. What the analyzer learns, measures and prices must
// not move with it -- and must not move either when the fleet really is short,
// every replica of the role busy. Each test below checks both sides.
//
// The fixtures use the shipped test policy (makeAnalyzerInput: kvCacheThreshold
// 0.8, queueLengthThreshold 5, scale-up 0.85) and a 1,162,240-token cache, the
// figures of the load-imbalance audit (2026-10-23) these tests came from.

const imbKv = int64(1_162_240)

func imbAnalyzer() (*SaturationAnalyzer, *time.Time) {
	a := NewSaturationAnalyzer(capacity.NewStore())
	clock := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }
	return a, &clock
}

// imbRow is one replica at KV fraction frac (its one-minute peak and, unless
// the test sets it apart, its instant reading) with queue waiting.
func imbRow(pod, variant string, frac float64, queue int, in, out, reqRate float64) domain.ReplicaMetrics {
	rm := makeReplicaMetrics(pod, variant, int64(frac*float64(imbKv)), imbKv, queue, in, out)
	rm.KvUsageInstant = frac
	rm.RequestRate = reqRate
	rm.GenerationTokenRate = reqRate * out
	rm.PrefillComputedTokenRate = reqRate * in
	return rm
}

func imbVariant(t *testing.T, r *domain.AnalyzerResult, v string) domain.VariantCapacity {
	t.Helper()
	for _, vc := range r.VariantCapacities {
		if vc.VariantName == v {
			return vc
		}
	}
	t.Fatalf("no variant %s in the result", v)
	return domain.VariantCapacity{}
}

// imbK2Windows counts the k2 windows holding a reading for role ("" for any).
func imbK2Windows(a *SaturationAnalyzer, role string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for k, ra := range a.computeCapacityHistory {
		if (role == "" || strings.Contains(k, "|"+role+"|")) && ra.Len() > 0 {
			n++
		}
	}
	return n
}

// One decode replica full and queued while its three siblings idle is not
// decode saturated: prefill learns, and its demand is not held. Every decode
// replica full and queued still is, and one full among busy-but-not-full
// siblings still is too -- only an idle sibling is room.
func TestImbalanceDecodeSaturationNeedsNoIdleSibling(t *testing.T) {
	states := []domain.VariantReplicaState{
		{VariantName: "decode-v", Role: domain.RoleDecode, AcceleratorName: "H200", CurrentReplicas: 4, GPUsPerReplica: 1},
		{VariantName: "prefill-v", Role: domain.RolePrefill, AcceleratorName: "H200", CurrentReplicas: 1, GPUsPerReplica: 1},
	}
	roles := map[string]string{"decode-v": domain.RoleDecode, "prefill-v": domain.RolePrefill}
	decode := func(fracs []float64, queues []int) []domain.ReplicaMetrics {
		var rows []domain.ReplicaMetrics
		for i, f := range fracs {
			rows = append(rows, imbRow("decode-"+string(rune('0'+i)), "decode-v", f, queues[i], 6000, 1000, 0.3))
		}
		return rows
	}
	run := func(name string, rows []domain.ReplicaMetrics, wantSaturated bool) {
		t.Helper()
		a, _ := imbAnalyzer()
		rows = append(rows, imbRow("prefill-0", "prefill-v", 0.35, 30, 6000, 1, 4.77))
		sat := roleSaturated(rows, roles, domain.RoleDecode, 5, 0.8)
		r, err := a.Analyze(context.Background(), makeAnalyzerInput(rows, states))
		if err != nil {
			t.Fatal(err)
		}
		_, held := r.HeldRoleDemand[domain.RolePrefill]
		n := imbK2Windows(a, domain.RolePrefill)
		t.Logf("%s: decodeSaturated=%v prefillK2Windows=%d prefillHeld=%v prefillDemand=%.0f",
			name, sat, n, held, r.RoleDemand[domain.RolePrefill])
		if sat != wantSaturated {
			t.Errorf("%s: decode saturated = %v, want %v", name, sat, wantSaturated)
		}
		if wantSaturated && (n != 0 || !held) {
			t.Errorf("%s: decode saturated, yet prefill recorded %d k2 windows (want 0) and held=%v (want true)", name, n, held)
		}
		if !wantSaturated && (n != 1 || held) {
			t.Errorf("%s: decode has room, yet prefill recorded %d k2 windows (want 1) and held=%v (want false)", name, n, held)
		}
	}
	run("imbalanced", decode([]float64{0.95, 0.02, 0.02, 0.02}, []int{20, 0, 0, 0}), false)
	run("uniformly saturated", decode([]float64{0.95, 0.95, 0.95, 0.95}, []int{20, 20, 20, 20}), true)
	run("full among busy", decode([]float64{0.95, 0.5, 0.5, 0.5}, []int{20, 0, 0, 0}), true)
	run("balanced, not full", decode([]float64{0.2525, 0.2525, 0.2525, 0.2525}, []int{0, 0, 0, 0}), false)
}

// replicaIdle is the balance package's idle: Ready, busiest-rank KV under 0.30,
// nothing waiting.
func TestImbalanceReplicaIdleMatchesBalance(t *testing.T) {
	row := func(frac float64, queue int) domain.ReplicaMetrics {
		return imbRow("p", "v", frac, queue, 100, 100, 0)
	}
	for _, c := range []struct {
		name string
		rm   domain.ReplicaMetrics
		want bool
	}{
		{"empty", row(0, 0), true},
		{"just under", row(0.2999, 0), true},
		{"at the line", row(0.30, 0), false},
		{"queued at low KV", row(0.05, 1), false},
		{"not Ready", func() domain.ReplicaMetrics { r := row(0, 0); r.Ready = false; return r }(), false},
		{"busiest rank over the line", func() domain.ReplicaMetrics {
			r := row(0.1, 0)
			r.EngineRanks, r.MaxRankKvCacheUsage = 2, 0.4
			return r
		}(), false},
		{"tokens with no utilization", func() domain.ReplicaMetrics {
			r := row(0.5, 0)
			r.KvCacheUsage = 0
			return r
		}(), false},
	} {
		if got := replicaIdle(c.rm); got != c.want {
			t.Errorf("%s: idle = %v, want %v", c.name, got, c.want)
		}
	}
}

// imbFloorFleet is an aggregated variant of four replicas whose measured mu is
// about 1 req/s, learned over four saturated cycles a minute apart, and the
// analyzer handed back for one more cycle at lambda 2 -- half the fleet's
// throughput.
func imbFloorFleet(t *testing.T, rows []domain.ReplicaMetrics) (implied float64) {
	t.Helper()
	states := []domain.VariantReplicaState{
		{VariantName: "agg-v", AcceleratorName: "H200", CurrentReplicas: 4, GPUsPerReplica: 1},
	}
	a, clock := imbAnalyzer()
	for i := 0; i < 4; i++ {
		var seed []domain.ReplicaMetrics
		for _, p := range []string{"r0", "r1", "r2", "r3"} {
			// Distinct readings per cycle, so each counts as a sample.
			seed = append(seed, imbRow(p, "agg-v", 0.95, 10, 1000, 1000, []float64{1.0, 1.02, 0.98, 1.01}[i]))
		}
		in := makeAnalyzerInput(seed, states)
		in.ArrivalRate = 4.0
		if _, err := a.Analyze(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		*clock = clock.Add(ThroughputSampleSpacing + time.Second)
	}
	in := makeAnalyzerInput(rows, states)
	in.ArrivalRate = 2.0
	r, err := a.Analyze(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return r.TotalDemand / (0.85 * imbVariant(t, r, "agg-v").PerReplicaCapacity)
}

// imbFleet is four agg-v replicas r0..r3 at the given KV fractions, queues and
// request rates.
func imbFleet(fracs []float64, queues []int, rates []float64) []domain.ReplicaMetrics {
	var rows []domain.ReplicaMetrics
	for i := range fracs {
		rows = append(rows, imbRow("r"+string(rune('0'+i)), "agg-v", fracs[i], queues[i], 1000, 1000, rates[i]))
	}
	return rows
}

// A queue the router pinned on one replica, while a sibling idles, is not a
// backlog the role can be sized for: replicas added cannot drain another
// engine's queue. With every replica busy it is, and counts as before.
func TestImbalanceFloorBacklogLeavesAPinnedQueueOut(t *testing.T) {
	light := []float64{0.10, 0.10, 0.10}
	balanced := imbFloorFleet(t, imbFleet([]float64{0.3125, 0.3125, 0.3125, 0.3125}, []int{0, 0, 0, 0}, []float64{0.5, 0.5, 0.5, 0.5}))
	q100 := imbFloorFleet(t, imbFleet(append([]float64{0.95}, light...), []int{100, 0, 0, 0}, []float64{1.0, 0.33, 0.33, 0.34}))
	q300 := imbFloorFleet(t, imbFleet(append([]float64{0.95}, light...), []int{300, 0, 0, 0}, []float64{1.0, 0.33, 0.33, 0.34}))
	busyQ0 := imbFloorFleet(t, imbFleet([]float64{0.95, 0.95, 0.95, 0.95}, []int{0, 0, 0, 0}, []float64{0.5, 0.5, 0.5, 0.5}))
	busyQ100 := imbFloorFleet(t, imbFleet([]float64{0.95, 0.95, 0.95, 0.95}, []int{100, 100, 100, 100}, []float64{0.5, 0.5, 0.5, 0.5}))
	hotAmongBusy := imbFloorFleet(t, imbFleet([]float64{0.95, 0.5, 0.5, 0.5}, []int{100, 0, 0, 0}, []float64{1.0, 0.33, 0.33, 0.34}))
	t.Logf("implied replicas: balanced %.2f; one replica queued 100 / 300 beside idle siblings %.2f / %.2f", balanced, q100, q300)
	t.Logf("implied replicas: every replica full, queue 0 / 100 each %.2f / %.2f; queued 100 beside busy siblings %.2f",
		busyQ0, busyQ100, hotAmongBusy)
	for name, got := range map[string]float64{"queue 100": q100, "queue 300": q300} {
		if got > balanced+0.5 {
			t.Errorf("%s pinned on one replica beside idle siblings implies %.2f replicas, balanced %.2f", name, got, balanced)
		}
	}
	if !(busyQ100 > busyQ0+1) {
		t.Errorf("every replica full and queued 100 implies %.2f replicas, no more than queue 0's %.2f: the backlog of a short fleet was dropped",
			busyQ100, busyQ0)
	}
	if !(hotAmongBusy > balanced+0.5) {
		t.Errorf("a queue beside busy (not idle) siblings implies %.2f replicas, no more than balanced %.2f: only an idle sibling may drop it",
			hotAmongBusy, balanced)
	}
}

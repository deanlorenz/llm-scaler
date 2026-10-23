package saturation

import (
	"context"
	"math"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// A replica that has only just turned Ready reads idle: its one-minute peak
// covers the seconds since it came up. A fleet scaling up lands replicas one
// at a time, so it nearly always has one, and the short fleet's backlog must
// not go with it. Every original replica is full and queued here; the fresh
// fourth adds nothing and takes nothing away.
func TestIdleRoomFreshReplicaKeepsTheBacklog(t *testing.T) {
	full := []float64{0.95, 0.95, 0.95}
	f0 := imbFloorFleetAt(t, imbFleet(full, []int{60, 60, 60}, []float64{1, 1, 1}), 5, false)
	f1 := imbFloorFleetAt(t, imbFleet(append(full, 0), []int{60, 60, 60, 0}, []float64{1, 1, 1, 0}), 5, false)
	f2 := imbFloorFleetAt(t, imbFleet(append(full, 0.31), []int{60, 60, 60, 0}, []float64{1, 1, 1, 0.3}), 5, false)
	t.Logf("implied replicas at lambda 5, mu ~1: three full queued 60 %.2f; plus a fourth just Ready %.2f; plus a fourth at 0.31 KV %.2f",
		f0, f1, f2)
	if f1 < f0-0.01 {
		t.Errorf("a replica Ready for one cycle dropped the short fleet's order from %.2f to %.2f replicas", f0, f1)
	}

	// The occupancy path, with no mu on record: the fresh replica's queue of
	// nothing must not take the others' queues out of their charge.
	occ := func(fracs []float64, queues []int) float64 {
		t.Helper()
		a, _ := imbAnalyzer()
		rows := make([]domain.ReplicaMetrics, 0, len(fracs))
		for i := range fracs {
			rows = append(rows, imbRow("pod-"+string(rune('a'+i)), "v", fracs[i], queues[i], 2000, 500, 1.0))
		}
		states := []domain.VariantReplicaState{{VariantName: "v", AcceleratorName: "H200", CurrentReplicas: len(rows), GPUsPerReplica: 1}}
		r, err := a.Analyze(context.Background(), makeAnalyzerInput(rows, states))
		if err != nil {
			t.Fatal(err)
		}
		return r.TotalDemand
	}
	a0 := occ(full, []int{40, 40, 40})
	a1 := occ(append(full, 0), []int{40, 40, 40, 0})
	t.Logf("occupancy demand: three full queued 40 %.0f; plus a fourth just Ready %.0f", a0, a1)
	if a1 != a0 {
		t.Errorf("a replica Ready for one cycle moved the fleet's demand %.0f -> %.0f", a0, a1)
	}
}

// A sibling idle for long enough takes only what it has room for. One idle
// prefill replica beside a queue of 439 prompts (2.6M tokens) has room for
// its effective capacity, and the rest of the queue is still charged.
func TestIdleRoomBoundsWhatIsDropped(t *testing.T) {
	states := []domain.VariantReplicaState{
		{VariantName: "decode-v", Role: domain.RoleDecode, AcceleratorName: "H200", CurrentReplicas: 2, GPUsPerReplica: 1},
		{VariantName: "prefill-v", Role: domain.RolePrefill, AcceleratorName: "H200", CurrentReplicas: 3, GPUsPerReplica: 1},
	}
	const prompt = 6000.0
	a, clock := imbAnalyzer()
	rows := []domain.ReplicaMetrics{
		imbRow("decode-0", "decode-v", 0.25, 0, prompt, 1000, 0.5),
		imbRow("decode-1", "decode-v", 0.25, 0, prompt, 1000, 0.5),
		imbRow("prefill-0", "prefill-v", 0.20, 439, prompt, 1, 1.0),
		imbRow("prefill-1", "prefill-v", 0, 0, prompt, 1, 0),
		imbRow("prefill-2", "prefill-v", 0.35, 1, prompt, 1, 1.0),
	}
	r := imbSettle(t, a, clock, makeAnalyzerInput(rows, states))
	tokens := func(f float64) float64 { return float64(int64(f * float64(imbKv))) }
	resident := tokens(0.20) + tokens(0.35)
	queue := (439 + 1) * prompt
	// Every prefill replica is priced at one capacity (the variant's k2 or
	// k1), and the idle one holds nothing: its room is that capacity.
	idleP := imbVariant(t, r, "prefill-v").PerReplicaCapacity
	want := resident + queue - min(idleP, queue)
	got := r.RoleDemand[domain.RolePrefill]
	t.Logf("prefill demand %.0f: resident %.0f, queue %.0f, the idle replica's room %.0f", got, resident, queue, idleP)
	if math.Abs(got-want) > 2 {
		t.Errorf("prefill demand %.0f, want %.0f: the resident KV and the queue less the one idle replica's room", got, want)
	}
}

// A warm-pool bridge idle beside a queued replica is no room the role keeps:
// the queue is charged in full, as with no idle replica at all.
func TestIdleRoomBridgeIsNoRoom(t *testing.T) {
	states := []domain.VariantReplicaState{{VariantName: "v", AcceleratorName: "H200", CurrentReplicas: 1, GPUsPerReplica: 1}}
	run := func(bridge bool) float64 {
		t.Helper()
		a, clock := imbAnalyzer()
		rows := []domain.ReplicaMetrics{imbRow("own-0", "v", 0.95, 40, 2000, 500, 1.0)}
		if bridge {
			b := imbRow("bridge-0", "v", 0, 0, 2000, 500, 0)
			b.FromWarmPool = true
			rows = append(rows, b)
		}
		return imbSettle(t, a, clock, makeAnalyzerInput(rows, states)).TotalDemand
	}
	alone, withBridge := run(false), run(true)
	t.Logf("demand: one queued replica %.0f; beside an idle bridge %.0f", alone, withBridge)
	if withBridge < alone {
		t.Errorf("an idle warm-pool bridge took the queue out of demand: %.0f -> %.0f", alone, withBridge)
	}
}

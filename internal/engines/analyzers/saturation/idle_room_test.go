package saturation

import (
	"context"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// Uneven routing is reported, not compensated: a queue standing on one
// replica while a sibling idles is still the role's demand, and the floor's
// backlog. Demand and supply are both sums over the role's replicas, so the
// queue is in the sum whichever replica it stands on.

// A replica that has only just turned Ready reads idle: its one-minute peak
// covers the seconds since it came up. Every original replica is full and
// queued here; the fresh fourth adds nothing and takes nothing away.
func TestUnevenRoutingFreshReplicaKeepsTheBacklog(t *testing.T) {
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

// Review probe (round 2, the idle room counted twice). Two replicas: one at
// 0.75 of its cache with 300 waiting, one idle for minutes. The work there is
// the resident KV and the whole queue, 0.93 of the two replicas' supply.
// Leaving the queue the idle replica had room for out of demand, while its
// capacity stayed in supply, priced it at 0.50: exactly one replica, a
// scale-down signal for the replica whose room was counted.
func TestUnevenRoutingChargesTheWholeQueue(t *testing.T) {
	states := []domain.VariantReplicaState{{VariantName: "v", AcceleratorName: "H200", CurrentReplicas: 2, GPUsPerReplica: 1}}
	const in, out = 2000.0, 500.0
	const queue = 300
	a, clock := imbAnalyzer()
	rows := []domain.ReplicaMetrics{
		imbRow("busy-0", "v", 0.75, queue, in, out, 1.0),
		imbRow("idle-1", "v", 0, 0, in, out, 0),
	}
	r := imbSettle(t, a, clock, makeAnalyzerInput(rows, states))
	vc := imbVariant(t, r, "v")
	resident := float64(int64(0.75 * float64(imbKv)))
	queued := float64(queue) * (in + out)
	supply := vc.PerReplicaCapacity * float64(vc.ReplicaCount)
	t.Logf("demand %.0f; resident %.0f + queue %.0f = %.0f; supply %.0f (util %.2f)",
		r.TotalDemand, resident, queued, resident+queued, supply, r.TotalDemand/supply)
	if r.TotalDemand < resident+queued-1 {
		t.Errorf("demand %.0f, want the resident KV and the whole queue, %.0f: a queue on the wrong replica is still the role's demand",
			r.TotalDemand, resident+queued)
	}
	if !(r.TotalDemand > vc.PerReplicaCapacity) {
		t.Errorf("the load (%.0f) does not fit one replica (%.0f), yet demand %.0f does", resident+queued, vc.PerReplicaCapacity, r.TotalDemand)
	}
}

// unevenSeed writes a measured mu of about 1 req/s for agg-v from three full,
// queued replicas over four saturated cycles a minute apart.
func unevenSeed(t *testing.T) (*SaturationAnalyzer, *time.Time) {
	t.Helper()
	a, clock := imbAnalyzer()
	states := []domain.VariantReplicaState{{VariantName: "agg-v", AcceleratorName: "H200", CurrentReplicas: 3, GPUsPerReplica: 1}}
	for i := range 4 {
		seed := make([]domain.ReplicaMetrics, 0, 3)
		for _, p := range []string{"r0", "r1", "r2"} {
			seed = append(seed, imbRow(p, "agg-v", 0.95, 10, 1000, 1000, []float64{1.0, 1.02, 0.98, 1.01}[i]))
		}
		in := makeAnalyzerInput(seed, states)
		in.ArrivalRate = 3.0
		if _, err := a.Analyze(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		*clock = clock.Add(ThroughputSampleSpacing + time.Second)
	}
	return a, clock
}

// unevenImplied runs one cycle of agg-v at lambda with rows built from
// (frac, queue, rate) triples, and returns the replicas its demand implies.
func unevenImplied(t *testing.T, a *SaturationAnalyzer, lambda float64, pods [][3]float64) float64 {
	t.Helper()
	rows := make([]domain.ReplicaMetrics, 0, len(pods))
	for i, p := range pods {
		rows = append(rows, imbRow("r"+string(rune('0'+i)), "agg-v", p[0], int(p[1]), 1000, 1000, p[2]))
	}
	states := []domain.VariantReplicaState{{VariantName: "agg-v", AcceleratorName: "H200", CurrentReplicas: len(pods), GPUsPerReplica: 1}}
	in := makeAnalyzerInput(rows, states)
	in.ArrivalRate = lambda
	r, err := a.Analyze(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return r.TotalDemand / (0.85 * imbVariant(t, r, "agg-v").PerReplicaCapacity)
}

// Review probe (round 2, a short fleet beside a settled-idle sibling). mu
// about 1, lambda 5, three replicas full and queued 60 each, and a fourth
// never routed to. With perfect routing the fleet still needs (5 + 180/60) /
// 1 / 0.85 = 9.41 replicas: the idle replica's KV room drains nothing, its mu
// is already spent on its share of lambda. The order must not fall once the
// fourth has read idle for two minutes (it fell to 5.88).
func TestUnevenRoutingShortFleetKeepsItsBacklog(t *testing.T) {
	full := [3]float64{0.95, 60, 1}
	ref, _ := unevenSeed(t)
	want := unevenImplied(t, ref, 5, [][3]float64{full, full, full})

	a, clock := unevenSeed(t)
	for i := range 14 {
		got := unevenImplied(t, a, 5, [][3]float64{full, full, full, {0, 0, 0}})
		t.Logf("t=%3ds three full queued 60 beside an idle fourth: implied %.2f (three alone %.2f)", i*15, got, want)
		if got < want-0.01 {
			t.Errorf("t=%ds: implied %.2f replicas, under the %.2f the three queued replicas need alone", i*15, got, want)
		}
		*clock = clock.Add(15 * time.Second)
	}
}

// Review probe (round 2, a load step onto a fleet idle for minutes). The
// router reaches three of the four replicas first; the first cycle of the
// step must not drop the backlog the three hold (it read 5.88 against 10.59
// on the next cycle).
func TestUnevenRoutingLoadStepOntoSettledIdle(t *testing.T) {
	idle := [3]float64{0.05, 0, 0.1}
	full := [3]float64{0.95, 60, 1}
	ref, _ := unevenSeed(t)
	want := unevenImplied(t, ref, 5, [][3]float64{full, full, full})

	a, clock := unevenSeed(t)
	for range 13 {
		unevenImplied(t, a, 0.4, [][3]float64{idle, idle, idle, idle})
		*clock = clock.Add(15 * time.Second)
	}
	first := unevenImplied(t, a, 5, [][3]float64{full, full, full, idle})
	*clock = clock.Add(15 * time.Second)
	next := unevenImplied(t, a, 5, [][3]float64{full, full, full, full})
	t.Logf("step onto a fleet idle for 3 minutes: first cycle %.2f, next %.2f; three queued replicas alone %.2f", first, next, want)
	if first < want-0.01 {
		t.Errorf("the step's first cycle implied %.2f replicas, under the %.2f its three queued replicas need", first, want)
	}
}

package itl

import (
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle/settlerow"
)

// The admission's settle fields (settle.go), on the Tracker directly: the
// key's own trace (Settle nil) and the settle tracker's path. Each run is a
// controller start at t0 with three replicas at distinct fills, one cycle
// every 15 s, and reports the cycles on which a reading was kept out as its
// replica's settle.

const settleTestCycle = 15 * time.Second

var settleT0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// settleTestRow is a Ready replica of v at fill k, every gauge read.
func settleTestRow(pod string, k float64) domain.ReplicaMetrics {
	return domain.ReplicaMetrics{PodName: pod, VariantName: "v", Ready: true, ReadySince: settleT0.Add(-time.Hour),
		KvCacheUsage: k, KvCacheUsageKnown: true, KvUsageInstant: k, KvUsageInstantKnown: true,
		AvgITL: 0.008 + 0.04*k, AvgITLKnown: true, GenerationTokenRate: 1000, GenerationTokenRateKnown: true}
}

func settleTestFleet() []domain.ReplicaMetrics {
	return []domain.ReplicaMetrics{settleTestRow("p0", 0.30), settleTestRow("p1", 0.45), settleTestRow("p2", 0.60)}
}

// settleRun drives a Tracker for n cycles; with tracked, an engine settle
// tracker is observed before each and read (adm.Settle). at is each cycle's
// instant, rows its rows.
func settleRun(adm Admission, tracked bool, n int, at func(i int) time.Time,
	rows func(i int) []domain.ReplicaMetrics) []int {
	tr := NewTracker()
	st := settle.NewTracker()
	st.SetInterval(settleTestCycle)
	if tracked {
		adm.Settle = &Settle{View: st, Model: "ns/m"}
	}
	var out []int
	for i := range n {
		r := rows(i)
		st.Observe("ns/m", settlerow.Of(r, nil), at(i))
		if tr.Observe("k", "v", r, adm, at(i)).Settling > 0 {
			out = append(out, i)
		}
	}
	return out
}

func every(i int) time.Time { return settleT0.Add(time.Duration(i) * settleTestCycle) }

func fleetAlways(int) []domain.ReplicaMetrics { return settleTestFleet() }

// missing is the fleet with p2 missing on the cycles from..to-1.
func missing(from, to int) func(i int) []domain.ReplicaMetrics {
	return func(i int) []domain.ReplicaMetrics {
		f := settleTestFleet()
		if i >= from && i < to {
			return f[:2]
		}
		return f
	}
}

func run(from, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = from + i
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAdmissionSettleFields(t *testing.T) {
	steadyFor := int(DefaultSteadyFor / settleTestCycle)
	for _, tc := range []struct {
		name    string
		adm     Admission
		tracked bool
		at      func(i int) time.Time
		rows    func(i int) []domain.ReplicaMetrics
		n       int
		want    []int
	}{
		// The key's own trace.
		{name: "own trace: a replica on the key's first cycle is settled", rows: fleetAlways, n: 12, at: every},
		{name: "own trace: WaitsAfterRestart makes it wait steadyFor", adm: Admission{WaitsAfterRestart: true},
			rows: fleetAlways, n: 12, at: every, want: run(0, steadyFor)},
		{name: "own trace: one missed cycle restarts the settle without KeepsThroughMissingRow",
			rows: missing(10, 11), n: 25, at: every, want: run(11, steadyFor)},
		{name: "own trace: KeepsThroughMissingRow keeps it through one missed cycle",
			adm: Admission{KeepsThroughMissingRow: true}, rows: missing(10, 11), n: 25, at: every},
		{name: "own trace: KeepsThroughMissingRow restarts it past the absence tolerance (75 s > 1 min)",
			adm: Admission{KeepsThroughMissingRow: true}, rows: missing(10, 15), n: 30, at: every, want: run(15, steadyFor)},
		{name: "own trace: a longer AbsenceTolerance (2 min) keeps a 75 s absence",
			adm: Admission{KeepsThroughMissingRow: true, AbsenceTolerance: 2 * time.Minute}, rows: missing(10, 15), n: 30, at: every},
		{name: "own trace: a gap between the key's cycles is no absence",
			adm: Admission{KeepsThroughMissingRow: true}, rows: fleetAlways, n: 25,
			at: func(i int) time.Time {
				if i >= 10 {
					return every(i).Add(time.Hour)
				}
				return every(i)
			}},
		// The settle tracker.
		{name: "tracker: settled on the first cycle without the wait", tracked: true, rows: fleetAlways, n: 12, at: every},
		{name: "tracker: WaitsAfterRestart waits from the tracker's first sight", tracked: true,
			adm: Admission{WaitsAfterRestart: true}, rows: fleetAlways, n: 12, at: every, want: run(0, steadyFor)},
		{name: "tracker: a new Ready time under the name settles with RestartUnderName", tracked: true,
			adm: Admission{RestartUnderName: true}, rows: newReadyAt(10), n: 25, at: every, want: run(10, steadyFor)},
		{name: "tracker: and is missed without it", tracked: true, rows: newReadyAt(10), n: 25, at: every},
		{name: "tracker: a scrape that read nothing does not idle with IdleOnEvidence", tracked: true,
			adm: Admission{IdleOnEvidence: true}, rows: readNothing(10), n: 25, at: every},
		{name: "tracker: and does without it", tracked: true, rows: readNothing(10), n: 25, at: every, want: run(12, steadyFor)},
		{name: "tracker: a not-Ready row ageing out of the tracker's trace starts nothing", tracked: true,
			rows: notReadyAt(10), n: 50, at: every, want: run(11, steadyFor)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := settleRun(tc.adm, tc.tracked, tc.n, tc.at, tc.rows); !equalInts(got, tc.want) {
				t.Errorf("settling cycles %v, want %v", got, tc.want)
			}
		})
	}
}

// newReadyAt gives p2 a new Ready time from cycle at on.
func newReadyAt(at int) func(i int) []domain.ReplicaMetrics {
	return func(i int) []domain.ReplicaMetrics {
		f := settleTestFleet()
		if i >= at {
			f[2].ReadySince = every(at).Add(-5 * time.Second)
		}
		return f
	}
}

// readNothing makes p2's rows at at and at+1 read nothing at all.
func readNothing(at int) func(i int) []domain.ReplicaMetrics {
	return func(i int) []domain.ReplicaMetrics {
		f := settleTestFleet()
		if i == at || i == at+1 {
			f[2] = domain.ReplicaMetrics{PodName: "p2", VariantName: "v", Ready: true, ReadySince: f[2].ReadySince}
		}
		return f
	}
}

// notReadyAt makes p2 not Ready on cycle at.
func notReadyAt(at int) func(i int) []domain.ReplicaMetrics {
	return func(i int) []domain.ReplicaMetrics {
		f := settleTestFleet()
		if i == at {
			f[2].Ready = false
		}
		return f
	}
}

// A Pod with an Absent rank: FiniteOnly refuses it, and without it its
// Finite ranks' mean is a reading.
func TestAdmissionFiniteOnly(t *testing.T) {
	rm := settleTestRow("p0", 0.4)
	rm.AvgITLKnown = false
	for _, tc := range []struct {
		finite         bool
		offered, noITL int
	}{{true, 0, 1}, {false, 1, 0}} {
		r := NewTracker().Observe("k", "v", []domain.ReplicaMetrics{rm}, Admission{FiniteOnly: tc.finite}, settleT0)
		if r.Offered != tc.offered || r.NoITL != tc.noITL {
			t.Errorf("FiniteOnly %v: offered %d noITL %d, want %d and %d", tc.finite, r.Offered, r.NoITL, tc.offered, tc.noITL)
		}
	}
}

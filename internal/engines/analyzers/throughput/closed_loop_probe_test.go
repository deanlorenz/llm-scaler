package throughput

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/internal/learnertest"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// The closed-loop probe (analyzer-learner.md, 2.5; PR 4b, "Tests") for the
// throughput analyzer's ITL line, on the fleet simulator the saturation
// analyzer's probe runs on (learnertest.Fleet). Each case pastes its result
// with the line on the tracker and on the parent's line (both switches off).
//
// The physics, per Ready replica of a decode variant: the true line is
// ITL(k) = probeA k + probeB, and a replica at KV fill k serves
// k C / KVreq / ITL(k) tokens a second. Offered r requests a second of
// probeOL output tokens each, it settles at the k that serves them, up to
// probeKMax; beyond that it serves its limit and queues. A replica's first
// probeWarmCycles cycles after it turns Ready read the warm-up ITL a real
// engine reports (0.45 s, as on the two-namespace run), at its k.
//
// The analyzer's order is its demand (the arrival rate times the mean
// output length) over its per-replica supply, rounded up; a cycle with no
// throughput capacity for the variant holds the fleet (learnertest.Hold), as
// a silent analyzer leaves the decision to the others. Need is that order
// priced on the true line: what a truthful line orders.
const (
	probeA, probeB  = 0.050, 0.010
	probeC          = 65536
	probeIL         = 1024
	probeOL         = 256
	probeKMax       = 0.95
	probeWarmITL    = 0.45
	probeWarmCycles = 4
	// probeMaxReplicas is the scale target's maxReplicas: where an order
	// that feeds itself stops.
	probeMaxReplicas = 64
)

func probeITL(k float64) float64 { return probeA*k + probeB }

// probeKVreq is the footprint the analyzer's shape gives the probe's rows,
// read off the analyzer itself so need is priced as the analyzer prices.
func probeKVreq(t *testing.T) float64 {
	t.Helper()
	a := NewThroughputAnalyzer().WithClock(clocktesting.NewFakePassiveClock(learnertest.Start))
	row := probeRow("r00", 0.40, probeITL(0.40), learnertest.Start.Add(-time.Hour), 1, 0)
	_, err := a.Analyze(context.Background(), domain.AnalyzerInput{ModelID: "m", Namespace: "ns",
		ReplicaMetrics: []domain.ReplicaMetrics{row},
		VariantStates:  []domain.VariantReplicaState{{VariantName: "v1", CurrentReplicas: 1, GPUsPerReplica: 1}}})
	require.NoError(t, err)
	st, ok := a.VariantState("m", "ns", "v1")
	require.True(t, ok)
	require.Positive(t, st.Shape.KVreq)
	return st.Shape.KVreq
}

// probeTokens is what a replica at fill k serves, in tokens a second.
func probeTokens(k, kvReq float64) float64 { return k * probeC / kvReq / probeITL(k) }

// probeRow is one Ready decode replica at fill k reading itlSec, serving
// served requests a second with queue waiting.
func probeRow(pod string, k, itlSec float64, readySince time.Time, served float64, queue int) domain.ReplicaMetrics {
	return domain.ReplicaMetrics{
		PodName: pod, VariantName: "v1", Ready: true, ReadySince: readySince,
		KvCacheUsage: k, KvUsageInstant: k, TotalKvCapacityTokens: probeC,
		AvgInputTokens: probeIL, AvgOutputTokens: probeOL, AvgITL: itlSec,
		RequestRate: served, GenerationTokenRate: served * probeOL, QueueLength: queue,
	}
}

// probeCase is one closed-loop scenario of the throughput line.
type probeCase struct {
	// start is the fleet's size at the start.
	start   int
	lambda  func(elapsed time.Duration) float64
	horizon time.Duration
	// restartAt, when set, restarts the controller then: a new analyzer
	// with nothing learned.
	restartAt time.Duration
	// noise is the ITL gauge's relative noise.
	noise float64
}

// probeResult is what the loop did, and the line's reasons over it.
type probeResult struct {
	learnertest.Result
	reasons map[string]int
}

// runProbe drives pc with the analyzer newA builds (again at a restart).
func runProbe(t *testing.T, pc probeCase, newA func() *ThroughputAnalyzer) probeResult {
	t.Helper()
	kvReq := probeKVreq(t)
	perReplica := probeTokens(itl.DefaultKSat, kvReq) // tokens/s at the pricing fill
	mu := probeTokens(probeKMax, kvReq) / probeOL     // requests/s at the limit
	clock := clocktesting.NewFakePassiveClock(learnertest.Start)
	build := func() *ThroughputAnalyzer { return newA().WithClock(clock).WithSlopeHoldFor(time.Hour) }
	a := build()
	pods := make([]float64, pc.start)
	for i := range pods {
		pods[i] = mu
	}
	noise := learnertest.ARNoise(learnertest.Rand(1), 1<<14, pc.noise, 0.5)
	n := 0
	res := probeResult{reasons: map[string]int{}}
	restarted := false
	fleet := learnertest.Fleet{
		Pods: pods, AddedMu: mu, Lambda: pc.lambda, Horizon: pc.horizon,
		Need:        func(lambda float64) int { return learnertest.Ceil(lambda * probeOL / perReplica) },
		MaxReplicas: probeMaxReplicas,
	}
	res.Result = fleet.Run(func(now time.Time, elapsed time.Duration, lambda float64, ready []learnertest.Replica) int {
		if pc.restartAt > 0 && !restarted && elapsed >= pc.restartAt {
			restarted, a = true, build()
		}
		clock.SetTime(now)
		rows := make([]domain.ReplicaMetrics, 0, len(ready))
		for _, r := range ready {
			k := probeKMax
			if !r.Saturated {
				tokens := r.Served * probeOL
				k = tokens * probeB / (probeC/kvReq - tokens*probeA)
			}
			lat := probeITL(k) * (1 + noise[n%len(noise)])
			n++
			if now.Sub(r.ReadyAt) < probeWarmCycles*learnertest.Cycle && r.ReadyAt.After(learnertest.Start) {
				lat = probeWarmITL
			}
			rows = append(rows, probeRow(r.Name, k, lat, r.ReadyAt, r.Served, int(math.Round(r.Backlog))))
		}
		out, err := a.Analyze(context.Background(), domain.AnalyzerInput{ModelID: "m", Namespace: "ns",
			ReplicaMetrics: rows, ArrivalRate: lambda,
			VariantStates: []domain.VariantReplicaState{{VariantName: "v1", CurrentReplicas: len(rows), GPUsPerReplica: 1}}})
		require.NoError(t, err)
		for _, vc := range out.VariantCapacities {
			if vc.VariantName != "v1" || vc.PerReplicaCapacity <= 0 {
				continue
			}
			res.reasons[vc.Reason]++
			return learnertest.Ceil(out.TotalDemand / vc.PerReplicaCapacity)
		}
		res.reasons[itlReasonT2Failed]++
		return learnertest.Hold
	})
	return res
}

func (r probeResult) String() string {
	return fmt.Sprintf("need %d, final %d, peak %d, under need for %s; reasons %v; verdict %q",
		r.Need, r.Final, r.Peak, r.UnderFor, r.reasons, r.Verdict())
}

// stepAt is an arrival rate of from until at, then to.
func stepAt(from, to float64, at time.Duration) func(time.Duration) float64 {
	return func(e time.Duration) float64 {
		if e < at {
			return from
		}
		return to
	}
}

// The slope hold: a line learned at load, 35 min of light load below the
// band (no reading enters the window, and its readings age out at 30 min),
// then a step back to need. On the tracker the line holds its A (T2-held)
// through the light load and prices the step on its first cycle. The
// parent's line has no hold: below the band it fits A from k near zero on
// every cycle, whose A is the noise over a small k.
func TestThroughputProbe_TheSlopeHoldCarriesALineThroughLightLoad(t *testing.T) {
	pc := probeCase{start: 3, horizon: 60 * time.Minute, noise: 0.03,
		lambda: func(e time.Duration) float64 {
			switch {
			case e < 10*time.Minute:
				return 10
			case e < 45*time.Minute:
				return 0.5
			default:
				return 10
			}
		}}
	on := runProbe(t, pc, NewThroughputAnalyzer)
	t.Logf("on the tracker: %s", on)
	parent := runProbe(t, pc, newParentLineAnalyzer)
	t.Logf("the parent's line: %s", parent)
	require.Empty(t, on.Verdict())
	require.Positive(t, on.reasons[itlReasonT2Held], "premise: the light load held the slope")
	require.Zero(t, parent.reasons[itlReasonT2Held], "the parent has no held slope")
}

// A scale-up whose joiners carry warm-up ITL: two replicas at k near 0.4
// under 6 requests a second, then a step to 20. On the tracker the joiners'
// warm-up readings are stamped settling and kept out of the line, and the
// fleet settles at need with a peak no more than a replica over it. The
// parent's pinned fit takes them: A steepens, supply falls, every joiner it
// orders lowers its supply again, and it orders more than the line on the
// tracker does at the peak.
func TestThroughputProbe_AScaleUpsWarmUpDoesNotInflateTheOrder(t *testing.T) {
	pc := probeCase{start: 2, horizon: 40 * time.Minute, noise: 0.02, lambda: stepAt(6, 20, 5*time.Minute)}
	on := runProbe(t, pc, NewThroughputAnalyzer)
	t.Logf("on the tracker: %s", on)
	parent := runProbe(t, pc, newParentLineAnalyzer)
	t.Logf("the parent's line: %s", parent)
	require.Empty(t, on.Verdict())
	require.LessOrEqual(t, on.Peak, on.Need+1, "the joiners' warm-up inflated the order on the tracker: %v", on.Trace)
	require.Greater(t, parent.Peak, on.Peak, "negative control: the parent's line took the warm-up and ordered more")
}

// restartCase is four replicas at k near 0.4 under 8 requests a second; the
// controller restarts at 15 min, and the load steps to 30 at stepAfter past
// the restart.
func restartCase(stepAfter time.Duration) probeCase {
	return probeCase{start: 4, horizon: 40 * time.Minute, noise: 0.02, restartAt: 15 * time.Minute,
		lambda: stepAt(12, 30, 15*time.Minute+stepAfter)}
}

// A controller restart 2 min before a load step: the restarted analyzer has
// learned nothing, and its line needs itl.DefaultMinSamples readings, which
// four replicas in the band give it within the minute. The fleet is never
// under need for longer than a start and a settle take, and settles at need.
// The parent's line prices from the restart's first cycle, takes the
// joiners' warm-up, and runs to the replica cap.
func TestThroughputProbe_AControllerRestartBeforeALoadStepSettlesAtNeed(t *testing.T) {
	pc := restartCase(2 * time.Minute)
	on := runProbe(t, pc, NewThroughputAnalyzer)
	t.Logf("on the tracker: %s", on)
	parent := runProbe(t, pc, newParentLineAnalyzer)
	t.Logf("the parent's line: %s", parent)
	require.Empty(t, on.Verdict())
	require.Positive(t, on.reasons[itlReasonT2Failed], "premise: the restarted line was silent for a while")
}

// The same restart 15 s before the step, as the plan states the case: the
// step lands before the restarted line has its ten readings, and the step
// pushes every replica above the band, where the band admits no reading
// (the sequence-cap admission is 4b-3's, and these replicas queue below any
// stated cap). The restarted line has never fitted, so it fits from the
// readings above the band (T2-above-band) until the scale-up it orders
// brings the fleet back into the band, and the fleet is under need for no
// longer than a start and a settle take.
//
// Before the fallback, measured on this case: need 9, final 4, peak 4, under
// need for 24m45s, reasons T2-default 58 (before the restart) and T2-failed
// 102 -- the restarted line was silent for the rest of the run, and alone
// the analyzer never ordered the scale-up. With it: need 9, final 9, peak
// 9, under need for 30s. Negative control: with the fallback off the case
// fails as it did.
func TestThroughputProbe_ARestartJustBeforeAStepAboveTheBandSettlesAtNeed(t *testing.T) {
	on := runProbe(t, restartCase(15*time.Second), NewThroughputAnalyzer)
	t.Logf("on the tracker: %s", on)
	require.Empty(t, on.Verdict())
	require.Positive(t, on.reasons[itlReasonT2AboveBand], "premise: the restarted line priced from above the band")
	require.LessOrEqual(t, on.UnderFor, learnertest.UnderFor)

	off := runProbe(t, restartCase(15*time.Second), func() *ThroughputAnalyzer {
		a := NewThroughputAnalyzer()
		a.noAboveBandFallback = true
		return a
	})
	t.Logf("negative control, no above-band fallback: %s", off)
	require.Contains(t, off.Verdict(), "under-ordered", "negative control: without the fallback the restarted line stays silent")
	require.Zero(t, off.reasons[itlReasonT2AboveBand])

	parent := runProbe(t, restartCase(15*time.Second), newParentLineAnalyzer)
	t.Logf("the parent's line: %s", parent)
}

// One replica at its sequence cap with its siblings below the band: the
// only reading in reach is above the band, from a replica at its stated
// cap. The band has no cap admission until learner PR 4b-3
// (throughput-itl-admits-at-sequence-cap), so the band's window holds no
// reading, and the line that prices the variant is the above-band
// fallback's (T2-above-band), from the capped replica's readings, once
// there are ten of them. Synthetic (a sweep does not hold a decode pod at
// its cap without preempting): when 4b-3 admits the reading, the band's
// window fills and this test fails.
func TestThroughputProbe_AReplicaAtItsCapWithSiblingsBelowTheBandPricesFromAboveTheBand(t *testing.T) {
	run := func(a *ThroughputAnalyzer) (map[string]int, int) {
		store := capacity.NewStore()
		store.Update("ns", "m", "v1", capacity.Record{EngineParams: &capacity.EngineParams{Engine: "vllm", DeclaredBatchCap: 64}},
			lineStart)
		r := newLineRun(t, a.WithCapacityStore(store))
		early := lineStart.Add(-time.Hour)
		reasons, most := map[string]int{}, 0
		for range 40 {
			capped := probeRow("r00", 0.92, probeITL(0.92), early, 3, 6)
			capped.RunningRequests, capped.PreemptionRate, capped.PreemptionRateKnown = 64, 0, true
			rows := []domain.ReplicaMetrics{capped,
				probeRow("r01", 0.08, probeITL(0.08), early, 0.3, 0), probeRow("r02", 0.08, probeITL(0.08), early, 0.3, 0)}
			vc, has := r.step(rows)
			if has {
				reasons[vc.Reason]++
			} else {
				reasons[itlReasonT2Failed]++
			}
			st, _ := a.VariantState("m", "ns", "v1")
			most = max(most, st.SampleCount)
		}
		return reasons, most
	}
	on, inBand := run(NewThroughputAnalyzer())
	parent, _ := run(newParentLineAnalyzer())
	t.Logf("on the tracker: %v, the band's window up to %d readings; the parent's line: %v", on, inBand, parent)
	require.Zero(t, inBand, "the capped replica's reading was admitted to the band: if 4b-3 landed, make this its case")
	require.Equal(t, 9, on[itlReasonT2Failed], "silent until the capped replica's tenth reading")
	require.Equal(t, 31, on[itlReasonT2AboveBand], "the above-band fallback prices the variant after it")
	require.Positive(t, parent[itlReasonT2Default], "the parent prices from the below-band rows' tier-2 fit")
}

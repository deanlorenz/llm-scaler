package slo

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
)

// vs is a variant as discovery reports it: current is status.replicas (Pods
// not Ready included), pending is current less ready.
func vs(role string, current, pending, stuck int, ages ...float64) domain.VariantReplicaState {
	v := domain.VariantReplicaState{VariantName: role + "-v", Role: role, CurrentReplicas: current,
		PendingReplicas: pending, StuckReplicas: stuck}
	if ages != nil {
		v.PendingAges = ages
	}
	return v
}

func everyVariant(domain.VariantReplicaState) bool { return true }

// cadence is the engine interval these tests run cycles at.
const cadence = 15 * time.Second

// watched gives a model the history of an analyzed cycle one cadence before
// now, so a test of another rule does not meet the analyzer's first sight of
// the model (unobserved). The test then runs its cycles at cadence: a jump of
// the clock between two cycles is itself a gap the analyzer reads.
func watched(a *Analyzer, key string, now time.Time) {
	before := now.Add(-cadence)
	a.transients[key] = &transientMemo{seenAt: before, analyzedAt: before, interval: cadence}
}

// ticks returns the cycle times after from, cadence apart, ending exactly at to.
func ticks(from, to time.Time) []time.Time {
	var out []time.Time
	for at := from; at.Before(to); {
		at = at.Add(cadence)
		if at.After(to) {
			at = to
		}
		out = append(out, at)
	}
	return out
}

var _ = Describe("transientOf", func() {
	DescribeTable("names what makes a cycle transient over a set of variants",
		func(states []domain.VariantReplicaState, want string) {
			Expect(transientOf(states, everyVariant)).To(Equal(want))
		},
		Entry("a Pod on its way and none ready: waking",
			[]domain.VariantReplicaState{vs(domain.RoleBoth, 1, 1, 0)}, constants.SLOExcludedZeroReplicas),
		Entry("nothing at all: parked, which is not analyzed and not a wake",
			[]domain.VariantReplicaState{vs(domain.RoleBoth, 0, 0, 0)}, ""),
		Entry("one variant at zero beside one that serves: not a wake",
			[]domain.VariantReplicaState{vs(domain.RoleBoth, 0, 0, 0), vs(domain.RoleDecode, 2, 0, 0)}, ""),
		Entry("a replica starting", []domain.VariantReplicaState{vs(domain.RoleBoth, 3, 1, 0)}, constants.SLOExcludedStarting),
		Entry("a stuck Pod is not starting", []domain.VariantReplicaState{vs(domain.RoleBoth, 3, 1, 1)}, ""),
		Entry("a young not-Ready Pod is starting",
			[]domain.VariantReplicaState{vs(domain.RoleBoth, 3, 1, 0, 90)}, constants.SLOExcludedStarting),
		Entry("an old not-Ready Pod is a readiness flap, not a start",
			[]domain.VariantReplicaState{vs(domain.RoleBoth, 3, 1, 0, 7200)}, ""),
		Entry("a Pod a second short of the flap age is starting",
			[]domain.VariantReplicaState{vs(domain.RoleBoth, 3, 1, 0, startingMaxAge.Seconds()-1)}, constants.SLOExcludedStarting),
		Entry("a Pod at exactly the flap age is a flap",
			[]domain.VariantReplicaState{vs(domain.RoleBoth, 3, 1, 0, startingMaxAge.Seconds())}, ""),
		Entry("a negative age (clock skew) is not a start",
			[]domain.VariantReplicaState{vs(domain.RoleBoth, 3, 1, 0, -1)}, ""),
		Entry("one young Pod beside an old one is starting",
			[]domain.VariantReplicaState{vs(domain.RoleBoth, 4, 2, 0, 7200, 90)}, constants.SLOExcludedStarting),
		Entry("two old Pods are flaps",
			[]domain.VariantReplicaState{vs(domain.RoleBoth, 4, 2, 0, 7200, 3600)}, ""),
		Entry("ages read and none of them pending: the ages win over the count",
			[]domain.VariantReplicaState{{VariantName: "v", Role: domain.RoleBoth, CurrentReplicas: 3,
				PendingReplicas: 1, PendingAges: []float64{}}}, ""),
		Entry("steady", []domain.VariantReplicaState{vs(domain.RoleBoth, 2, 0, 0), vs(domain.RoleDecode, 1, 0, 0)}, ""),
	)
})

var _ = Describe("transientGrace", func() {
	It("is at least the floor, and one and a half times the longest request otherwise", func() {
		Expect(transientGrace(nil)).To(Equal(minTransientGrace))
		rows := []domain.ReplicaMetrics{
			{Ready: true, AvgServiceTime: 40},
			{Ready: true, AvgServiceTime: 200},
			{Ready: false, AvgServiceTime: 9000},
			{Ready: true, FromWarmPool: true, AvgServiceTime: 9000},
		}
		Expect(transientGrace(rows)).To(Equal(300 * time.Second))
	})

	It("is at most the cap, however long a bogus service time says requests take", func() {
		Expect(transientGrace([]domain.ReplicaMetrics{{Ready: true, AvgServiceTime: 1e12}})).To(Equal(maxTransientGrace))
		Expect(transientGrace([]domain.ReplicaMetrics{{Ready: true, AvgServiceTime: maxTransientGrace.Seconds()}})).
			To(Equal(maxTransientGrace))
	})
})

var _ = Describe("noteTransient", func() {
	var (
		a     *Analyzer
		clock time.Time
	)
	const window = 300 * time.Second
	grace := minTransientGrace
	steady := []domain.VariantReplicaState{vs(domain.RoleBoth, 2, 0, 0)}
	BeforeEach(func() {
		a = New(nil, nil)
		clock = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		for _, key := range []string{"ns/m", "ns/a", "ns/b"} {
			watched(a, key, clock)
		}
	})

	It("excludes the window from a wake until the window and the grace have passed", func() {
		ttft, tpot := a.noteTransient("ns/m", []domain.VariantReplicaState{vs(domain.RoleBoth, 1, 1, 0)}, window, grace, clock)
		Expect(ttft).To(Equal(constants.SLOExcludedZeroReplicas))
		Expect(tpot).To(Equal(constants.SLOExcludedZeroReplicas))
		for _, at := range ticks(clock, clock.Add(window+grace-time.Second)) {
			ttft, _ = a.noteTransient("ns/m", steady, window, grace, at)
		}
		Expect(ttft).To(Equal(constants.SLOExcludedZeroReplicas), "the window still holds requests that waited out the wake")
		ttft, tpot = a.noteTransient("ns/m", steady, window, grace, clock.Add(window+grace))
		Expect(ttft).To(BeEmpty())
		Expect(tpot).To(BeEmpty())
	})

	It("excludes TTFT and not TPOT when only a prefill replica starts", func() {
		states := []domain.VariantReplicaState{vs(domain.RolePrefill, 2, 1, 0), vs(domain.RoleDecode, 2, 0, 0)}
		ttft, tpot := a.noteTransient("ns/m", states, window, grace, clock)
		Expect(ttft).To(Equal(constants.SLOExcludedStarting))
		Expect(tpot).To(BeEmpty())
	})

	It("excludes both when a decode replica starts", func() {
		states := []domain.VariantReplicaState{vs(domain.RolePrefill, 2, 0, 0), vs(domain.RoleDecode, 2, 1, 0)}
		ttft, tpot := a.noteTransient("ns/m", states, window, grace, clock)
		Expect(ttft).To(Equal(constants.SLOExcludedStarting))
		Expect(tpot).To(Equal(constants.SLOExcludedStarting))
	})

	It("does not judge a model's first window: nothing here saw what came before it", func() {
		ttft, tpot := a.noteTransient("ns/new", steady, window, grace, clock)
		Expect(ttft).To(Equal(constants.SLOExcludedUnobserved), "a restart, or the first cycle ever")
		Expect(tpot).To(Equal(constants.SLOExcludedUnobserved))
		for _, at := range ticks(clock, clock.Add(window+grace-time.Second)) {
			ttft, _ = a.noteTransient("ns/new", steady, window, grace, at)
			Expect(ttft).To(Equal(constants.SLOExcludedUnobserved), "at %v", at.Sub(clock))
		}
		ttft, tpot = a.noteTransient("ns/new", steady, window, grace, clock.Add(window+grace))
		Expect(ttft).To(BeEmpty())
		Expect(tpot).To(BeEmpty())
	})

	It("does not judge the window after cycles it did not see: the wake the engine skipped", func() {
		at := clock
		for _, at = range ticks(clock, clock.Add(4*cadence)) {
			ttft, _ := a.noteTransient("ns/m", steady, window, grace, at)
			Expect(ttft).To(BeEmpty())
		}
		// Parked, then woken: the engine skipped the model while its Pod had
		// no metrics, so the first cycle the analyzer sees is already steady.
		// The engine reports it present every cycle of the gap, and that
		// must not hide the gap.
		woke := at.Add(3 * time.Minute)
		for _, p := range ticks(at, woke.Add(-cadence)) {
			a.now = func() time.Time { return p }
			a.NoteVariantsPresent("ns", "m", []string{"v"})
		}
		ttft, tpot := a.noteTransient("ns/m", steady, window, grace, woke)
		Expect(ttft).To(Equal(constants.SLOExcludedUnobserved))
		Expect(tpot).To(Equal(constants.SLOExcludedUnobserved))
		// Back on the cadence, a cycle is a cycle: the one mark expires after
		// window+grace and nothing renews it.
		for _, at = range ticks(woke, woke.Add(window+grace)) {
			ttft, _ = a.noteTransient("ns/m", steady, window, grace, at)
		}
		Expect(ttft).To(BeEmpty())
	})

	It("reads jitter in the engine's interval as a cycle, not a gap", func() {
		at := clock
		for _, step := range []time.Duration{15, 15, 19, 15, 25, 15} {
			at = at.Add(step * time.Second)
			ttft, _ := a.noteTransient("ns/m", steady, window, grace, at)
			Expect(ttft).To(BeEmpty(), "a %ds step", step)
		}
	})

	It("judges a model at zero whose only Pod cannot be scheduled: a shortfall, not a wake", func() {
		ttft, tpot := a.noteTransient("ns/m", []domain.VariantReplicaState{vs(domain.RoleBoth, 1, 1, 1)}, window, grace, clock)
		Expect(ttft).To(BeEmpty())
		Expect(tpot).To(BeEmpty())
	})

	It("stops counting a group's pending replicas as starting after the flap age", func() {
		// A LeaderWorkerSet group lists no Pod ages: only how long it has
		// been pending tells a slow start from a group that will never land.
		group := []domain.VariantReplicaState{vs(domain.RoleBoth, 3, 1, 0)}
		ttft, _ := a.noteTransient("ns/m", group, window, grace, clock)
		Expect(ttft).To(Equal(constants.SLOExcludedStarting))
		for _, at := range ticks(clock, clock.Add(startingMaxAge)) {
			ttft, _ = a.noteTransient("ns/m", group, window, grace, at)
		}
		Expect(ttft).To(Equal(constants.SLOExcludedStarting), "the last start's window and grace still hold")
		for _, at := range ticks(clock.Add(startingMaxAge), clock.Add(startingMaxAge+window+grace)) {
			ttft, _ = a.noteTransient("ns/m", group, window, grace, at)
		}
		Expect(ttft).To(BeEmpty(), "pending for longer than any start: judged")

		// Settled, then a new scale-up: a fresh start again.
		steadyAt := clock.Add(startingMaxAge + window + grace + cadence)
		a.noteTransient("ns/m", []domain.VariantReplicaState{vs(domain.RoleBoth, 3, 0, 0)}, window, grace, steadyAt)
		ttft, _ = a.noteTransient("ns/m", group, window, grace, steadyAt.Add(cadence))
		Expect(ttft).To(Equal(constants.SLOExcludedStarting))
	})

	It("judges a steady model", func() {
		ttft, tpot := a.noteTransient("ns/m", steady, window, grace, clock)
		Expect(ttft).To(BeEmpty())
		Expect(tpot).To(BeEmpty())
	})

	It("keeps each model's history apart", func() {
		ttft, _ := a.noteTransient("ns/a", []domain.VariantReplicaState{vs(domain.RoleBoth, 1, 1, 0)}, window, grace, clock)
		Expect(ttft).To(Equal(constants.SLOExcludedZeroReplicas))
		ttft, _ = a.noteTransient("ns/b", steady, window, grace, clock)
		Expect(ttft).To(BeEmpty())
	})

	It("forgets a model not seen for the window timeout", func() {
		a.noteTransient("ns/m", steady, window, grace, clock)
		Expect(a.EvictStale(clock.Add(windowTimeout+time.Minute)).Transients).To(Equal(3),
			"ns/m and the two other models this suite watches")
		Expect(a.transients).To(BeEmpty())
	})
})

var _ = Describe("Analyze with a transient", func() {
	It("withholds the judgement through a wake, keeps the request count, and names the reason per objective", func() {
		registry := prometheus.NewRegistry()
		Expect(metrics.InitMetrics(registry)).To(Succeed())
		rl := &domain.RouterLatency{WindowSeconds: 120, TTFTBuckets: buckets(1, 0.9, 3, 0.95, inf, 1.0)}
		a := New(fakeRouter{latency: rl}, nil)
		clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		a.now = func() time.Time { return clock }
		watched(a, "ns/m", clock)
		run := func(current, pending int) {
			_, err := a.Analyze(context.Background(), domain.AnalyzerInput{
				ModelID: "m", Namespace: "ns", Config: policy(0, 3*time.Second), ArrivalRate: 1,
				VariantStates: []domain.VariantReplicaState{{VariantName: "v", Role: domain.RoleBoth,
					CurrentReplicas: current, PendingReplicas: pending}},
			})
			Expect(err).NotTo(HaveOccurred())
		}
		ttft := map[string]string{constants.LabelObjective: constants.SLOObjectiveTTFT}

		run(1, 1) // waking: one Pod, not yet Ready
		_, observed := gaugeIn(registry, constants.WVASLOObservedSeconds, ttft)
		Expect(observed).To(BeFalse(), "a window that covered a wake is not judged")
		n, counted := gaugeIn(registry, constants.WVASLOWindowRequests, ttft)
		Expect(counted).To(BeTrue())
		Expect(n).To(BeNumerically("~", 120, 1e-9))
		on, _ := gaugeIn(registry, constants.WVASLOExcluded, map[string]string{constants.LabelObjective: constants.SLOObjectiveTTFT,
			constants.LabelReason: constants.SLOExcludedZeroReplicas})
		Expect(on).To(Equal(1.0))

		// The router's own window (120 s here, not the 300 s default) plus the
		// grace decides when judging resumes: a second before, still excluded.
		start := clock
		for _, clock = range ticks(start, start.Add(120*time.Second+minTransientGrace-time.Second)) {
			run(1, 0)
		}
		_, observed = gaugeIn(registry, constants.WVASLOObservedSeconds, ttft)
		Expect(observed).To(BeFalse())
		clock = start.Add(120*time.Second + minTransientGrace)
		run(1, 0)
		v, observed := gaugeIn(registry, constants.WVASLOObservedSeconds, ttft)
		Expect(observed).To(BeTrue(), "judging resumes after the router's window plus the grace")
		Expect(v).To(BeNumerically("~", 1.0, 1e-12))
		for _, reason := range constants.SLOExcludedReasons {
			off, present := gaugeIn(registry, constants.WVASLOExcluded,
				map[string]string{constants.LabelObjective: constants.SLOObjectiveTTFT, constants.LabelReason: reason})
			Expect(present).To(BeTrue(), reason)
			Expect(off).To(BeZero(), reason)
		}
	})

	DescribeTable("excludes per objective while a P/D model's replica starts",
		func(prefillPending, decodePending int, tpotExcluded bool) {
			registry := prometheus.NewRegistry()
			Expect(metrics.InitMetrics(registry)).To(Succeed())
			rl := &domain.RouterLatency{WindowSeconds: 300, TTFTBuckets: buckets(1, 0.9, 3, 0.95, inf, 1.0),
				TPOTSumRate: 0.04, TPOTCountRate: 1}
			a := New(fakeRouter{latency: rl}, nil)
			watched(a, "ns/m", time.Now())
			_, err := a.Analyze(context.Background(), domain.AnalyzerInput{
				ModelID: "m", Namespace: "ns", Config: policy(44*time.Millisecond, 3*time.Second), ArrivalRate: 1,
				VariantStates: []domain.VariantReplicaState{vs(domain.RolePrefill, 2, prefillPending, 0),
					vs(domain.RoleDecode, 2, decodePending, 0)},
			})
			Expect(err).NotTo(HaveOccurred())
			for objective, want := range map[string]bool{constants.SLOObjectiveTTFT: true, constants.SLOObjectiveTPOT: tpotExcluded} {
				_, observed := gaugeIn(registry, constants.WVASLOObservedSeconds,
					map[string]string{constants.LabelObjective: objective})
				Expect(observed).To(Equal(!want), objective+" observed")
				v, ok := gaugeIn(registry, constants.WVASLOExcluded, map[string]string{
					constants.LabelObjective: objective, constants.LabelReason: constants.SLOExcludedStarting})
				Expect(ok).To(BeTrue(), objective)
				wantValue := 0.0
				if want {
					wantValue = 1
				}
				Expect(v).To(Equal(wantValue), objective+" excluded")
			}
		},
		Entry("a prefill replica starting delays first tokens, not the tokens after", 1, 0, false),
		Entry("a decode replica starting delays both", 0, 1, true),
	)

	It("holds the exclusion for the grace the fleet's longest requests need", func() {
		registry := prometheus.NewRegistry()
		Expect(metrics.InitMetrics(registry)).To(Succeed())
		rl := &domain.RouterLatency{WindowSeconds: 120, TTFTBuckets: buckets(1, 0.9, 3, 0.95, inf, 1.0)}
		a := New(fakeRouter{latency: rl}, nil)
		clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		a.now = func() time.Time { return clock }
		watched(a, "ns/m", clock)
		// Requests that last 400 s on average: the grace is 600 s, not the 120 s floor.
		rows := []domain.ReplicaMetrics{{PodName: "p", VariantName: "v", Ready: true, AvgServiceTime: 400}}
		run := func(pending int) {
			_, err := a.Analyze(context.Background(), domain.AnalyzerInput{
				ModelID: "m", Namespace: "ns", Config: policy(0, 3*time.Second), ArrivalRate: 1,
				VariantStates: []domain.VariantReplicaState{{VariantName: "v", Role: domain.RoleBoth,
					CurrentReplicas: 2, PendingReplicas: pending}},
				ReplicaMetrics: rows,
			})
			Expect(err).NotTo(HaveOccurred())
		}
		ttft := map[string]string{constants.LabelObjective: constants.SLOObjectiveTTFT}
		run(1)
		start := clock
		for _, clock = range ticks(start, start.Add(120*time.Second+minTransientGrace)) {
			run(0)
		}
		_, observed := gaugeIn(registry, constants.WVASLOObservedSeconds, ttft)
		Expect(observed).To(BeFalse(), "past the floor, inside the request-duration grace")
		for _, clock = range ticks(clock, start.Add(120*time.Second+600*time.Second)) {
			run(0)
		}
		_, observed = gaugeIn(registry, constants.WVASLOObservedSeconds, ttft)
		Expect(observed).To(BeTrue())
	})

	It("keys a model's transient history by namespace and model", func() {
		rl := &domain.RouterLatency{WindowSeconds: 300, TTFTBuckets: buckets(1, 0.9, 3, 0.95, inf, 1.0)}
		a := New(fakeRouter{latency: rl}, nil)
		clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		a.now = func() time.Time { return clock }
		for _, model := range []string{"m1", "m2"} {
			_, err := a.Analyze(context.Background(), domain.AnalyzerInput{
				ModelID: model, Namespace: "ns", Config: policy(0, 3*time.Second), ArrivalRate: 1,
				VariantStates: []domain.VariantReplicaState{{VariantName: "v", Role: domain.RoleBoth, CurrentReplicas: 1}},
			})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(a.transients).To(HaveKey("ns/m1"))
		Expect(a.transients).To(HaveKey("ns/m2"))
	})
})

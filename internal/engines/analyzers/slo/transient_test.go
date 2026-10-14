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
	})

	It("excludes the window from a wake until the window and the grace have passed", func() {
		ttft, tpot := a.noteTransient("ns/m", []domain.VariantReplicaState{vs(domain.RoleBoth, 1, 1, 0)}, window, grace, clock)
		Expect(ttft).To(Equal(constants.SLOExcludedZeroReplicas))
		Expect(tpot).To(Equal(constants.SLOExcludedZeroReplicas))
		at := clock
		clock = at.Add(window + grace - time.Second)
		ttft, _ = a.noteTransient("ns/m", steady, window, grace, clock)
		Expect(ttft).To(Equal(constants.SLOExcludedZeroReplicas), "the window still holds requests that waited out the wake")
		clock = at.Add(window + grace)
		ttft, tpot = a.noteTransient("ns/m", steady, window, grace, clock)
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
		a.sweep(clock.Add(windowTimeout + time.Minute))
		Expect(a.transients).NotTo(HaveKey("ns/m"))
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
		clock = start.Add(120*time.Second + minTransientGrace - time.Second)
		run(1, 0)
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

	It("excludes TTFT, and only TTFT, while a P/D model's prefill replica starts", func() {
		registry := prometheus.NewRegistry()
		Expect(metrics.InitMetrics(registry)).To(Succeed())
		rl := &domain.RouterLatency{WindowSeconds: 300, TTFTBuckets: buckets(1, 0.9, 3, 0.95, inf, 1.0)}
		a := New(fakeRouter{latency: rl}, nil)
		_, err := a.Analyze(context.Background(), domain.AnalyzerInput{
			ModelID: "m", Namespace: "ns", Config: policy(0, 3*time.Second), ArrivalRate: 1,
			VariantStates: []domain.VariantReplicaState{vs(domain.RolePrefill, 2, 1, 0), vs(domain.RoleDecode, 2, 0, 0)},
		})
		Expect(err).NotTo(HaveOccurred())
		ttft := map[string]string{constants.LabelObjective: constants.SLOObjectiveTTFT}
		_, observed := gaugeIn(registry, constants.WVASLOObservedSeconds, ttft)
		Expect(observed).To(BeFalse(), "a prefill replica starting delays first tokens")
		on, _ := gaugeIn(registry, constants.WVASLOExcluded, map[string]string{
			constants.LabelObjective: constants.SLOObjectiveTTFT, constants.LabelReason: constants.SLOExcludedStarting})
		Expect(on).To(Equal(1.0))
	})

	It("holds the exclusion for the grace the fleet's longest requests need", func() {
		registry := prometheus.NewRegistry()
		Expect(metrics.InitMetrics(registry)).To(Succeed())
		rl := &domain.RouterLatency{WindowSeconds: 120, TTFTBuckets: buckets(1, 0.9, 3, 0.95, inf, 1.0)}
		a := New(fakeRouter{latency: rl}, nil)
		clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		a.now = func() time.Time { return clock }
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
		clock = start.Add(120*time.Second + minTransientGrace)
		run(0)
		_, observed := gaugeIn(registry, constants.WVASLOObservedSeconds, ttft)
		Expect(observed).To(BeFalse(), "past the floor, inside the request-duration grace")
		clock = start.Add(120*time.Second + 600*time.Second)
		run(0)
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

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

var _ = Describe("transientNow", func() {
	vs := func(current, pending, stuck int) domain.VariantReplicaState {
		return domain.VariantReplicaState{VariantName: "v", CurrentReplicas: current,
			PendingReplicas: pending, StuckReplicas: stuck}
	}

	DescribeTable("names what makes a cycle transient",
		func(states []domain.VariantReplicaState, prev int, hadPrev bool, want string) {
			got, _ := transientNow(states, prev, hadPrev)
			Expect(got).To(Equal(want))
		},
		Entry("no replica at all: waking", []domain.VariantReplicaState{vs(0, 1, 0)}, 0, true, constants.SLOExcludedZeroReplicas),
		Entry("a replica starting", []domain.VariantReplicaState{vs(2, 1, 0)}, 3, true, constants.SLOExcludedStarting),
		Entry("a stuck Pod is not starting", []domain.VariantReplicaState{vs(2, 1, 1)}, 3, true, ""),
		Entry("a rise since last cycle", []domain.VariantReplicaState{vs(3, 0, 0)}, 2, true, constants.SLOExcludedRise),
		Entry("no rise on the first cycle seen", []domain.VariantReplicaState{vs(3, 0, 0)}, 0, false, ""),
		Entry("a fall is not transient", []domain.VariantReplicaState{vs(2, 0, 0)}, 3, true, ""),
		Entry("steady", []domain.VariantReplicaState{vs(2, 0, 0), vs(1, 0, 0)}, 3, true, ""),
	)
})

var _ = Describe("noteTransient", func() {
	var (
		a     *Analyzer
		clock time.Time
	)
	const window = 300 * time.Second
	steady := []domain.VariantReplicaState{{VariantName: "v", CurrentReplicas: 2}}
	BeforeEach(func() {
		a = New(nil, nil)
		clock = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	})

	It("excludes the window from a wake until the window and the grace have passed", func() {
		Expect(a.noteTransient("ns/m", []domain.VariantReplicaState{{VariantName: "v", PendingReplicas: 1}}, window, clock)).
			To(Equal(constants.SLOExcludedZeroReplicas))
		clock = clock.Add(15 * time.Second)
		// Now serving, and the count it rose to is the one it holds from here.
		Expect(a.noteTransient("ns/m", []domain.VariantReplicaState{{VariantName: "v", CurrentReplicas: 1}}, window, clock)).
			To(Equal(constants.SLOExcludedZeroReplicas), "the window still holds requests that waited out the wake")
		hold := []domain.VariantReplicaState{{VariantName: "v", CurrentReplicas: 1}}
		at := clock.Add(-15 * time.Second) // the last transient cycle
		clock = at.Add(window + transientGrace - time.Second)
		Expect(a.noteTransient("ns/m", hold, window, clock)).To(Equal(constants.SLOExcludedZeroReplicas))
		clock = at.Add(window + transientGrace)
		Expect(a.noteTransient("ns/m", hold, window, clock)).To(BeEmpty())
	})

	It("judges a steady model, and excludes it again on a rise", func() {
		Expect(a.noteTransient("ns/m", steady, window, clock)).To(BeEmpty())
		clock = clock.Add(15 * time.Second)
		Expect(a.noteTransient("ns/m", steady, window, clock)).To(BeEmpty())
		clock = clock.Add(15 * time.Second)
		Expect(a.noteTransient("ns/m", []domain.VariantReplicaState{{VariantName: "v", CurrentReplicas: 3}}, window, clock)).
			To(Equal(constants.SLOExcludedRise))
	})

	It("keeps each model's history apart", func() {
		Expect(a.noteTransient("ns/a", []domain.VariantReplicaState{{VariantName: "v"}}, window, clock)).
			To(Equal(constants.SLOExcludedZeroReplicas))
		Expect(a.noteTransient("ns/b", steady, window, clock)).To(BeEmpty())
	})

	It("forgets a model not seen for the window timeout", func() {
		a.noteTransient("ns/m", steady, window, clock)
		a.sweep(clock.Add(windowTimeout + time.Minute))
		Expect(a.transients).NotTo(HaveKey("ns/m"))
	})
})

var _ = Describe("Analyze with a transient", func() {
	It("withholds the judgement through a wake, keeps the request count, and names the reason", func() {
		registry := prometheus.NewRegistry()
		Expect(metrics.InitMetrics(registry)).To(Succeed())
		gauge := func(name string, match map[string]string) (float64, bool) {
			mfs, err := registry.Gather()
			Expect(err).NotTo(HaveOccurred())
			for _, mf := range mfs {
				if mf.GetName() != name {
					continue
				}
			next:
				for _, m := range mf.GetMetric() {
					for k, v := range match {
						found := false
						for _, l := range m.GetLabel() {
							if l.GetName() == k && l.GetValue() == v {
								found = true
							}
						}
						if !found {
							continue next
						}
					}
					return m.GetGauge().GetValue(), true
				}
			}
			return 0, false
		}

		rl := &domain.RouterLatency{WindowSeconds: 300, TTFTBuckets: buckets(1, 0.9, 3, 0.95, inf, 1.0)}
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

		run(0, 1) // parked, waking
		_, observed := gauge(constants.WVASLOObservedSeconds, ttft)
		Expect(observed).To(BeFalse(), "a window that covered a wake is not judged")
		_, attained := gauge(constants.WVASLOAttainmentRatio, ttft)
		Expect(attained).To(BeFalse())
		n, counted := gauge(constants.WVASLOWindowRequests, ttft)
		Expect(counted).To(BeTrue())
		Expect(n).To(BeNumerically("~", 300, 1e-9))
		on, _ := gauge(constants.WVASLOExcluded, map[string]string{constants.LabelReason: constants.SLOExcludedZeroReplicas})
		Expect(on).To(Equal(1.0))

		clock = clock.Add(15 * time.Second)
		run(1, 0) // serving
		_, observed = gauge(constants.WVASLOObservedSeconds, ttft)
		Expect(observed).To(BeFalse(), "the window still holds requests that waited out the wake")

		clock = clock.Add(300*time.Second + transientGrace)
		run(1, 0)
		v, observed := gauge(constants.WVASLOObservedSeconds, ttft)
		Expect(observed).To(BeTrue())
		Expect(v).To(BeNumerically("~", 1.0, 1e-12))
		for _, reason := range constants.SLOExcludedReasons {
			off, present := gauge(constants.WVASLOExcluded, map[string]string{constants.LabelReason: reason})
			Expect(present).To(BeTrue(), reason)
			Expect(off).To(BeZero(), reason)
		}
	})
})

package slo

import (
	"context"
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
)

var _ = Describe("quantileOf", func() {
	It("interpolates between order statistics and leaves its input alone", func() {
		in := []float64{5, 1, 4, 2, 3}
		Expect(quantileOf(in, 0.5)).To(Equal(3.0))
		Expect(quantileOf(in, 0.9)).To(BeNumerically("~", 4.6, 1e-12))
		Expect(quantileOf(in, 1)).To(Equal(5.0))
		Expect(quantileOf([]float64{7}, 0.9)).To(Equal(7.0))
		Expect(in).To(Equal([]float64{5, 1, 4, 2, 3}))
	})
})

var _ = Describe("observeTTFTInputs", func() {
	var (
		a     *Analyzer
		clock time.Time
	)
	BeforeEach(func() {
		a = New(nil, nil)
		clock = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	})
	pre := func(pod string, rate, prefill, queue, tok float64, waiting int) domain.ReplicaMetrics {
		return domain.ReplicaMetrics{PodName: pod, VariantName: "pre", Ready: true, RequestRate: rate,
			AvgPrefillTime: prefill, AvgQueueTime: queue, PrefillComputedTokenRate: tok, QueueLength: waiting}
	}

	It("weights prefill and queue time by request rate over the variant's own ready rows", func() {
		in := a.observeTTFTInputs("k", "pre", []domain.ReplicaMetrics{
			pre("a", 3, 1.0, 0, 1000, 0),
			pre("b", 1, 5.0, 4, 1000, 0),
			{PodName: "c", VariantName: "pre", Ready: false, RequestRate: 100, AvgPrefillTime: 99},
			{PodName: "d", VariantName: "pre", Ready: true, FromWarmPool: true, RequestRate: 100, AvgPrefillTime: 99},
			{PodName: "e", VariantName: "other", Ready: true, RequestRate: 100, AvgPrefillTime: 99},
		}, clock)
		Expect(in.prefillSeconds).To(BeNumerically("~", 2.0, 1e-12))
		// A zero queue wait beside a real prefill reading is an idle scheduler,
		// and it counts.
		Expect(in.queueSeconds).To(BeNumerically("~", 1.0, 1e-12))
		Expect(math.IsNaN(in.ceiling)).To(BeTrue(), "no saturated readings yet")
		Expect(math.IsNaN(in.utilization)).To(BeTrue())
	})

	It("learns the ceiling as the p90 of rates read while the queue was non-empty, then reports utilization", func() {
		for i := range 10 {
			a.observeTTFTInputs("k", "pre", []domain.ReplicaMetrics{
				pre("a", 1, 1, 0.5, 10000+float64(i)*1000, 3), // saturated: 10k .. 19k
			}, clock)
			clock = clock.Add(15 * time.Second)
		}
		in := a.observeTTFTInputs("k", "pre", []domain.ReplicaMetrics{
			pre("a", 1, 1, 0, 9000, 0), // not saturated: does not move the ceiling
			pre("b", 1, 1, 0, 9000, 0),
		}, clock)
		Expect(in.ceiling).To(BeNumerically("~", 18100, 1e-9))
		Expect(in.utilization).To(BeNumerically("~", 18000/(2*18100.0), 1e-12))
	})

	It("forgets saturated readings older than an hour", func() {
		for range 10 {
			a.observeTTFTInputs("k", "pre", []domain.ReplicaMetrics{pre("a", 1, 1, 0.5, 50000, 2)}, clock)
		}
		clock = clock.Add(ceilingMaxAge + time.Minute)
		in := a.observeTTFTInputs("k", "pre", []domain.ReplicaMetrics{pre("a", 1, 1, 0, 1000, 0)}, clock)
		Expect(math.IsNaN(in.ceiling)).To(BeTrue())
		Expect(a.ceilings["k"]).To(BeEmpty())
	})

	It("keeps at most the newest readings", func() {
		for i := range ceilingMaxSamples + 30 {
			a.observeTTFTInputs("k", "pre", []domain.ReplicaMetrics{pre("a", 1, 1, 0.5, float64(i), 1)}, clock)
		}
		Expect(a.ceilings["k"]).To(HaveLen(ceilingMaxSamples))
		Expect(a.ceilings["k"][0].rate).To(Equal(30.0))
	})
})

var _ = Describe("TTFT inputs in what Analyze publishes", func() {
	It("publishes them for a prefill variant, with no TPOT source, and not for decode", func() {
		registry := prometheus.NewRegistry()
		Expect(metrics.InitMetrics(registry)).To(Succeed())
		a := New(nil, nil)
		clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		a.now = func() time.Time { return clock }
		prefill := domain.VariantReplicaState{VariantName: "pre", Role: domain.RolePrefill, CurrentReplicas: 1}
		decode := domain.VariantReplicaState{VariantName: "dec", Role: domain.RoleDecode, CurrentReplicas: 1}
		rows := []domain.ReplicaMetrics{
			{PodName: "p", VariantName: "pre", Ready: true, RequestRate: 1, AvgPrefillTime: 0.8, AvgQueueTime: 0.2,
				PrefillComputedTokenRate: 5000},
			{PodName: "d", VariantName: "dec", Ready: true, RequestRate: 1, AvgOutputTokens: 500,
				AvgPrefillTime: 0.1, KvUsageInstant: 0.3, AvgITL: 0.03},
		}
		_, err := a.Analyze(context.Background(), domain.AnalyzerInput{
			ModelID: "m", Namespace: "ns", Config: policy(0, 3*time.Second), ArrivalRate: 1,
			VariantStates: []domain.VariantReplicaState{prefill, decode}, ReplicaMetrics: rows,
		})
		Expect(err).NotTo(HaveOccurred())
		v, ok := gaugeIn(registry, constants.WVASLOPrefillSeconds, map[string]string{constants.LabelVariantName: "pre"})
		Expect(ok).To(BeTrue())
		Expect(v).To(BeNumerically("~", 0.8, 1e-12))
		q, _ := gaugeIn(registry, constants.WVASLOEngineQueueSeconds, map[string]string{constants.LabelVariantName: "pre"})
		Expect(q).To(BeNumerically("~", 0.2, 1e-12))
		_, ok = gaugeIn(registry, constants.WVASLOSource, map[string]string{constants.LabelVariantName: "pre"})
		Expect(ok).To(BeFalse(), "a prefill variant is not priced for TPOT, so it has no source")
		_, ok = gaugeIn(registry, constants.WVASLOPrefillSeconds, map[string]string{constants.LabelVariantName: "dec"})
		Expect(ok).To(BeFalse(), "decode's prefill is the KV handoff, not a prefill curve")
		_, ok = gaugeIn(registry, constants.WVASLOPrefillCeiling, map[string]string{constants.LabelVariantName: "pre"})
		Expect(ok).To(BeFalse(), "no ceiling until enough saturated readings")
	})

	It("publishes nothing about prefill without a TTFT objective", func() {
		registry := prometheus.NewRegistry()
		Expect(metrics.InitMetrics(registry)).To(Succeed())
		a := New(nil, nil)
		_, err := a.Analyze(context.Background(), domain.AnalyzerInput{
			ModelID: "m", Namespace: "ns", Config: policy(44*time.Millisecond, 0), ArrivalRate: 1,
			VariantStates:  []domain.VariantReplicaState{{VariantName: "pre", Role: domain.RolePrefill, CurrentReplicas: 1}},
			ReplicaMetrics: []domain.ReplicaMetrics{{PodName: "p", VariantName: "pre", Ready: true, AvgPrefillTime: 0.8}},
		})
		Expect(err).NotTo(HaveOccurred())
		_, ok := gaugeIn(registry, constants.WVASLOPrefillSeconds, nil)
		Expect(ok).To(BeFalse())
	})
})

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

// pre is a prefill replica's row: request rate, prefill and queue time, the
// prefill token rate, how many requests wait, and its KV fill.
func pre(pod string, rate, prefill, queue, tok float64, waiting int, kv float64) domain.ReplicaMetrics {
	return domain.ReplicaMetrics{PodName: pod, VariantName: "pre", Ready: true, RequestRate: rate,
		AvgPrefillTime: prefill, AvgQueueTime: queue, PrefillComputedTokenRate: tok, QueueLength: waiting,
		KvUsageInstant: kv}
}

var _ = Describe("observeTTFTInputs", func() {
	var (
		a     *Analyzer
		clock time.Time
	)
	BeforeEach(func() {
		a = New(nil, nil)
		clock = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	})
	// cycles feeds n cycles 30 s apart of one saturated replica at the given
	// rates, cycling through them.
	cycles := func(n int, rates ...float64) {
		for i := range n {
			a.observeTTFTInputs("k", "pre", domain.RolePrefill,
				[]domain.ReplicaMetrics{pre("a", 1, 1, 0.5, rates[i%len(rates)], 3, 0.4)}, clock)
			clock = clock.Add(30 * time.Second)
		}
	}

	It("weights prefill and queue time by request rate over the variant's own ready rows", func() {
		in := a.observeTTFTInputs("k", "pre", domain.RolePrefill, []domain.ReplicaMetrics{
			pre("a", 3, 1.0, 0, 1000, 0, 0.3),
			pre("b", 1, 5.0, 4, 1000, 0, 0.3),
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

	It("learns the ceiling as the p90 of per-cycle medians, from ten cycles over five minutes", func() {
		// 10 cycles, 30 s apart: 4.5 minutes, one short of the span.
		cycles(10, 10000, 11000, 12000, 13000, 14000, 15000, 16000, 17000, 18000, 19000)
		in := a.observeTTFTInputs("k", "pre", domain.RolePrefill,
			[]domain.ReplicaMetrics{pre("a", 1, 1, 0, 9000, 0, 0.4)}, clock)
		Expect(math.IsNaN(in.ceiling)).To(BeTrue(), "ten cycles inside five minutes is still one burst")
		clock = clock.Add(time.Minute)
		in = a.observeTTFTInputs("k", "pre", domain.RolePrefill, []domain.ReplicaMetrics{
			pre("a", 1, 1, 0.5, 20000, 3, 0.4), // the 11th cycle, past five minutes
			pre("b", 1, 1, 0, 9000, 0, 0.4),
		}, clock)
		Expect(in.ceiling).To(BeNumerically("~", quantileOf([]float64{10000, 11000, 12000, 13000, 14000,
			15000, 16000, 17000, 18000, 19000, 20000}, 0.9), 1e-9))
		Expect(in.utilization).To(BeNumerically("~", 29000/(2*in.ceiling), 1e-12))
	})

	It("takes one sample per cycle however many replicas were saturated", func() {
		a.observeTTFTInputs("k", "pre", domain.RolePrefill, []domain.ReplicaMetrics{
			pre("a", 1, 1, 1, 1000, 2, 0.4), pre("b", 1, 1, 1, 3000, 2, 0.4), pre("c", 1, 1, 1, 9000, 2, 0.4),
		}, clock)
		Expect(a.ceilings["k"].samples).To(HaveLen(1))
		Expect(a.ceilings["k"].samples[0].rate).To(Equal(3000.0), "the median of the cycle")
	})

	It("ignores a replica queued with full KV: it waits on decode, it is not computing", func() {
		a.observeTTFTInputs("k", "pre", domain.RolePrefill,
			[]domain.ReplicaMetrics{pre("a", 1, 1, 1, 500, 40, 0.95)}, clock)
		Expect(a.ceilings["k"].samples).To(BeEmpty())
	})

	DescribeTable("samples a queued replica only while its rate and KV say it computes",
		func(tok, kv float64, sampled bool) {
			a.observeTTFTInputs("k", "pre", domain.RolePrefill,
				[]domain.ReplicaMetrics{pre("a", 1, 1, 0.5, tok, 3, kv)}, clock)
			if sampled {
				Expect(a.ceilings["k"].samples).To(HaveLen(1))
			} else {
				Expect(a.ceilings["k"].samples).To(BeEmpty())
			}
		},
		Entry("KV just below the observable range", 1000.0, math.Nextafter(0.80, 0), true),
		Entry("KV at the observable limit", 1000.0, 0.80, false),
		Entry("KV not read (the collector leaves 0)", 1000.0, 0.0, false),
		Entry("KV NaN", 1000.0, math.NaN(), false),
		Entry("no prefill tokens computed", 0.0, 0.4, false),
		Entry("prefill rate NaN", math.NaN(), 0.4, false),
	)

	// sampleAt feeds one saturated cycle at the given offset from the start.
	sampleAt := func(start time.Time, offset time.Duration) ttftInputs {
		return a.observeTTFTInputs("k", "pre", domain.RolePrefill,
			[]domain.ReplicaMetrics{pre("a", 1, 1, 0.5, 10000, 3, 0.4)}, start.Add(offset))
	}

	It("reports no ceiling from nine cycles, however long they span", func() {
		start := clock
		var in ttftInputs
		for i := range 9 {
			in = sampleAt(start, time.Duration(i)*time.Minute)
		}
		Expect(math.IsNaN(in.ceiling)).To(BeTrue(), "nine cycles over eight minutes")
	})

	It("reports a ceiling from ten cycles spanning exactly five minutes, and none a second short", func() {
		start := clock
		for i := range 9 {
			sampleAt(start, time.Duration(i)*30*time.Second)
		}
		in := sampleAt(start, ceilingMinSpan-time.Second)
		Expect(math.IsNaN(in.ceiling)).To(BeTrue(), "ten cycles over 4:59")

		a = New(nil, nil)
		for i := range 9 {
			sampleAt(start, time.Duration(i)*30*time.Second)
		}
		in = sampleAt(start, ceilingMinSpan)
		Expect(in.ceiling).To(BeNumerically("~", 10000, 1e-9), "ten cycles over exactly 5:00")
	})

	It("keeps the last ceiling after the window runs dry, and ages the samples", func() {
		cycles(12, 10000)
		learned := a.ceilings["k"].last
		Expect(learned).To(BeNumerically("~", 10000, 1e-9))
		clock = clock.Add(ceilingMaxAge + time.Minute)
		in := a.observeTTFTInputs("k", "pre", domain.RolePrefill,
			[]domain.ReplicaMetrics{pre("a", 1, 1, 0, 5000, 0, 0.4)}, clock)
		Expect(a.ceilings["k"].samples).To(BeEmpty())
		Expect(in.ceiling).To(Equal(learned))
		Expect(in.utilization).To(BeNumerically("~", 0.5, 1e-12))
	})

	It("measures utilization over the replicas that report a prefill rate", func() {
		cycles(12, 10000)
		in := a.observeTTFTInputs("k", "pre", domain.RolePrefill, []domain.ReplicaMetrics{
			pre("a", 1, 1, 0, 5000, 0, 0.4),
			pre("fresh", 1, 1, 0, 0, 0, 0.4), // Ready, no rate series yet
		}, clock)
		Expect(in.utilization).To(BeNumerically("~", 0.5, 1e-12), "5000 of one replica's 10000, not of two")
	})

	It("keeps at most the newest samples when cycles run faster than the age limit drops them", func() {
		for i := range ceilingMaxSamples + 30 {
			a.observeTTFTInputs("k", "pre", domain.RolePrefill,
				[]domain.ReplicaMetrics{pre("a", 1, 1, 0.5, float64(i), 3, 0.4)}, clock)
			clock = clock.Add(5 * time.Second)
		}
		Expect(a.ceilings["k"].samples).To(HaveLen(ceilingMaxSamples))
		Expect(a.ceilings["k"].samples[0].rate).To(Equal(30.0), "the oldest are the ones dropped")
	})

	It("gives an aggregated variant prefill and queue time but no ceiling", func() {
		for range 12 {
			in := a.observeTTFTInputs("k", "pre", domain.RoleBoth,
				[]domain.ReplicaMetrics{pre("a", 1, 1, 0.5, 9000, 3, 0.4)}, clock)
			Expect(in.prefillSeconds).To(Equal(1.0))
			Expect(math.IsNaN(in.ceiling)).To(BeTrue())
			Expect(math.IsNaN(in.utilization)).To(BeTrue())
			clock = clock.Add(time.Minute)
		}
		Expect(a.ceilings).NotTo(HaveKey("k"))
	})

	It("forgets a variant's ceiling when the variant is swept", func() {
		cycles(1, 10000)
		Expect(a.ceilings).To(HaveKey("k"))
		Expect(a.EvictStale(clock.Add(windowTimeout + time.Minute)).Ceilings).To(Equal(1))
		Expect(a.ceilings).NotTo(HaveKey("k"))
	})
})

var _ = Describe("TTFT inputs in what Analyze publishes", func() {
	var registry *prometheus.Registry
	BeforeEach(func() {
		registry = prometheus.NewRegistry()
		Expect(metrics.InitMetrics(registry)).To(Succeed())
	})
	prefill := domain.VariantReplicaState{VariantName: "pre", Role: domain.RolePrefill, CurrentReplicas: 1}
	decode := domain.VariantReplicaState{VariantName: "dec", Role: domain.RoleDecode, CurrentReplicas: 1}
	preVar := map[string]string{constants.LabelVariantName: "pre"}
	decVar := map[string]string{constants.LabelVariantName: "dec"}

	It("publishes them, ceiling and utilization included, for a prefill variant, and nothing TPOT for it", func() {
		a := New(nil, nil)
		clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		a.now = func() time.Time { return clock }
		for i := range 12 {
			_, err := a.Analyze(context.Background(), domain.AnalyzerInput{
				ModelID: "m", Namespace: "ns", Config: policy(0, 3*time.Second), ArrivalRate: 1,
				VariantStates:  []domain.VariantReplicaState{prefill},
				ReplicaMetrics: []domain.ReplicaMetrics{pre("p", 1, 0.8, 0.2, 10000+float64(i), 2, 0.4)},
			})
			Expect(err).NotTo(HaveOccurred())
			clock = clock.Add(30 * time.Second)
		}
		v, _ := gaugeIn(registry, constants.WVASLOPrefillSeconds, preVar)
		Expect(v).To(BeNumerically("~", 0.8, 1e-12))
		q, _ := gaugeIn(registry, constants.WVASLOEngineQueueSeconds, preVar)
		Expect(q).To(BeNumerically("~", 0.2, 1e-12))
		c, ok := gaugeIn(registry, constants.WVASLOPrefillCeiling, preVar)
		Expect(ok).To(BeTrue())
		Expect(c).To(BeNumerically("~", 10009.9, 1e-6))
		u, ok := gaugeIn(registry, constants.WVASLOPrefillUtilization, preVar)
		Expect(ok).To(BeTrue())
		Expect(u).To(BeNumerically("~", 10011/c, 1e-9))
		for _, name := range []string{constants.WVASLOSource, constants.WVASLOFill, constants.WVASLOPredictedITLSeconds} {
			_, ok = gaugeIn(registry, name, preVar)
			Expect(ok).To(BeFalse(), name+": a prefill variant is not priced for TPOT")
		}
	})

	It("publishes no prefill series for a decode variant priced for TPOT", func() {
		a := New(nil, nil)
		clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		a.now = func() time.Time { return clock }
		for i := range 5 {
			// The decode rows carry prefill and queue times too, as vLLM reports
			// them on a decode Pod: only the role keeps them off the curve.
			rows := cycleRows("dec", i)
			for j := range rows {
				rows[j].AvgPrefillTime, rows[j].AvgQueueTime = 0.05, 0.01
			}
			rows = append(rows, pre("p", 1, 0.8, 0.2, 5000, 0, 0.3))
			_, err := a.Analyze(context.Background(), domain.AnalyzerInput{
				ModelID: "m", Namespace: "ns", Config: policy(44*time.Millisecond, 3*time.Second), ArrivalRate: 1,
				VariantStates: []domain.VariantReplicaState{prefill, decode}, ReplicaMetrics: rows,
			})
			Expect(err).NotTo(HaveOccurred())
			clock = clock.Add(15 * time.Second)
		}
		_, ok := gaugeIn(registry, constants.WVASLOSource, decVar)
		Expect(ok).To(BeTrue(), "the decode variant is priced")
		for _, name := range []string{constants.WVASLOPrefillSeconds, constants.WVASLOEngineQueueSeconds,
			constants.WVASLOPrefillUtilization, constants.WVASLOPrefillCeiling} {
			_, ok = gaugeIn(registry, name, decVar)
			Expect(ok).To(BeFalse(), name+": decode's prefill is the KV handoff, not a prefill curve")
		}
		_, ok = gaugeIn(registry, constants.WVASLOPrefillSeconds, preVar)
		Expect(ok).To(BeTrue())
	})

	It("names no TPOT source for an aggregated variant under a TTFT-only policy", func() {
		a := New(nil, nil)
		both := domain.VariantReplicaState{VariantName: "pre", Role: domain.RoleBoth, CurrentReplicas: 1}
		// No output length is reported: the reading carries TTFT inputs only,
		// so it was never priced for TPOT and cannot lack what TPOT needs.
		_, err := a.Analyze(context.Background(), domain.AnalyzerInput{
			ModelID: "m", Namespace: "ns", Config: policy(0, 3*time.Second), ArrivalRate: 1,
			VariantStates:  []domain.VariantReplicaState{both},
			ReplicaMetrics: []domain.ReplicaMetrics{pre("p", 1, 0.8, 0.2, 5000, 0, 0.3)},
		})
		Expect(err).NotTo(HaveOccurred())
		_, ok := gaugeIn(registry, constants.WVASLOPrefillSeconds, preVar)
		Expect(ok).To(BeTrue(), "the TTFT inputs are published")
		_, ok = gaugeIn(registry, constants.WVASLOSource, preVar)
		Expect(ok).To(BeFalse(), "no TPOT objective, so no TPOT source")
	})

	It("publishes nothing about prefill without a TTFT objective", func() {
		a := New(nil, nil)
		_, err := a.Analyze(context.Background(), domain.AnalyzerInput{
			ModelID: "m", Namespace: "ns", Config: policy(44*time.Millisecond, 0), ArrivalRate: 1,
			VariantStates:  []domain.VariantReplicaState{prefill},
			ReplicaMetrics: []domain.ReplicaMetrics{pre("p", 1, 0.8, 0.2, 5000, 0, 0.3)},
		})
		Expect(err).NotTo(HaveOccurred())
		_, ok := gaugeIn(registry, constants.WVASLOPrefillSeconds, nil)
		Expect(ok).To(BeFalse())
	})
})

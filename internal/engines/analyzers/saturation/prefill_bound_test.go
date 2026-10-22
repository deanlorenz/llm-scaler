package saturation

import (
	"context"
	"math"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/floor"
)

var _ = Describe("prefillBoundKey", func() {
	It("renames the role field of a prefill throughput key", func() {
		Expect(prefillBoundKey("ns|v|m|H200|1|prefill|ilong|noout|q5")).
			To(Equal("ns|v|m|H200|1|prefill-bound|ilong|noout|q5"))
	})
	It("gives no key for another role or another shape", func() {
		Expect(prefillBoundKey("ns|v|m|H200|1|decode|ilong|long|q5")).To(BeEmpty())
		Expect(prefillBoundKey("ns|v|m|H200|1|prefill|noout|q5")).To(BeEmpty())
	})
})

// Issue #144, re-scoped to the capacity side. The fleet: the P/D benchmark's
// two decode replicas and one prefill replica (downstream_saturation_test.go),
// 6000-token prompts. In phase A decode is the saturated role, and prefill
// queues 30 deep on decode's back-pressure while computing 24,000 prompt
// tokens/s -- 4 req/s at 6000 tokens. computeK2 discards that reading for
// prefill's own window, rightly; before this change it was lost, prefill had
// no mu, and the floor gave prefill no term. In phase B decode has recovered,
// prefill's queue is EMPTY, and the router's arrival rate doubles.
var _ = Describe("a prefill lower bound learned while decode meters prefill", func() {
	var (
		analyzer *SaturationAnalyzer
		ctx      context.Context
		clock    time.Time
	)
	const (
		prefillVariant = "prefill-v"
		prefillKv      = int64(1_149_312)
		promptTokens   = 6000.0
		boundRate      = 4.0 // req/s: 24,000 computed tokens/s over 6000-token prompts
		scaleUp        = 0.85
	)
	BeforeEach(func() {
		analyzer = NewSaturationAnalyzer(capacity.NewStore())
		clock = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
		analyzer.now = func() time.Time { return clock }
		ctx = context.Background()
	})
	decode := func(pod string, tokensInUse int64, queue int) domain.ReplicaMetrics {
		rm := makeReplicaMetrics(pod, decodeVariant, tokensInUse, runKvCapacity, queue, promptTokens, 1000)
		rm.RequestRate = 4.73
		rm.GenerationTokenRate = rm.RequestRate * rm.AvgOutputTokens
		return rm
	}
	decodeFull := func() []domain.ReplicaMetrics {
		return []domain.ReplicaMetrics{decode("decode-0", 970_475, 36), decode("decode-1", 1_039_474, 81)}
	}
	decodeCoping := func() []domain.ReplicaMetrics {
		return []domain.ReplicaMetrics{decode("decode-0", 300_000, 0), decode("decode-1", 280_000, 0)}
	}
	// A prefill pod. queue 30 with 357,800 resident is phase A's back-pressure;
	// queue 0 with 20,000 resident is phase B's prefill keeping up.
	prefill := func(pod string, queue int, tokens int64, computed float64) domain.ReplicaMetrics {
		rm := makeReplicaMetrics(pod, prefillVariant, tokens, prefillKv, queue, promptTokens, 1)
		rm.RequestRate = computed / promptTokens
		rm.PrefillComputedTokenRate = computed
		return rm
	}
	states := func(prefillReady, prefillPending int) []domain.VariantReplicaState {
		return []domain.VariantReplicaState{
			{VariantName: decodeVariant, Role: domain.RoleDecode, AcceleratorName: "H200", CurrentReplicas: 2, GPUsPerReplica: 1},
			{VariantName: prefillVariant, Role: domain.RolePrefill, AcceleratorName: "H200",
				CurrentReplicas: prefillReady + prefillPending, PendingReplicas: prefillPending, GPUsPerReplica: 1},
		}
	}
	run := func(rows []domain.ReplicaMetrics, st []domain.VariantReplicaState, lambda float64,
		tweak ...func(*config.ScalingPolicy)) *domain.AnalyzerResult {
		in := makeAnalyzerInput(rows, st)
		in.ArrivalRate = lambda
		for _, t := range tweak {
			t(in.Config.(*config.ScalingPolicy))
		}
		res, err := analyzer.Analyze(ctx, in)
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	boundKey := func() string {
		analyzer.mu.Lock()
		defer analyzer.mu.Unlock()
		for k := range analyzer.saturatedThroughput {
			if strings.Contains(k, "|"+prefillBoundRole+"|") {
				return k
			}
		}
		return ""
	}
	measuredPrefillKeys := func() int {
		analyzer.mu.Lock()
		defer analyzer.mu.Unlock()
		n := 0
		for k := range analyzer.saturatedThroughput {
			if strings.Contains(k, "|"+domain.RolePrefill+"|") {
				n++
			}
		}
		return n
	}
	prefillP := func(res *domain.AnalyzerResult) float64 {
		for _, vc := range res.VariantCapacities {
			if vc.VariantName == prefillVariant {
				return vc.PerReplicaCapacity
			}
		}
		Fail("no capacity for " + prefillVariant)
		return 0
	}
	// prefillOrder is what the engine orders for prefill from this result:
	// RC = D / scaleUp - anticipated supply, in replicas, rounded up as the
	// optimizer does (steadystate.applyUniversalThreshold).
	prefillOrder := func(res *domain.AnalyzerResult, ready, pending int) int {
		rc := res.RoleDemand[domain.RolePrefill]/(scaleUp*prefillP(res)) - float64(ready+pending)
		if rc <= 1e-9 {
			return 0
		}
		return int(math.Ceil(rc - 1e-9))
	}
	// learn runs phase A for n cycles a sample spacing apart.
	learn := func(n int) {
		for i := 0; i < n; i++ {
			clock = clock.Add(ThroughputSampleSpacing)
			// Each reading a little lower than the last: a reading equal to the
			// previous one is a re-read of the same scrape pair and is not a
			// sample of its own (recordSaturatedThroughput). The max is the first.
			computed := boundRate * promptTokens * (1 - 0.01*float64(i))
			run(append(decodeFull(), prefill("prefill-0", 30, 357_800, computed)), states(1, 0), runLambda)
		}
	}
	// recover moves past the decode-saturation memory, so the next cycle
	// reads decode as having recovered.
	recover := func() { clock = clock.Add(DecodeSaturationMemory + time.Second) }

	It("learns the bound from the reading prefill's own window refuses", func() {
		learn(floor.MinThroughputSamplesToOrder)
		Expect(measuredPrefillKeys()).To(BeZero(), "prefill's own window is still not written under decode's metering")
		k := boundKey()
		Expect(k).NotTo(BeEmpty())
		Expect(analyzer.saturatedThroughput[k].Max()).To(BeNumerically("~", boundRate, 1e-9))
		Expect(analyzer.saturatedThroughput[k].Len()).To(Equal(floor.MinThroughputSamplesToOrder))
	})

	It("keeps the max of its readings: each one is a floor on the true rate", func() {
		for _, computed := range []float64{18_000, 24_000, 12_000} {
			clock = clock.Add(ThroughputSampleSpacing)
			run(append(decodeFull(), prefill("prefill-0", 30, 357_800, computed)), states(1, 0), runLambda)
		}
		Expect(analyzer.saturatedThroughput[boundKey()].Max()).To(BeNumerically("~", 24_000/promptTokens, 1e-9))
	})

	It("learns nothing from a prefill that is not queued, not Ready, or has no token rate", func() {
		clock = clock.Add(ThroughputSampleSpacing)
		run(append(decodeFull(), prefill("prefill-0", 2, 357_800, 24_000)), states(1, 0), runLambda)
		notReady := prefill("prefill-0", 30, 357_800, 24_000)
		notReady.Ready = false
		clock = clock.Add(ThroughputSampleSpacing)
		run(append(decodeFull(), notReady), states(1, 0), runLambda)
		clock = clock.Add(ThroughputSampleSpacing)
		run(append(decodeFull(), prefill("prefill-0", 30, 357_800, 0)), states(1, 0), runLambda)
		Expect(boundKey()).To(BeEmpty())
	})

	It("learns and prices nothing when the model turns it off", func() {
		off := func(c *config.ScalingPolicy) { c.DisablePrefillThroughputBound = true }
		for i := 0; i < 2; i++ {
			clock = clock.Add(ThroughputSampleSpacing)
			run(append(decodeFull(), prefill("prefill-0", 30, 357_800, 24_000)), states(1, 0), runLambda, off)
		}
		Expect(boundKey()).To(BeEmpty())
	})

	// The closed loop. Phase B: decode has recovered, prefill's queue is
	// empty, and the router's arrival rate doubles to 12 req/s. lambda over
	// the bound is 3 replicas' worth, 3 / 0.85 = 3.53 of supply, against a
	// fleet of one. The engine's order is replayed each cycle: a replica
	// ordered is pending for one cycle and Ready the next, and every prefill
	// row reads an empty queue -- the queue never forms.
	It("orders prefill on rising router arrivals before its queue forms, and stops at what the bound implies", func() {
		learn(floor.MinThroughputSamplesToOrder)
		recover()

		lambda := 2 * runLambda
		ready, pending := 1, 0
		var orders []int
		for cycle := 0; cycle < 8; cycle++ {
			clock = clock.Add(15 * time.Second)
			rows := decodeCoping()
			for i := 0; i < ready; i++ {
				// Each ready replica computes its share of what arrives; none queues.
				rows = append(rows, prefill("prefill-"+string(rune('0'+i)), 0, 20_000,
					lambda*promptTokens/float64(ready)))
			}
			res := run(rows, states(ready, pending), lambda)
			order := prefillOrder(res, ready, pending)
			orders = append(orders, order)
			ready += pending
			pending = order
		}
		Expect(orders[0]).To(Equal(1),
			"the first cycle of the surge orders prefill with its queue at zero, capped at twice the ready fleet")
		// 12 / 4 / 0.85 = 3.53: the fleet settles on four, never more.
		Expect(ready+pending).To(Equal(4), "converges on lambda / (bound x scaleUp), no ratchet")
		Expect(orders[len(orders)-1]).To(BeZero())
		for _, o := range orders {
			Expect(o).To(BeNumerically("<=", 2), "no cycle more than doubles the ready fleet")
		}
	})

	It("orders nothing on a bound while decode is still saturated: the prefill hold stands", func() {
		learn(floor.MinThroughputSamplesToOrder)
		res := run(append(decodeFull(), prefill("prefill-0", 0, 20_000, 24_000)), states(1, 0), 2*runLambda)
		Expect(prefillOrder(res, 1, 0)).To(BeZero())
	})

	It("never shrinks prefill: a bound above the load leaves occupancy's demand alone", func() {
		learn(floor.MinThroughputSamplesToOrder)
		recover()
		rows := append(decodeCoping(), prefill("prefill-0", 0, 600_000, 1_000), prefill("prefill-1", 0, 600_000, 1_000))
		with := run(rows, states(2, 0), 0.5)
		without := NewSaturationAnalyzer(capacity.NewStore())
		without.now = analyzer.now
		in := makeAnalyzerInput(rows, states(2, 0))
		in.ArrivalRate = 0.5
		base, err := without.Analyze(ctx, in)
		Expect(err).NotTo(HaveOccurred())
		Expect(with.RoleDemand[domain.RolePrefill]).To(Equal(base.RoleDemand[domain.RolePrefill]))
	})

	It("gives way to prefill's own window once prefill saturates on its own", func() {
		learn(floor.MinThroughputSamplesToOrder)
		recover()
		// Decode coping, prefill queued: prefill's own reading, 6 req/s.
		for i := 0; i < floor.MinThroughputSamplesToOrder; i++ {
			clock = clock.Add(ThroughputSampleSpacing)
			run(append(decodeCoping(), prefill("prefill-0", 30, 357_800, 36_000)), states(1, 0), runLambda)
		}
		Expect(measuredPrefillKeys()).To(Equal(1))
		res := run(append(decodeCoping(), prefill("prefill-0", 0, 20_000, 36_000)), states(1, 0), 2*runLambda)
		for _, vc := range res.VariantCapacities {
			if vc.VariantName == prefillVariant {
				Expect(vc.ServiceRate).NotTo(BeNil())
				Expect(vc.ServiceRate.RateSource).To(Equal(domain.ServiceRateSourceMeasured))
			}
		}
	})

	It("reports the bound as the variant's service-rate source while it prices", func() {
		learn(floor.MinThroughputSamplesToOrder)
		recover()
		res := run(append(decodeCoping(), prefill("prefill-0", 0, 20_000, 24_000)), states(1, 0), 2*runLambda)
		for _, vc := range res.VariantCapacities {
			if vc.VariantName == prefillVariant {
				Expect(vc.ServiceRate).NotTo(BeNil())
				Expect(vc.ServiceRate.RateSource).To(Equal(domain.ServiceRateSourceLowerBound))
				Expect(vc.ServiceRate.Rate).To(BeNumerically("~", boundRate, 1e-9))
			}
		}
	})

	It("is exported with the rest of the learned state, so it survives a restart", func() {
		learn(floor.MinThroughputSamplesToOrder)
		s := analyzer.ExportLearnedState()
		k := boundKey()
		Expect(s.Throughput).To(HaveKey(k))
		Expect(s.Throughput[k].Values).To(HaveLen(floor.MinThroughputSamplesToOrder))
	})
})

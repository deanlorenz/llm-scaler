package slo

import (
	"context"
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

var _ = Describe("Analyzer state and inputs", func() {
	var (
		clock time.Time
		ctx   = context.Background()
		agg   = domain.VariantReplicaState{VariantName: "agg", Role: domain.RoleBoth,
			AcceleratorName: "H100", GPUsPerReplica: 1, CurrentReplicas: 2}
	)
	BeforeEach(func() {
		clock = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	})
	newAt := func(store *capacity.Store) *Analyzer {
		a := New(nil, store)
		a.now = func() time.Time { return clock }
		return a
	}
	// warmWith runs five cycles of rows whose fills span 0.45 of k, so the
	// window fits a full line, with mutate applied to every row.
	warmWith := func(a *Analyzer, mutate func(*domain.ReplicaMetrics)) *domain.AnalyzerResult {
		var res *domain.AnalyzerResult
		for i := range 5 {
			rows := cycleRows("agg", i)
			for j := range rows {
				mutate(&rows[j])
			}
			var err error
			res, err = a.Analyze(ctx, domain.AnalyzerInput{
				ModelID: "m", Namespace: "ns", Config: policy(44*time.Millisecond, 0), ArrivalRate: 2,
				VariantStates: []domain.VariantReplicaState{agg}, ReplicaMetrics: rows,
			})
			Expect(err).NotTo(HaveOccurred())
			clock = clock.Add(15 * time.Second)
		}
		return res
	}
	noop := func(*domain.ReplicaMetrics) {}

	It("drops a variant's window once it has not been seen for the timeout", func() {
		a := newAt(nil)
		warmWith(a, noop)
		key := windowKey("ns", "m", agg)
		Expect(a.windows).To(HaveKey(key))
		Expect(a.baseline).To(HaveKey(key))

		clock = clock.Add(windowTimeout + time.Minute)
		other := domain.VariantReplicaState{VariantName: "other", Role: domain.RoleBoth, CurrentReplicas: 1}
		_, err := a.Analyze(ctx, domain.AnalyzerInput{ModelID: "m2", Namespace: "ns",
			Config: policy(44*time.Millisecond, 0), VariantStates: []domain.VariantReplicaState{other}})
		Expect(err).NotTo(HaveOccurred())
		Expect(a.windows).NotTo(HaveKey(key))
		Expect(a.baseline).NotTo(HaveKey(key))
		Expect(a.seen).NotTo(HaveKey(key))
	})

	It("keeps a window seen within the timeout", func() {
		a := newAt(nil)
		warmWith(a, noop)
		a.sweep(clock.Add(windowTimeout - time.Minute))
		Expect(a.windows).To(HaveKey(windowKey("ns", "m", agg)))
	})

	Context("engine params from the capacity store", func() {
		store := func(params capacity.EngineParams) *capacity.Store {
			s := capacity.NewStore()
			s.Update("ns", "m", "agg", capacity.Record{EngineParams: &params})
			return s
		}

		It("caps the resident sequences at max_num_seqs and prices the fill the engine can reach", func() {
			res := warmWith(newAt(store(capacity.EngineParams{MaxNumSeqs: 64})), noop)
			// k = 0.6 implies 120 sequences; the engine admits 64, so the cache
			// never fills past 64 * 500 / 100000 = 0.32, and 64 sequences
			// advance at ITL(0.32) = 32.8 ms, not ITL(0.6).
			Expect(res.VariantCapacities[0].PerReplicaCapacity).To(BeNumerically("~", 64/line.ITLAt(0.32), 1e-6))
		})

		It("reads the configured KV override where the live reading is absent or the overflow sentinel", func() {
			for _, live := range []int64{0, math.MaxInt64} {
				res := warmWith(newAt(store(capacity.EngineParams{TotalKvTokensOverride: 200000})),
					func(rm *domain.ReplicaMetrics) { rm.TotalKvCapacityTokens = live })
				// 0.6 * 200000 / 500 = 240 sequences.
				Expect(res.VariantCapacities[0].PerReplicaCapacity).To(BeNumerically("~", 240/0.044, 1e-6),
					"live capacity %d", live)
			}
		})

		It("averages the capacity over the variant's ready replicas", func() {
			res := warmWith(newAt(nil), func(rm *domain.ReplicaMetrics) {
				if rm.PodName == "agg-b" {
					rm.TotalKvCapacityTokens = 300000
				}
			})
			// The mean of 100k and 300k: 0.6 * 200000 / 500 = 240 sequences.
			Expect(res.VariantCapacities[0].PerReplicaCapacity).To(BeNumerically("~", 240/0.044, 1e-6))
		})
	})

	It("is input-invalid, an error and not no-data, when the variant has no KV capacity at all", func() {
		a := newAt(nil)
		res := warmWith(a, func(rm *domain.ReplicaMetrics) { rm.TotalKvCapacityTokens = 0 })
		Expect(res.VariantCapacities[0].Reason).To(Equal(domain.ReasonError))
		Expect(res.VariantCapacities[0].PerReplicaCapacity).To(BeZero())

		zeroCap := row("agg", "agg-a", 0.5)
		zeroCap.TotalKvCapacityTokens = 0
		_, reading := a.priceVariant(domain.AnalyzerInput{Namespace: "ns", ModelID: "m"}, agg,
			readFleet(domain.AnalyzerInput{VariantStates: []domain.VariantReplicaState{agg},
				ReplicaMetrics: []domain.ReplicaMetrics{zeroCap}}),
			0.044, 0.85, clock)
		Expect(reading.Source).To(Equal(constants.SLOSourceInputInvalid))
	})

	It("holds B at the last full fit's intercept once the fleet stops spanning k", func() {
		a := newAt(nil)
		// The true intercept here is 30 ms, far from the 6 ms default floor.
		steep := func(k float64) float64 { return 0.040*k + 0.030 }
		key := windowKey("ns", "m", agg)
		var src string
		for i := range 5 {
			rows := cycleRows("agg", i)
			for j := range rows {
				rows[j].AvgITL = steep(rows[j].KvUsageInstant)
			}
			_, src = a.fitLine(key, rows, "agg", clock)
			clock = clock.Add(15 * time.Second)
		}
		Expect(src).To(Equal(constants.SLOSourceOwn))
		Expect(a.baseline[key]).To(BeNumerically("~", 0.030, 1e-9))

		// Twenty readings at nearly one fill push the spread-out ones out of
		// the window, so only a pinned fit is possible.
		var m itl.Model
		for range 10 {
			rows := []domain.ReplicaMetrics{row("agg", "agg-a", 0.50), row("agg", "agg-b", 0.52)}
			for j := range rows {
				rows[j].AvgITL = steep(rows[j].KvUsageInstant)
			}
			m, src = a.fitLine(key, rows, "agg", clock)
			clock = clock.Add(15 * time.Second)
		}
		Expect(src).To(Equal(constants.SLOSourcePinned))
		Expect(m.B).To(BeNumerically("~", 0.030, 1e-9), "the learned intercept, not itl.DefaultBaselineSec")
	})

	It("splits demand across generating variants by their ready replicas, and reports utilization", func() {
		a := newAt(nil)
		two := domain.VariantReplicaState{VariantName: "agg2", Role: domain.RoleBoth,
			AcceleratorName: "H100", GPUsPerReplica: 1, CurrentReplicas: 1}
		var res *domain.AnalyzerResult
		for i := range 5 {
			rows := append(cycleRows("agg", i), row("agg2", "agg2-a", 0.20+0.1*float64(i)))
			var err error
			res, err = a.Analyze(ctx, domain.AnalyzerInput{
				ModelID: "m", Namespace: "ns", Config: policy(44*time.Millisecond, 0), ArrivalRate: 3,
				VariantStates: []domain.VariantReplicaState{agg, two}, ReplicaMetrics: rows,
			})
			Expect(err).NotTo(HaveOccurred())
			clock = clock.Add(15 * time.Second)
		}
		Expect(res.TotalDemand).To(BeNumerically("~", 3000, 1e-9))
		byName := map[string]domain.VariantCapacity{}
		for _, vc := range res.VariantCapacities {
			byName[vc.VariantName] = vc
		}
		Expect(byName["agg"].TotalDemand).To(BeNumerically("~", 2000, 1e-9))
		Expect(byName["agg2"].TotalDemand).To(BeNumerically("~", 1000, 1e-9))
		vc := byName["agg"]
		Expect(vc.PerReplicaCapacity).To(BeNumerically(">", 0))
		Expect(vc.Utilization).To(BeNumerically("~", vc.TotalDemand/(2*vc.PerReplicaCapacity), 1e-12))
	})
})

var _ = Describe("judge's sample minimums and labels", func() {
	judge := func(rl *domain.RouterLatency, slo *config.SLOPolicy) []metrics.SLOObjectiveReading {
		return New(fakeRouter{latency: rl}, nil).judge(context.Background(),
			domain.AnalyzerInput{ModelID: "m", Namespace: "ns"}, slo)
	}

	It("reports no TPOT mean from fewer than 50 requests, and none from zero", func() {
		for _, rate := range []float64{0.1, 0} { // 30 requests, then none
			out := judge(&domain.RouterLatency{WindowSeconds: 300, TPOTSumRate: rate * 0.08, TPOTCountRate: rate},
				policy(100*time.Millisecond, 0).SLO)
			Expect(math.IsNaN(out[0].Observed)).To(BeTrue(), "rate %v", rate)
		}
	})

	It("labels each objective with its statistic", func() {
		slo := policy(100*time.Millisecond, 3*time.Second).SLO
		slo.TTFT.Percentile = 99
		out := judge(nil, slo)
		Expect(out).To(HaveLen(2))
		Expect(out[0].Objective).To(Equal(constants.SLOObjectiveTPOT))
		Expect(out[0].Statistic).To(Equal("mean"))
		Expect(out[1].Objective).To(Equal(constants.SLOObjectiveTTFT))
		Expect(out[1].Statistic).To(Equal("p99"))
	})

	It("asks a p99 for a thousand requests", func() {
		slo := policy(0, 3*time.Second).SLO
		slo.TTFT.Percentile = 99
		short := judge(&domain.RouterLatency{WindowSeconds: 300, // 900 requests
			TTFTBuckets: buckets(1, 2.9, 3, 2.97, inf, 3.0)}, slo)
		Expect(math.IsNaN(short[0].Observed)).To(BeTrue())
		enough := judge(&domain.RouterLatency{WindowSeconds: 300, // 1200 requests
			TTFTBuckets: buckets(1, 3.9, 3, 3.96, inf, 4.0)}, slo)
		Expect(enough[0].Observed).To(BeNumerically("~", 3.0, 1e-9))
		Expect(enough[0].Attainment).To(BeNumerically("~", 0.99, 1e-12))
	})
})

var _ = Describe("judge's request counts", func() {
	It("reports how many requests the router saw, even below the minimum", func() {
		slo := policy(100*time.Millisecond, 3*time.Second).SLO
		out := New(fakeRouter{latency: &domain.RouterLatency{WindowSeconds: 300,
			TPOTSumRate: 0.008, TPOTCountRate: 0.1, TTFTBuckets: buckets(1, 0.18, 3, 0.19, inf, 0.2)}}, nil).
			judge(context.Background(), domain.AnalyzerInput{ModelID: "m", Namespace: "ns"}, slo)
		Expect(out[0].Requests).To(BeNumerically("~", 30, 1e-9))
		Expect(math.IsNaN(out[0].Observed)).To(BeTrue())
		Expect(out[1].Requests).To(BeNumerically("~", 60, 1e-9))
		Expect(math.IsNaN(out[1].Observed)).To(BeTrue())
	})

	It("reports no request count when the router reported nothing", func() {
		out := New(fakeRouter{}, nil).judge(context.Background(),
			domain.AnalyzerInput{ModelID: "m", Namespace: "ns"}, policy(0, 3*time.Second).SLO)
		Expect(math.IsNaN(out[0].Requests)).To(BeTrue())
	})
})

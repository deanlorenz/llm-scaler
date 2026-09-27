package saturation_v2

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
)

// The fit the 2026-09-23 shape-swap runs were taken on, and the card they ran
// on: ITL(k) = 30.7 ms*k + 1.6 ms over 70 intervals, 1,163,136 KV tokens,
// max_num_seqs 256.
var (
	tracedModel  = itl.Model{A: 0.0307, B: 0.0016}
	tracedParams = &capacity.EngineParams{MaxNumSeqs: 256}
	tracedKv     = int64(1_163_136)
	// The operating point the analyzer targets: k1 is KvCacheThreshold of
	// physical KV and it scales out at ScaleUpThreshold of k1, so mu is priced
	// at the product -- 0.68 on the defaults, not 0.85.
	tracedK = config.DefaultKvCacheThreshold * config.DefaultScaleUpThreshold
)

// The fit from run P (2026-09-27), the first pass of this trace where the fleet
// was never short of replicas: ITL(k) = 34.4 ms*k + 0.61 ms over the 88
// intervals with k in [0.15, 0.75], mean residual 6.4%. Same card as the
// traced runs above: 1,163,136 KV tokens, max_num_seqs 256.
var runPModel = itl.Model{A: 0.0344, B: 0.00061}

var _ = Describe("pricingK", func() {
	It("is the two thresholds multiplied, because they compound", func() {
		// k1 is KvCacheThreshold of physical KV and the analyzer scales out at
		// ScaleUpThreshold of k1, so a replica is full at the product.
		Expect(pricingK(&config.ScalingPolicy{
			KvCacheThreshold: 0.80, ScaleUpThreshold: 0.85,
		})).To(BeNumerically("~", 0.68, 1e-9))

		Expect(pricingK(&config.ScalingPolicy{
			KvCacheThreshold: 0.80, ScaleUpThreshold: 0.95,
		})).To(BeNumerically("~", 0.76, 1e-9))
	})

	It("stays inside the range the ITL line was fitted over", func() {
		// The line says nothing outside the window's bounds, so a configuration
		// whose product falls outside them is clamped rather than extrapolated.
		Expect(pricingK(&config.ScalingPolicy{
			KvCacheThreshold: 1.0, ScaleUpThreshold: 1.0,
		})).To(Equal(itl.DefaultMaxObservableK))

		Expect(pricingK(&config.ScalingPolicy{
			KvCacheThreshold: 0.1, ScaleUpThreshold: 0.1,
		})).To(Equal(itl.DefaultMinObservableK))

		// Including the fallback: DefaultKSat is 0.85 and the window ends at
		// 0.80, so returning it unclamped would evaluate the line outside the
		// range it was fitted over -- what this function exists to prevent.
		Expect(pricingK(nil)).To(Equal(itl.DefaultMaxObservableK))
	})
})

var _ = Describe("deriveMu against run P", func() {
	const kRunP = 0.80 * 0.85

	It("agrees with the mu the fleet measured, where it measured one", func() {
		// Phase 1 saturated, so the floor had a reading of its own to compare
		// against: it used 1.27 requests a second for the whole phase.
		got := deriveMu(runPModel, tracedParams, tracedKv, shape.New(1000, 6000, 0), kRunP)
		Expect(got.ok).To(BeTrue())
		Expect(got.rate).To(BeNumerically("~", 1.27, 0.15),
			"a derived mu that disagrees with a measured one is not describing this card")
	})

	It("tracks the shape change the measured mu could not", func() {
		// Phase 2 is 8000/1000: six times shorter generations, so roughly six
		// times the request rate per replica. The floor went on using 1.31 --
		// the phase 1 figure -- for all nineteen minutes, and held 6-7 replicas
		// at 1.2% KV utilisation and an empty queue.
		got := deriveMu(runPModel, tracedParams, tracedKv, shape.New(8000, 1000, 0), kRunP)
		Expect(got.ok).To(BeTrue())
		Expect(got.rate).To(BeNumerically("~", 3.89, 0.2))

		const measuredArrival = 5.86
		Expect(measuredArrival/got.rate).To(BeNumerically("<", 2.0),
			"the arrival rate over a shape-correct mu is a small fleet")
		Expect(measuredArrival/1.31).To(BeNumerically(">", 4.0),
			"where the stale figure ordered four and a half replicas of capacity")
	})
})

var _ = Describe("deriveMu", func() {
	It("reproduces the mu the fleet measured for itself under the first shape", func() {
		// 1000-token prompts, 6000-token generations. Saturated, the fleet
		// recorded 1.4292 and 1.5429 requests a second across the two runs.
		got := deriveMu(tracedModel, tracedParams, tracedKv, shape.New(1000, 6000, 0), tracedK)
		Expect(got.ok).To(BeTrue())
		Expect(got.rate).To(BeNumerically("~", 1.49, 0.12),
			"the model has to land where the hardware did, or it is not describing it")
	})

	It("prices the second shape, which the fleet never measured at all", func() {
		// 8000-token prompts, 1000-token generations. The fleet was
		// over-provisioned for this and never saturated under it, so no reading
		// was ever taken -- the floor went on using the first shape's figure
		// for the remaining nineteen minutes of the run.
		got := deriveMu(tracedModel, tracedParams, tracedKv, shape.New(8000, 1000, 0), tracedK)
		Expect(got.ok).To(BeTrue())
		Expect(got.rate).To(BeNumerically(">", 3.0))
		Expect(6.0/got.rate).To(BeNumerically("<", 2.5),
			"the arrival rate over a shape-correct mu is a small fleet, not the seven that ran")
	})

	It("prices a fully cached workload, whose prompts occupy nothing", func() {
		// ILeff = IL*(1 - hitRate), so a workload served entirely from the
		// prefix cache has an effective prompt length of zero and a footprint
		// of OL/2 alone. That is a real shape, not a missing one, and it holds
		// more sequences per replica than the same workload uncached.
		cached := deriveMu(tracedModel, tracedParams, tracedKv, shape.New(1000, 6000, 1.0), tracedK)
		plain := deriveMu(tracedModel, tracedParams, tracedKv, shape.New(1000, 6000, 0), tracedK)
		Expect(cached.ok).To(BeTrue())
		Expect(cached.seqs).To(BeNumerically(">", plain.seqs),
			"a cached prompt leaves room for more resident sequences")
		Expect(cached.rate).To(BeNumerically(">", plain.rate))
	})

	It("is capped by max_num_seqs, which the trace's second phase sat on", func() {
		// The cache alone implies thousands of resident sequences for a short
		// shape; the engine admits 256, and phase 2 ran at exactly that. The
		// uncapped figure is asserted against the arithmetic rather than a
		// constant, because it moves with the pricing point: it was ~6,591 when
		// mu was priced at 0.85 of physical KV and is ~5,273 at the 0.68 the
		// analyzer actually targets.
		got := deriveMu(tracedModel, tracedParams, tracedKv, shape.New(100, 100, 0), tracedK)
		Expect(got.ok).To(BeTrue())
		Expect(got.seqs).To(Equal(float64(tracedParams.MaxNumSeqs)))

		uncapped := deriveMu(tracedModel, &capacity.EngineParams{}, tracedKv, shape.New(100, 100, 0), tracedK)
		Expect(uncapped.seqs).To(BeNumerically("~", tracedK*float64(tracedKv)/150, 1),
			"uncapped, it is the cache's own number: kPrice x C / KVreq")
		Expect(uncapped.seqs).To(BeNumerically(">", 20*float64(tracedParams.MaxNumSeqs)),
			"and that is a number the engine will not admit")
	})

	It("declines rather than guessing", func() {
		Expect(deriveMu(itl.Model{}, tracedParams, tracedKv, shape.New(1000, 6000, 0), tracedK).ok).To(BeFalse(),
			"no fitted model")
		Expect(deriveMu(tracedModel, tracedParams, 0, shape.New(1000, 6000, 0), tracedK).ok).To(BeFalse(),
			"no KV capacity")
		Expect(deriveMu(tracedModel, tracedParams, tracedKv, shape.New(1000, 0, 0), tracedK).ok).To(BeFalse(),
			"no generation length is not a decode shape")
		Expect(deriveMu(tracedModel, tracedParams, tracedKv, shape.New(1000, 6000, 0), 0).ok).To(BeFalse(),
			"a kPrice of zero is not a utilization")
		Expect(deriveMu(tracedModel, tracedParams, tracedKv, shape.New(1000, 6000, 0), -0.1).ok).To(BeFalse(),
			"nor is a negative one")
		Expect(deriveMu(tracedModel, tracedParams, tracedKv, shape.New(1000, 6000, 0), 1.01).ok).To(BeFalse(),
			"nor is one above full")
		Expect(deriveMu(tracedModel, tracedParams, tracedKv, shape.Shape{}, tracedK).ok).To(BeFalse(),
			"an empty shape has no footprint to divide the cache by")
		Expect(deriveMu(itl.Model{A: -1, B: 0.5}, tracedParams, tracedKv, shape.New(1000, 6000, 0), tracedK).ok).To(BeFalse(),
			"a model whose reading at k_sat is not positive")
	})
})

var _ = Describe("the floor's mu, through Analyze", func() {
	const variant = "decode-v"

	// A fleet whose replicas sit at a spread of loads, each reporting the ITL
	// the traced model predicts for its own k, so the fit recovers that model.
	// Ten of them clears the window's sample count and its k-spread in one
	// cycle.
	fleet := func(n int, avgIn, avgOut float64, queue int, tokens int64) domain.AnalyzerInput {
		rms := make([]domain.ReplicaMetrics, 0, n)
		for i := 0; i < n; i++ {
			k := 0.20 + 0.06*float64(i)
			rm := makeReplicaMetrics(fmt.Sprintf("d%d", i), variant, tokens, tracedKv, queue, avgIn, avgOut)
			rm.Ready = true
			rm.KvUsageInstant = k
			rm.AvgITL = tracedModel.ITLAt(k)
			rm.RequestRate = 0.6
			rm.GenerationTokenRate = rm.RequestRate * avgOut
			rms = append(rms, rm)
		}
		in := makeAnalyzerInput(rms, []domain.VariantReplicaState{
			{VariantName: variant, Role: domain.RoleDecode, AcceleratorName: "H200",
				CurrentReplicas: n, GPUsPerReplica: 1},
		})
		in.ArrivalRate = 6
		return in
	}

	// What the floor asked for, in replicas: the role's demand over what one
	// replica of it supplies.
	impliedReplicas := func(res *domain.AnalyzerResult) float64 {
		var perReplica float64
		for _, vc := range res.VariantCapacities {
			if vc.VariantName == variant {
				perReplica = vc.PerReplicaCapacity
			}
		}
		Expect(perReplica).To(BeNumerically(">", 0))
		return res.RoleDemand[domain.RoleDecode] / perReplica
	}

	It("prices the shape arriving now, on the first cycle it is seen", func() {
		a := NewSaturationAnalyzer(capacity.NewStore())
		ctx := context.Background()

		// One cycle of the first shape, saturated, is enough to fit ITL(k) --
		// ten replicas at a spread of loads clears the window in one go.
		_, err := a.Analyze(ctx, fleet(10, 1000, 6000, 10, 900_000))
		Expect(err).NotTo(HaveOccurred())

		// The shape swaps to short generations and long prompts, and the fleet
		// is now far too big for it: nothing is queued and occupancy has
		// collapsed. On the measured path nothing would saturate again, so the
		// floor would go on pricing 6 req/s against the 6000-token figure for
		// the rest of the run -- which is what the fleet did on 2026-09-23,
		// holding seven replicas against a utilization of 0.028.
		res, err := a.Analyze(ctx, fleet(10, 8000, 1000, 0, 17_282))
		Expect(err).NotTo(HaveOccurred())

		Expect(impliedReplicas(res)).To(BeNumerically("<", 2.5),
			"a mu derived for the shape now arriving asks for one or two replicas")
	})

	It("carries the fit across a shape change instead of refitting", func() {
		// The window is not cleared when the shape moves, and this is the spec
		// that holds it to that: cycle 2 brings only three replicas, which on
		// their own are short of DefaultMinSamples and of the k-spread, so a
		// model can only exist here if cycle 1's readings are still in the
		// window. ITL(k) describes the hardware, not the shape, so they are.
		a := NewSaturationAnalyzer(capacity.NewStore())
		ctx := context.Background()

		_, err := a.Analyze(ctx, fleet(10, 1000, 6000, 10, 900_000))
		Expect(err).NotTo(HaveOccurred())

		res, err := a.Analyze(ctx, fleet(3, 8000, 1000, 0, 17_282))
		Expect(err).NotTo(HaveOccurred())
		Expect(impliedReplicas(res)).To(BeNumerically("<", 2.5),
			"three fresh readings cannot fit a model; the carried-over ones can")
	})

	It("falls back to the measured window when no model can be fitted", func() {
		// Ten replicas, so the sample count is not what stops it -- every one
		// of them reports no inter-token latency, which is the exclusion under
		// test. The floor must still bind, on the measured reading, rather than
		// the role quietly losing its floor.
		a := NewSaturationAnalyzer(capacity.NewStore())
		in := fleet(10, 1000, 6000, 10, 900_000)
		for i := range in.ReplicaMetrics {
			in.ReplicaMetrics[i].AvgITL = 0
		}
		res, err := a.Analyze(context.Background(), in)
		Expect(err).NotTo(HaveOccurred())

		// The measured mu here is the fleet's own completion rate, 0.6 req/s
		// against 6 arriving, so the floor asks for far more than the derived
		// path would -- which is the point: this is the old answer, and it is
		// still available when the new one is not.
		Expect(impliedReplicas(res)).To(BeNumerically(">", 2.5),
			"with no model the floor is back on the measured reading")
	})
})

var _ = Describe("noteITL", func() {
	const variant = "decode-v"

	reading := func(pod string, k, avgITL float64) domain.ReplicaMetrics {
		rm := makeReplicaMetrics(pod, variant, 900_000, tracedKv, 0, 1000, 6000)
		rm.Ready = true
		rm.KvUsageInstant = k
		rm.AvgITL = avgITL
		return rm
	}
	// Ten readings on the traced line, enough to fit it.
	line := func() []domain.ReplicaMetrics {
		out := make([]domain.ReplicaMetrics, 0, 10)
		for i := 0; i < 10; i++ {
			k := 0.20 + 0.06*float64(i)
			out = append(out, reading(fmt.Sprintf("d%d", i), k, tracedModel.ITLAt(k)))
		}
		return out
	}
	fit := func(rms []domain.ReplicaMetrics) itl.Model {
		a := NewSaturationAnalyzer(capacity.NewStore())
		return a.noteITL("ns|model|"+variant, rms, variant, a.now(), logr.Discard())
	}

	It("fits the line its replicas are reporting", func() {
		got := fit(line())
		Expect(got.IsZero()).To(BeFalse())
		Expect(got.A).To(BeNumerically("~", tracedModel.A, 1e-6))
		Expect(got.B).To(BeNumerically("~", tracedModel.B, 1e-6))
	})

	DescribeTable("leaves out what is not a reading of this variant's own replicas",
		func(spoil func(*domain.ReplicaMetrics)) {
			rms := line()
			for i := range rms {
				spoil(&rms[i])
			}
			Expect(fit(rms).IsZero()).To(BeTrue())
		},
		Entry("another variant's", func(rm *domain.ReplicaMetrics) { rm.VariantName = "other-v" }),
		Entry("a pod still failing readiness", func(rm *domain.ReplicaMetrics) { rm.Ready = false }),
		Entry("a warm-pool bridge on the pool's own settings", func(rm *domain.ReplicaMetrics) { rm.FromWarmPool = true }),
		Entry("no inter-token latency", func(rm *domain.ReplicaMetrics) { rm.AvgITL = 0 }),
		Entry("no utilization to place it at", func(rm *domain.ReplicaMetrics) { rm.KvUsageInstant = 0 }),
		Entry("a load above the band the line was fitted in", func(rm *domain.ReplicaMetrics) {
			rm.KvUsageInstant = itl.DefaultMaxObservableK + 0.05
		}),
	)

	It("fits a balanced fleet, which is the fleet a router produces", func() {
		// Every replica at the same load and the same latency: no spread in k,
		// so the window is never Ready and the two-parameter fit has nothing to
		// work with. This is not a corner case -- it is what a run on a working
		// fleet looks like, and requiring the full fit left the derivation
		// inert on 2026-09-24 with every replica at queue 0.
		rms := make([]domain.ReplicaMetrics, 0, 6)
		for i := 0; i < 6; i++ {
			rms = append(rms, reading(fmt.Sprintf("d%d", i), 0.62, 0.0272))
		}
		// The SPREAD requirement is what a balanced fleet cannot meet and is
		// waived; the sample floor is not, so one cycle of six is not enough
		// and two are. Driven through one analyzer, because the window is what
		// accumulates and `fit` builds a fresh one each call.
		a := NewSaturationAnalyzer(capacity.NewStore())
		key := "ns|model|" + variant
		Expect(a.noteITL(key, rms, variant, a.now(), logr.Discard()).IsZero()).To(BeTrue(),
			"six readings is under DefaultMinSamples: nothing is derived yet")
		got := a.noteITL(key, rms, variant, a.now(), logr.Discard())
		Expect(got.IsZero()).To(BeFalse(),
			"a balanced fleet still gets a model, with B pinned")
		Expect(got.B).To(Equal(itl.DefaultBaselineSec))
		Expect(got.ITLAt(0.62)).To(BeNumerically("~", 0.0272, 1e-9),
			"and it passes through the load the fleet is actually at")
	})

	It("keeps the good readings when only some are excluded", func() {
		rms := line()
		rms = append(rms, reading("warm", 0.5, 0.02))
		rms[len(rms)-1].FromWarmPool = true
		rms = append(rms, reading("other", 0.5, 0.02))
		rms[len(rms)-1].VariantName = "other-v"

		got := fit(rms)
		Expect(got.IsZero()).To(BeFalse())
		Expect(got.A).To(BeNumerically("~", tracedModel.A, 1e-6),
			"the excluded readings are off the line and would drag the fit")
	})
})

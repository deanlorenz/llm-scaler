package metrics

import (
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
)

var _ = Describe("PublishSLO", func() {
	var registry *prometheus.Registry
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

	family := func(name string) []*dto.Metric {
		mfs, err := registry.Gather()
		Expect(err).NotTo(HaveOccurred())
		for _, mf := range mfs {
			if mf.GetName() == name {
				return mf.GetMetric()
			}
		}
		return nil
	}
	label := func(m *dto.Metric, name string) string {
		for _, l := range m.GetLabel() {
			if l.GetName() == name {
				return l.GetValue()
			}
		}
		return ""
	}
	snapshot := func(model string, variants ...SLOVariantReading) SLOModelSnapshot {
		return SLOModelSnapshot{
			Namespace: "ns", ModelName: model,
			Objectives: []SLOObjectiveReading{
				{Objective: constants.SLOObjectiveTTFT, Statistic: "p90", TargetSeconds: 3, Observed: 1.2, Attainment: 0.96},
				{Objective: constants.SLOObjectiveTPOT, Statistic: "mean", TargetSeconds: 0.1,
					Observed: math.NaN(), Attainment: math.NaN()},
			},
			Variants: variants,
		}
	}

	BeforeEach(func() {
		registry = prometheus.NewRegistry()
		Expect(InitMetrics(registry)).To(Succeed())
	})

	Describe("the TTFT series", func() {
		reading := func() SLOTTFTReading {
			return SLOTTFTReading{Variant: "pre", Statistic: "p90", Predicted: 1.5, PerReplica: 2, VPSource: "busy-minutes",
				Reason: constants.SLOTTFTReasonPriced, Flags: []string{"UnvalidatedTTFT", "AssumedPoissonArrivals"}}
		}
		publish := func(t SLOTTFTReading) {
			snap := snapshot("m")
			snap.TTFT = []SLOTTFTReading{t}
			PublishSLO(snap, t0)
		}
		value := func(name string) []float64 {
			var out []float64
			for _, m := range family(name) {
				out = append(out, m.GetGauge().GetValue())
			}
			return out
		}
		oneHot := func(name, labelName string) []string {
			var on []string
			for _, m := range family(name) {
				if m.GetGauge().GetValue() == 1 {
					on = append(on, label(m, labelName))
				}
			}
			return on
		}

		It("publishes a priced variant under the four labels, and zero-fills exactly the three lists", func() {
			publish(reading())
			Expect(value(constants.WVASLOPredictedTTFTSeconds)).To(Equal([]float64{1.5}))
			Expect(value(constants.WVASLOTTFTReplicaRequestsPerSecond)).To(Equal([]float64{2.0}))
			Expect(value(constants.WVASLOTTFTUnmet)).To(Equal([]float64{0.0}))
			m := family(constants.WVASLOPredictedTTFTSeconds)[0]
			Expect(label(m, constants.LabelVariantName)).To(Equal("pre"))
			Expect(label(m, constants.LabelStatistic)).To(Equal("p90"))

			Expect(family(constants.WVASLOTTFTFlag)).To(HaveLen(len(constants.SLOTTFTFlags)))
			Expect(family(constants.WVASLOTTFTReason)).To(HaveLen(len(constants.SLOTTFTReasons)))
			Expect(family(constants.WVASLOTTFTVPSource)).To(HaveLen(len(constants.SLOTTFTVPSources)))
			var flags []string
			for _, m := range family(constants.WVASLOTTFTFlag) {
				flags = append(flags, label(m, constants.LabelFlag))
			}
			Expect(flags).To(ConsistOf(constants.SLOTTFTFlags))
			Expect(oneHot(constants.WVASLOTTFTFlag, constants.LabelFlag)).To(ConsistOf("UnvalidatedTTFT", "AssumedPoissonArrivals"))
			Expect(oneHot(constants.WVASLOTTFTReason, constants.LabelReason)).To(ConsistOf(constants.SLOTTFTReasonPriced))
			Expect(oneHot(constants.WVASLOTTFTVPSource, constants.LabelVPSource)).To(ConsistOf("busy-minutes"))
		})

		It("reads a refusal as a reason: no number, no unmet", func() {
			publish(SLOTTFTReading{Variant: "pre", Statistic: "p90", Predicted: math.NaN(), PerReplica: math.NaN(),
				VPSource: constants.SLOTTFTVPNone, Reason: constants.SLOTTFTReasonNoPrefillCapacity})
			Expect(family(constants.WVASLOPredictedTTFTSeconds)).To(BeEmpty())
			Expect(family(constants.WVASLOTTFTReplicaRequestsPerSecond)).To(BeEmpty())
			Expect(family(constants.WVASLOTTFTUnmet)).To(BeEmpty())
			Expect(oneHot(constants.WVASLOTTFTReason, constants.LabelReason)).To(ConsistOf(constants.SLOTTFTReasonNoPrefillCapacity))
		})

		It("reads a rate past the knee as unmet with no prediction", func() {
			r := reading()
			r.Predicted, r.Unmet = math.NaN(), true
			publish(r)
			Expect(family(constants.WVASLOPredictedTTFTSeconds)).To(BeEmpty())
			Expect(value(constants.WVASLOTTFTUnmet)).To(Equal([]float64{1.0}))
		})

		It("deletes the series when the reading goes", func() {
			publish(reading())
			PublishSLO(snapshot("m"), t0)
			Expect(family(constants.WVASLOTTFTFlag)).To(BeEmpty())
			Expect(family(constants.WVASLOTTFTReason)).To(BeEmpty())
			Expect(family(constants.WVASLOPredictedTTFTSeconds)).To(BeEmpty())
		})

		It("says it is not a bound, in its help", func() {
			publish(reading())
			mfs, err := registry.Gather()
			Expect(err).NotTo(HaveOccurred())
			for _, mf := range mfs {
				if mf.GetName() == constants.WVASLOPredictedTTFTSeconds {
					Expect(mf.GetHelp()).To(And(ContainSubstring("Not a bound"), ContainSubstring("34%")))
				}
			}
		})
	})

	It("publishes a percentile in the +Inf bucket as +Inf, not as absent", func() {
		snap := snapshot("m")
		snap.Objectives[0].Observed = math.Inf(1)
		PublishSLO(snap, t0)
		var found bool
		for _, m := range family(constants.WVASLOObservedSeconds) {
			if label(m, constants.LabelObjective) == constants.SLOObjectiveTTFT {
				Expect(math.IsInf(m.GetGauge().GetValue(), 1)).To(BeTrue())
				found = true
			}
		}
		Expect(found).To(BeTrue())
	})

	It("labels attainment with its statistic, and publishes the window's request count", func() {
		snap := snapshot("m")
		snap.Objectives[0].Requests = 300
		snap.Objectives[1].Requests = math.NaN()
		PublishSLO(snap, t0)
		att := family(constants.WVASLOAttainmentRatio)
		Expect(att).To(HaveLen(1))
		Expect(label(att[0], constants.LabelStatistic)).To(Equal("p90"))
		req := family(constants.WVASLOWindowRequests)
		Expect(req).To(HaveLen(1))
		Expect(req[0].GetGauge().GetValue()).To(Equal(300.0))
	})

	It("publishes targets, the judged figures, and one source per variant set to 1", func() {
		PublishSLO(snapshot("m", SLOVariantReading{Variant: "v", Source: constants.SLOSourceOwn,
			Fill: 0.6, PredictedITL: 0.04}), t0)

		Expect(family(constants.WVASLOTargetSeconds)).To(HaveLen(2))
		// The TPOT mean was not judged: no observed series, rather than a zero.
		Expect(family(constants.WVASLOObservedSeconds)).To(HaveLen(1))
		Expect(family(constants.WVASLOAttainmentRatio)).To(HaveLen(1))
		Expect(family(constants.WVASLOFill)[0].GetGauge().GetValue()).To(Equal(0.6))
		sources := family(constants.WVASLOSource)
		Expect(sources).To(HaveLen(len(constants.SLOSources)))
		on := 0
		for _, m := range sources {
			if m.GetGauge().GetValue() == 1 {
				on++
				Expect(label(m, constants.LabelSource)).To(Equal(constants.SLOSourceOwn))
			}
		}
		Expect(on).To(Equal(1))
	})

	It("deletes a variant's series when it is gone from the next snapshot", func() {
		PublishSLO(snapshot("m",
			SLOVariantReading{Variant: "a", Source: constants.SLOSourceOwn, Fill: 0.6, PredictedITL: 0.04},
			SLOVariantReading{Variant: "b", Source: constants.SLOSourceOwn, Fill: 0.5, PredictedITL: 0.04}), t0)
		Expect(family(constants.WVASLOFill)).To(HaveLen(2))

		PublishSLO(snapshot("m",
			SLOVariantReading{Variant: "a", Source: constants.SLOSourceNoLine, Fill: math.NaN(), PredictedITL: math.NaN()}), t0)
		Expect(family(constants.WVASLOFill)).To(BeEmpty())
		Expect(family(constants.WVASLOSource)).To(HaveLen(len(constants.SLOSources)))
	})

	It("sweeps a model silent past the stale horizon, and leaves a live one alone", func() {
		PublishSLO(snapshot("m1"), t0)
		PublishSLO(snapshot("m2"), t0.Add(time.Minute))
		Expect(family(constants.WVASLOTargetSeconds)).To(HaveLen(4))

		SweepSLO(t0.Add(sloStaleAfter + 30*time.Second))
		targets := family(constants.WVASLOTargetSeconds)
		Expect(targets).To(HaveLen(2))
		for _, m := range targets {
			Expect(label(m, constants.LabelModelName)).To(Equal("m2"))
		}
	})

	It("deletes one model's every series at once, and leaves the others", func() {
		v := SLOVariantReading{Variant: "v", Source: constants.SLOSourceOwn, Fill: 0.6, PredictedITL: 0.04}
		PublishSLO(snapshot("gone", v), t0)
		PublishSLO(snapshot("kept", v), t0)
		DeleteSLOModel("ns", "gone")
		for _, name := range []string{constants.WVASLOTargetSeconds, constants.WVASLOObservedSeconds,
			constants.WVASLOAttainmentRatio, constants.WVASLOFill, constants.WVASLOPredictedITLSeconds,
			constants.WVASLOSource} {
			ms := family(name)
			Expect(ms).NotTo(BeEmpty(), name)
			for _, m := range ms {
				Expect(label(m, constants.LabelModelName)).To(Equal("kept"), name)
			}
		}
	})

	// The case a sweep inside PublishSLO missed: the only model publishing
	// stops, so nothing publishes again and only the engine's per-cycle sweep
	// can remove its series.
	It("sweeps the last model even when nothing publishes after it", func() {
		PublishSLO(snapshot("m", SLOVariantReading{Variant: "v", Source: constants.SLOSourceOwn,
			Fill: 0.6, PredictedITL: 0.04}), t0)
		SweepSLO(t0.Add(sloStaleAfter - time.Second))
		Expect(family(constants.WVASLOTargetSeconds)).To(HaveLen(2), "not yet stale")

		SweepSLO(t0.Add(sloStaleAfter + time.Second))
		for _, name := range []string{constants.WVASLOTargetSeconds, constants.WVASLOObservedSeconds,
			constants.WVASLOAttainmentRatio, constants.WVASLOFill, constants.WVASLOPredictedITLSeconds,
			constants.WVASLOSource} {
			Expect(family(name)).To(BeEmpty(), name)
		}
	})
})

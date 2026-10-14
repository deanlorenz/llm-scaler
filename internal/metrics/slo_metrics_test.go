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

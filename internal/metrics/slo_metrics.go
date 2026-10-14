package metrics

import (
	"math"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
)

var (
	sloTarget       *prometheus.GaugeVec
	sloObserved     *prometheus.GaugeVec
	sloAttainment   *prometheus.GaugeVec
	sloFill         *prometheus.GaugeVec
	sloPredictedITL *prometheus.GaugeVec
	sloSource       *prometheus.GaugeVec
	sloRequests     *prometheus.GaugeVec
	sloExcluded     *prometheus.GaugeVec

	sloPrefillSeconds     *prometheus.GaugeVec
	sloEngineQueueSeconds *prometheus.GaugeVec
	sloPrefillUtilization *prometheus.GaugeVec
	sloPrefillCeiling     *prometheus.GaugeVec
)

// registerSLOMetrics creates and registers the SLO analyzer's gauges. Called
// from InitMetrics, after controllerInstance is set.
func registerSLOMetrics(registry prometheus.Registerer) error {
	withInstance := func(labels ...string) []string {
		if controllerInstance != "" {
			labels = append(labels, constants.LabelControllerInstance)
		}
		return labels
	}
	objectiveLabels := withInstance(constants.LabelNamespace, constants.LabelModelName,
		constants.LabelObjective, constants.LabelStatistic)
	gauge := func(name, help string, labels []string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	}
	sloTarget = gauge(constants.WVASLOTargetSeconds,
		"SLO analyzer: the latency target a model's scaling policy declares, in seconds.",
		objectiveLabels)
	sloObserved = gauge(constants.WVASLOObservedSeconds,
		"SLO analyzer: the latency the router observed over its window, in seconds -- the mean for "+
			"TPOT, the configured percentile for TTFT. Absent when too few requests were seen to judge.",
		objectiveLabels)
	sloAttainment = gauge(constants.WVASLOAttainmentRatio,
		"SLO analyzer: the share of completed streaming requests whose TTFT met the target, as the "+
			"router measured it (including its queue). Absent below the sample minimum.",
		objectiveLabels)
	sloRequests = gauge(constants.WVASLOWindowRequests,
		"SLO analyzer: requests the router recorded for the objective over its window. Present "+
			"whenever the router reported anything for the model.",
		objectiveLabels)
	sloFill = gauge(constants.WVASLOFill,
		"SLO analyzer: the KV fill, as a fraction of physical KV, at which a variant's mean "+
			"inter-token latency meets the TPOT target. Absent when the variant could not be priced.",
		withInstance(constants.LabelNamespace, constants.LabelModelName, constants.LabelVariantName,
			constants.LabelObjective))
	sloPredictedITL = gauge(constants.WVASLOPredictedITLSeconds,
		"SLO analyzer: the mean inter-token latency a variant's line predicts at the fill the fleet "+
			"would run at after the scale-up headroom, in seconds.",
		withInstance(constants.LabelNamespace, constants.LabelModelName, constants.LabelVariantName))
	sloSource = gauge(constants.WVASLOSource,
		"SLO analyzer: 1 for the source a variant's SLO capacity came from, 0 for every other.",
		withInstance(constants.LabelNamespace, constants.LabelModelName, constants.LabelVariantName,
			constants.LabelSource))
	// Fresh registry, fresh gauges: forget the series a previous registry held.
	sloPublishedMu.Lock()
	sloPublished = map[string]*sloModelSeries{}
	sloPublishedMu.Unlock()
	sloExcluded = gauge(constants.WVASLOExcluded,
		"SLO analyzer: per objective, 1 for the reason its router window is not judged this cycle (a wake "+
			"from zero, a replica starting), 0 for the others; all 0 while it is judged.",
		withInstance(constants.LabelNamespace, constants.LabelModelName, constants.LabelObjective, constants.LabelReason))
	variantLabels := withInstance(constants.LabelNamespace, constants.LabelModelName, constants.LabelVariantName)
	sloPrefillSeconds = gauge(constants.WVASLOPrefillSeconds,
		"SLO analyzer: the variant's mean prefill time, first scheduled to first token (a preemption's "+
			"re-wait and chunked prefill's turns included), weighted by request rate over its ready replicas. "+
			"Observed, not priced.",
		variantLabels)
	sloEngineQueueSeconds = gauge(constants.WVASLOEngineQueueSeconds,
		"SLO analyzer: the variant's mean engine scheduler-queue wait, the other half of the engine's TTFT.",
		variantLabels)
	sloPrefillUtilization = gauge(constants.WVASLOPrefillUtilization,
		"SLO analyzer: a prefill variant's prefill token rate per ready replica over its learned prefill ceiling.",
		variantLabels)
	sloPrefillCeiling = gauge(constants.WVASLOPrefillCeiling,
		"SLO analyzer: a prefill variant's learned prefill ceiling in tokens per second: the 90th percentile, "+
			"over the last hour, of per-cycle medians of the rate on replicas queued with KV below 0.80; the "+
			"last ceiling is kept once the hour runs dry.",
		variantLabels)
	for _, g := range []*prometheus.GaugeVec{sloTarget, sloObserved, sloAttainment, sloFill, sloPredictedITL,
		sloSource, sloRequests, sloExcluded,
		sloPrefillSeconds, sloEngineQueueSeconds, sloPrefillUtilization, sloPrefillCeiling} {
		if err := registry.Register(g); err != nil {
			return err
		}
	}
	return nil
}

// SLOObjectiveReading is one objective of one model, as PublishSLO takes it.
// A NaN Observed, Attainment or Requests is not published: absence is the
// honest value for "not enough requests to judge" (Observed, Attainment) and
// for "the router reported nothing" (Requests). An infinite Observed -- a
// percentile in the router's +Inf bucket -- is published as +Inf.
type SLOObjectiveReading struct {
	Objective     string
	Statistic     string
	TargetSeconds float64
	Observed      float64
	Attainment    float64
	Requests      float64
	// WindowSeconds is the router window the figures were taken over; zero
	// when the router reported nothing.
	WindowSeconds float64
	// Excluded is why the objective's window is not judged this cycle, one of
	// constants.SLOExcludedReasons, or "" when it is.
	Excluded string
}

// SLOVariantReading is one variant of one model, as PublishSLO takes it. A NaN
// field is not published, and an empty Source publishes no source series: a
// prefill variant observed for TTFT is not priced for TPOT at all.
type SLOVariantReading struct {
	Variant      string
	Source       string
	Fill         float64
	PredictedITL float64

	// The TTFT curve's inputs, published for a variant that prefills.
	PrefillSeconds     float64
	EngineQueueSeconds float64
	PrefillUtilization float64
	PrefillCeiling     float64
}

// SLOModelSnapshot is everything the SLO analyzer publishes for one model in
// one cycle.
type SLOModelSnapshot struct {
	Namespace  string
	ModelName  string
	Objectives []SLOObjectiveReading
	Variants   []SLOVariantReading
}

// sloStaleAfter is how long a model's SLO series outlive the last cycle that
// published them. The analyzer is only called for models whose policy lists
// it, so a model that left -- or whose policy stopped listing the analyzer --
// is noticed only by its silence, which SweepSLO reads every cycle.
const sloStaleAfter = 10 * time.Minute

type sloModelSeries struct {
	series   map[*prometheus.GaugeVec]map[string]prometheus.Labels
	lastSeen time.Time
}

var (
	sloPublished   = map[string]*sloModelSeries{}
	sloPublishedMu sync.Mutex
)

// PublishSLO makes one model's SLO series exactly this snapshot: it sets every
// current series first and only then deletes those the model no longer has, so
// a scrape never lands on a gap.
func PublishSLO(snap SLOModelSnapshot, now time.Time) {
	if sloTarget == nil {
		return
	}
	sloPublishedMu.Lock()
	defer sloPublishedMu.Unlock()

	current := map[*prometheus.GaugeVec]map[string]prometheus.Labels{}
	set := func(g *prometheus.GaugeVec, l prometheus.Labels, v float64) {
		if math.IsNaN(v) {
			return
		}
		g.With(l).Set(v)
		if current[g] == nil {
			current[g] = map[string]prometheus.Labels{}
		}
		current[g][seriesKey(l)] = l
	}
	labels := func(pairs ...string) prometheus.Labels {
		l := prometheus.Labels{
			constants.LabelNamespace: snap.Namespace,
			constants.LabelModelName: snap.ModelName,
		}
		for i := 0; i+1 < len(pairs); i += 2 {
			l[pairs[i]] = pairs[i+1]
		}
		if controllerInstance != "" {
			l[constants.LabelControllerInstance] = controllerInstance
		}
		return l
	}

	for _, o := range snap.Objectives {
		ol := labels(constants.LabelObjective, o.Objective, constants.LabelStatistic, o.Statistic)
		set(sloTarget, ol, o.TargetSeconds)
		set(sloObserved, ol, o.Observed)
		set(sloAttainment, ol, o.Attainment)
		set(sloRequests, ol, o.Requests)
		for _, reason := range constants.SLOExcludedReasons {
			on := 0.0
			if reason == o.Excluded {
				on = 1
			}
			set(sloExcluded, labels(constants.LabelObjective, o.Objective, constants.LabelReason, reason), on)
		}
	}
	for _, v := range snap.Variants {
		set(sloFill, labels(constants.LabelVariantName, v.Variant,
			constants.LabelObjective, constants.SLOObjectiveTPOT), v.Fill)
		set(sloPredictedITL, labels(constants.LabelVariantName, v.Variant), v.PredictedITL)
		vl := labels(constants.LabelVariantName, v.Variant)
		set(sloPrefillSeconds, vl, v.PrefillSeconds)
		set(sloEngineQueueSeconds, vl, v.EngineQueueSeconds)
		set(sloPrefillUtilization, vl, v.PrefillUtilization)
		set(sloPrefillCeiling, vl, v.PrefillCeiling)
		if v.Source == "" {
			continue
		}
		for _, s := range constants.SLOSources {
			on := 0.0
			if s == v.Source {
				on = 1
			}
			set(sloSource, labels(constants.LabelVariantName, v.Variant, constants.LabelSource, s), on)
		}
	}

	key := snap.Namespace + "/" + snap.ModelName
	if prev := sloPublished[key]; prev != nil {
		deleteGone(prev.series, current)
	}
	sloPublished[key] = &sloModelSeries{series: current, lastSeen: now}
}

// SweepSLO deletes every series of a model not published for sloStaleAfter.
// The engine calls it every cycle, whether or not any model runs the analyzer:
// a sweep that ran only inside PublishSLO never ran once the last model
// stopped publishing, and that model's series stayed for ever.
func SweepSLO(now time.Time) {
	if sloTarget == nil {
		return
	}
	sloPublishedMu.Lock()
	defer sloPublishedMu.Unlock()
	for k, m := range sloPublished {
		if now.Sub(m.lastSeen) > sloStaleAfter {
			deleteGone(m.series, nil)
			delete(sloPublished, k)
		}
	}
}

// deleteGone deletes every series in prev that current does not hold.
func deleteGone(prev, current map[*prometheus.GaugeVec]map[string]prometheus.Labels) {
	for g, series := range prev {
		for k, l := range series {
			if _, ok := current[g][k]; !ok {
				g.Delete(l)
			}
		}
	}
}

package steadystate

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	prometheuscollector "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source/prometheus"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/saturation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
	variantregistry "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/registry"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learn"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
	llmdVariantAutoscalingV1alpha1 "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/variant"
)

const lastAdmittedSeries = constants.WVALearnedStateLastAdmittedTimestampSeconds

// reportSpy is a saturation analyzer that hands over fixed reports, once, and
// counts the takes.
type reportSpy struct {
	reports, filed map[string][]learn.Report
	takes          int
}

func (s *reportSpy) Name() string { return "report-spy" }

func (s *reportSpy) Analyze(context.Context, domain.AnalyzerInput) (*domain.AnalyzerResult, error) {
	return nil, nil
}

func (s *reportSpy) TakeLearnerReports() map[string][]learn.Report {
	s.takes++
	out := s.reports
	s.reports = nil
	return out
}

func spyReports(model, variant string) map[string][]learn.Report {
	return map[string][]learn.Report{"ns|" + model: {{Learner: learn.Mu, Namespace: "ns", ModelID: model,
		Variant: variant, Role: domain.RoleDecode, PricingKey: "key", LastAdmitted: time.Unix(1_700_000_000, 0)}}}
}

// EvictStaleHistory files the spy's reports: the sweep runs inside the cycle,
// after the prune, as the Analyze calls that file real ones do.
func (s *reportSpy) EvictStaleHistory() saturation.Evicted {
	s.reports = s.filed
	return saturation.Evicted{}
}

// The analyzer's reports reach the metrics on the cycle that filed them, taken
// once: a spec of the emitter passes with no caller, which is how learner
// reports came to be read by nothing but the replay.
func TestLearnerReportsAreWiredIntoTheCycle(t *testing.T) {
	e, registry := analyzerSeriesEngine(t)
	spy := &reportSpy{filed: spyReports("m", "v")}
	e.Config = config.NewTestConfig()
	e.optimizer = allocation.NewCostAwareOptimizer()
	e.analyzers = testAnalyzers(spy)
	e.capacityStore = capacity.NewStore()

	e.optimizeV2(context.Background(), map[string][]llmdVariantAutoscalingV1alpha1.VariantAutoscaling{
		"ns/m": {{
			ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "ns"},
			Spec:       llmdVariantAutoscalingV1alpha1.VariantAutoscalingSpec{ModelID: "m"},
		}},
	}, nil)

	assert.Equal(t, 1, spy.takes, "one take a cycle")
	assert.Equal(t, []string{learn.Mu}, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelLearner))
}

// publishedSpy is an engine holding one published series of model m.
func publishedSpy(t *testing.T, model, variant string, e *Engine) {
	t.Helper()
	e.analyzers = testAnalyzers(&reportSpy{reports: spyReports(model, variant)})
	e.publishLearnerReports()
}

// parkRig is an engine over a fake cluster, driven through optimize: each
// variant a ScaledObject KEDA has called about, with a Deployment behind it.
type parkRig struct {
	t      *testing.T
	e      *Engine
	c      client.Client
	series *prometheus.Registry
}

func newParkRig(t *testing.T) *parkRig {
	t.Helper()
	series := prometheus.NewRegistry()
	require.NoError(t, metrics.InitMetrics(series))
	c := fake.NewClientBuilder().Build()
	sources := source.NewSourceRegistry()
	require.NoError(t, sources.Register("prometheus",
		prometheuscollector.NewPrometheusSource(context.Background(), nil, prometheuscollector.DefaultPrometheusSourceConfig())))
	e := NewEngine(c, c, c.Scheme(), nil, sources, config.NewTestConfig(), allocation.NewNoOpLimiter("test"))
	e.Variants = variantregistry.New(0)
	return &parkRig{t: t, e: e, c: c, series: series}
}

// variant adds a variant at replicas, with a learner series published for it.
func (r *parkRig) variant(model, name string, replicas int32) {
	r.t.Helper()
	r.e.Variants.Observe("ns", name, map[string]string{variantregistry.ModelIDKey: model})
	r.e.Variants.SetTarget("ns", name, variantregistry.Target{APIVersion: "apps/v1", Kind: "Deployment", Name: name})
	require.NoError(r.t, r.c.Create(context.Background(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}))
	r.e.metricsEmitter.RecordLearnerReports(spyReports(model, name)["ns|"+model])
}

// cycle runs one optimize. A model with replicas has nothing to collect from
// here, so its cycle may fail: the series do not depend on it passing.
func (r *parkRig) cycle() []string {
	r.t.Helper()
	_ = r.e.optimize(context.Background())
	return seriesLabels(r.t, r.series, lastAdmittedSeries, constants.LabelVariantName)
}

// A real park: every model at zero replicas, the path that returns before any
// model is analyzed and evicts every analyzer series. The series stay, however
// many cycles it lasts, or an alert on their age resolves instead of firing.
// A model whose ScaledObject goes loses its series, and the last one too.
func TestLearnerSeries_AllModelsParked(t *testing.T) {
	r := newParkRig(t)
	r.variant("a", "a-wva", 0)
	r.variant("b", "b-wva", 0)
	for range 3 {
		assert.ElementsMatch(t, []string{"a-wva", "b-wva"}, r.cycle())
	}
	r.e.Variants.Forget("ns", "a-wva")
	assert.Equal(t, []string{"b-wva"}, r.cycle())
	r.e.Variants.Forget("ns", "b-wva")
	assert.Empty(t, r.cycle())
}

// One model parked among others: it is in no model group, so the prune of the
// active set takes its analyzer series and not these. A deleted model beside
// them does lose its own.
func TestLearnerSeries_OneModelOfSeveralParked(t *testing.T) {
	r := newParkRig(t)
	r.variant("running", "running-wva", 1)
	r.variant("parked", "parked-wva", 0)
	r.variant("deleted", "deleted-wva", 1)
	for range 2 {
		assert.ElementsMatch(t, []string{"running-wva", "parked-wva", "deleted-wva"}, r.cycle())
	}
	r.e.Variants.Forget("ns", "deleted-wva")
	assert.ElementsMatch(t, []string{"running-wva", "parked-wva"}, r.cycle())
}

// A variant parked beside a running sibling of its model keeps its series; a
// variant deleted from a model that stays loses it.
func TestLearnerSeries_OneVariantOfAModel(t *testing.T) {
	r := newParkRig(t)
	r.variant("m", "running-wva", 1)
	r.variant("m", "parked-wva", 0)
	r.variant("m", "deleted-wva", 0)
	r.cycle()
	r.e.Variants.Forget("ns", "deleted-wva")
	assert.ElementsMatch(t, []string{"running-wva", "parked-wva"}, r.cycle())
}

// The analyzer series follow what analyzers report, and these do not: a
// variant no analyzer reports may only be parked.
func TestLearnerSeries_StayWhenNoAnalyzerReportsTheVariant(t *testing.T) {
	e, registry := analyzerSeriesEngine(t)
	e.recordAnalyzerMetrics("ns", "m", []allocation.NamedAnalyzerResult{namedResult("saturation", nil, 1, "v", "w")})
	publishedSpy(t, "m", "v", e)
	publishedSpy(t, "m", "w", e)
	e.recordAnalyzerMetrics("ns", "m", []allocation.NamedAnalyzerResult{namedResult("saturation", nil, 1, "w")})
	assert.Len(t, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelVariantName), 2)
	e.pruneAnalyzerSeries(map[string]bool{utils.GetNamespacedKey("ns", "other"): true})
	e.evictAllAnalyzerSeries()
	assert.Len(t, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelVariantName), 2)
}

// A cycle with no replica rows keeps them: the borrow and k2-source series
// describe this cycle's pricing and go, these describe the evidence.
func TestLearnerSeriesStayThroughACycleWithNoRows(t *testing.T) {
	e, registry := analyzerSeriesEngine(t)
	publishedSpy(t, "m", "v", e)
	e.zeroObservedReplicas("ns", "m")
	assert.Len(t, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelVariantName), 1)
}

// An analyzer that reports nothing, and one that cannot, publish nothing.
func TestPublishLearnerReportsWithNoReports(t *testing.T) {
	e, registry := analyzerSeriesEngine(t)
	e.publishLearnerReports() // no analyzer registered
	e.analyzers = testAnalyzers(&reportSpy{})
	e.publishLearnerReports()
	assert.Empty(t, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelVariantName))
}

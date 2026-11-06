package steadystate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	base   *clocktesting.FakePassiveClock
	series *prometheus.Registry
	// forbid makes every Deployment read Forbidden, and reads counts them.
	forbid bool
	reads  int
}

func newParkRig(t *testing.T) *parkRig {
	t.Helper()
	series := prometheus.NewRegistry()
	require.NoError(t, metrics.InitMetrics(series))
	// What other tests published is process-wide bookkeeping, not this rig's.
	metrics.NewMetricsEmitter().RetainLearnerSeries(func(string, string, string) bool { return false })
	r := &parkRig{t: t, series: series, base: clocktesting.NewFakePassiveClock(time.Unix(1_700_000_000, 0))}
	r.c = fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				r.reads++
				if r.forbid {
					return apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, key.Name, errors.New("no"))
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
	sources := source.NewSourceRegistry()
	require.NoError(t, sources.Register("prometheus",
		prometheuscollector.NewPrometheusSource(context.Background(), nil, prometheuscollector.DefaultPrometheusSourceConfig())))
	r.e = newEngine(r.c, r.c, r.c.Scheme(), nil, sources, config.NewTestConfig(), allocation.NewNoOpLimiter("test"), r.base)
	r.e.Variants = variantregistry.New(0)
	return r
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
	r.e.metricsEmitter.RecordLearnerReports(spyReports(model, name)["ns|"+model], r.e.learnerTarget)
}

// expire is KEDA no longer calling about a variant, its workload still there.
func (r *parkRig) expire(name string) {
	r.t.Helper()
	r.e.Variants.Forget("ns", name)
}

// remove deletes a variant: its ScaledObject and its workload both go.
func (r *parkRig) remove(name string) {
	r.t.Helper()
	r.expire(name)
	require.NoError(r.t, r.c.Delete(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"}}))
}

// after moves the clock on.
func (r *parkRig) after(d time.Duration) *parkRig {
	r.base.SetTime(r.base.Now().Add(d))
	return r
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
	r.remove("a-wva")
	assert.Equal(t, []string{"b-wva"}, r.after(learnerTargetCheckInterval).cycle())
	r.remove("b-wva")
	assert.Empty(t, r.after(learnerTargetCheckInterval).cycle())
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
	r.remove("deleted-wva")
	assert.ElementsMatch(t, []string{"running-wva", "parked-wva"}, r.after(learnerTargetCheckInterval).cycle())
}

// A variant parked beside a running sibling of its model keeps its series; a
// variant deleted from a model that stays loses it.
func TestLearnerSeries_OneVariantOfAModel(t *testing.T) {
	r := newParkRig(t)
	r.variant("m", "running-wva", 1)
	r.variant("m", "parked-wva", 0)
	r.variant("m", "deleted-wva", 0)
	r.cycle()
	r.remove("deleted-wva")
	assert.ElementsMatch(t, []string{"running-wva", "parked-wva"}, r.after(learnerTargetCheckInterval).cycle())
}

// A KEDA outage past the registry's TTL, through the registry itself: every
// entry expires, every target stands, and the series stay, or an alert on
// their age resolves in the middle of the outage.
func TestLearnerSeries_RegistryExpiryWithTheTargetPresentKeeps(t *testing.T) {
	r := newParkRig(t)
	const ttl = 300 * time.Millisecond
	r.e.Variants = variantregistry.New(ttl)
	r.variant("a", "a-wva", 0)
	r.variant("b", "b-wva", 1)
	require.Eventually(t, func() bool { return r.e.Variants.Len() == 0 || len(r.e.Variants.Snapshot()) == 0 },
		10*ttl, ttl/10, "premise: the entries expire")
	for range 3 {
		assert.ElementsMatch(t, []string{"a-wva", "b-wva"}, r.after(learnerTargetCheckInterval).cycle())
	}
}

// A trigger that stops parsing drops the variant from the known set, and only
// from it: the entry and the workload stand.
func TestLearnerSeries_AParseFailureKeeps(t *testing.T) {
	r := newParkRig(t)
	r.variant("m", "m-wva", 0)
	r.e.Variants.Observe("ns", "m-wva", map[string]string{"typo": "m"})
	require.Empty(t, utils.KnownVariantAutoscalings(context.Background(), r.e.Variants), "premise: the variant is not known")
	assert.Equal(t, []string{"m-wva"}, r.after(learnerTargetCheckInterval).cycle())
}

// NotFound is an answer, and the first variant to go missing is read at once:
// a cycle that read nothing does not start the interval.
func TestLearnerSeries_NotFoundDeletes(t *testing.T) {
	r := newParkRig(t)
	r.variant("m", "m-wva", 0)
	r.cycle()
	r.remove("m-wva")
	assert.Empty(t, r.cycle())
}

// A scale target being deleted is gone already.
func TestLearnerSeries_ADeletionTimestampDeletes(t *testing.T) {
	r := newParkRig(t)
	r.variant("m", "m-wva", 0)
	var d appsv1.Deployment
	key := client.ObjectKey{Namespace: "ns", Name: "m-wva"}
	require.NoError(t, r.c.Get(context.Background(), key, &d))
	d.Finalizers = []string{"test/hold"}
	require.NoError(t, r.c.Update(context.Background(), &d))
	require.NoError(t, r.c.Delete(context.Background(), &d))
	r.expire("m-wva")
	assert.Empty(t, r.cycle())
}

// Forbidden is not absent: the read could not ask. Nor is a kind the check
// does not read.
func TestLearnerSeries_ForbiddenKeeps(t *testing.T) {
	r := newParkRig(t)
	r.variant("m", "m-wva", 0)
	r.forbid = true
	r.expire("m-wva")
	for range 3 {
		assert.Equal(t, []string{"m-wva"}, r.after(learnerTargetCheckInterval).cycle())
	}
	assert.Equal(t, 3, r.reads, "premise: the reads were made")
	assert.False(t, r.e.scaleTargetGone(context.Background(), metrics.LearnerVariant{Namespace: "ns",
		Target: metrics.LearnerTarget{Kind: "StatefulSet", Name: "m-wva"}}))
}

// The cap ends a series no read can: the target stands, the variant has been
// missing for a day. Just under it the series stays, and a variant that comes
// back starts the count again.
func TestLearnerSeries_TheCapDeletes(t *testing.T) {
	r := newParkRig(t)
	r.variant("m", "m-wva", 0)
	r.expire("m-wva")
	require.Equal(t, []string{"m-wva"}, r.cycle())
	r.after(learnerSeriesMissingMax - time.Second)
	require.Equal(t, []string{"m-wva"}, r.cycle())
	r.e.Variants.Observe("ns", "m-wva", map[string]string{variantregistry.ModelIDKey: "m"})
	r.e.Variants.SetTarget("ns", "m-wva", variantregistry.Target{APIVersion: "apps/v1", Kind: "Deployment", Name: "m-wva"})
	require.Equal(t, []string{"m-wva"}, r.after(time.Second).cycle())
	r.expire("m-wva")
	require.Equal(t, []string{"m-wva"}, r.after(learnerSeriesMissingMax-time.Second).cycle(), "the count starts again")
	require.Equal(t, []string{"m-wva"}, r.after(learnerSeriesMissingMax-time.Second).cycle())
	assert.Empty(t, r.after(time.Second).cycle())
	assert.Empty(t, r.e.learnerMissingSince, "and nothing is kept of it")
}

// A variant with no target on record has nothing to read: it waits for the cap.
func TestLearnerSeries_NoRecordedTargetWaitsForTheCap(t *testing.T) {
	r := newParkRig(t)
	r.e.metricsEmitter.RecordLearnerReports(spyReports("m", "m-wva")["ns|m"], nil)
	r.reads = 0
	require.Equal(t, []string{"m-wva"}, r.cycle())
	assert.Zero(t, r.reads)
	assert.Empty(t, r.after(learnerSeriesMissingMax).cycle())
}

// The reads are bounded whatever the client is: one per missing variant per
// interval, however many cycles and however many learners publish.
func TestLearnerSeries_ReadsAreOnePerVariantPerInterval(t *testing.T) {
	r := newParkRig(t)
	r.variant("m", "m-wva", 0)
	r.e.metricsEmitter.RecordLearnerReports([]learn.Report{{Learner: learn.K2, Namespace: "ns", ModelID: "m", Variant: "m-wva",
		Role: domain.RoleDecode, PricingKey: "k", LastAdmitted: time.Unix(1_700_000_000, 0)}}, r.e.learnerTarget)
	r.variant("n", "n-wva", 0)
	r.expire("m-wva")
	r.expire("n-wva")
	r.reads = 0
	for range 5 {
		r.after(time.Second).cycle()
	}
	assert.Equal(t, 2, r.reads, "two variants, read once in the interval")
	r.after(learnerTargetCheckInterval).cycle()
	assert.Equal(t, 4, r.reads, "and once in the next")
}

// A variant still known costs no read.
func TestLearnerSeries_AKnownVariantCostsNoRead(t *testing.T) {
	r := newParkRig(t)
	r.variant("m", "m-wva", 0)
	r.reads = 0
	r.e.retainLearnerSeries(context.Background())
	assert.Zero(t, r.reads)
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

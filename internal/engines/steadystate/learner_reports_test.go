package steadystate

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learn"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
	llmdVariantAutoscalingV1alpha1 "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/variant"
)

const lastAdmittedSeries = constants.WVALearnedStateLastAdmittedTimestampSeconds

// reportSpy is a saturation analyzer that hands over fixed reports, once, and
// counts the takes.
type reportSpy struct {
	reports map[string][]learn.Report
	takes   int
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

// The analyzer's reports reach the metrics on a cycle, taken once: a spec of
// the emitter passes with no caller, which is how learner reports came to be
// read by nothing but the replay.
func TestLearnerReportsAreWiredIntoTheCycle(t *testing.T) {
	e, registry := analyzerSeriesEngine(t)
	spy := &reportSpy{reports: spyReports("m", "v")}
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

// A model no longer reconciled loses its series, through either way the
// engine deletes the others, and a model still reconciled keeps them.
func TestLearnerSeriesGoWithTheModel(t *testing.T) {
	e, registry := analyzerSeriesEngine(t)
	publishedSpy(t, "stays", "v", e)
	publishedSpy(t, "gone", "v", e)
	require.Len(t, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelModelName), 2)

	e.pruneAnalyzerSeries(map[string]bool{utils.GetNamespacedKey("ns", "stays"): true})
	assert.Equal(t, []string{"stays"}, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelModelName))

	e.evictAllAnalyzerSeries()
	assert.Empty(t, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelModelName))
	assert.Empty(t, e.learnerSeries, "nothing left to retain")
}

// A parked model keeps them: the borrow and k2-source series describe this
// cycle's pricing and go, these describe the evidence, and its age is the
// question while nothing reports.
func TestLearnerSeriesStayThroughAPark(t *testing.T) {
	e, registry := analyzerSeriesEngine(t)
	publishedSpy(t, "m", "v", e)
	e.zeroObservedReplicas("ns", "m")
	assert.Len(t, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelVariantName), 1)
}

// A variant that leaves a model that stays takes its series with it.
func TestLearnerSeriesGoWithTheVariant(t *testing.T) {
	e, registry := analyzerSeriesEngine(t)
	e.recordAnalyzerMetrics("ns", "m", []allocation.NamedAnalyzerResult{namedResult("saturation", nil, 1, "v", "w")})
	publishedSpy(t, "m", "v", e)
	publishedSpy(t, "m", "w", e)
	require.Len(t, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelVariantName), 2)

	e.recordAnalyzerMetrics("ns", "m", []allocation.NamedAnalyzerResult{namedResult("saturation", nil, 1, "w")})
	assert.Equal(t, []string{"w"}, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelVariantName))
}

// An analyzer that reports nothing, and one that cannot, publish nothing.
func TestPublishLearnerReportsWithNoReports(t *testing.T) {
	e, registry := analyzerSeriesEngine(t)
	e.publishLearnerReports() // no analyzer registered
	e.analyzers = testAnalyzers(&reportSpy{})
	e.publishLearnerReports()
	assert.Empty(t, seriesLabels(t, registry, lastAdmittedSeries, constants.LabelVariantName))
	assert.Empty(t, e.learnerSeries)
}

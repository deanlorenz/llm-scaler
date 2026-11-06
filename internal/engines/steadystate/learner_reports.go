package steadystate

import (
	"maps"
	"slices"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learn"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
)

// The learners' reports reach the metrics here (analyzer-learner.md, 6.2 and
// 6.3): the analyzer logs, the engine publishes.

// learnerReporter is the part of the saturation analyzer that hands over the
// learners' reports, by namespace|model.
type learnerReporter interface {
	TakeLearnerReports() map[string][]learn.Report
}

// learnerSeriesModel names a model whose learner reports were published.
type learnerSeriesModel struct {
	namespace, modelID string
}

// publishLearnerReports publishes what the cycle's Analyze calls reported, on
// one call per cycle, so every model's reports are of this cycle and the
// models that report are tracked for retainLearnerSeries.
func (e *Engine) publishLearnerReports() {
	reporter := e.analyzers.saturationCaps().reporter
	if reporter == nil {
		return
	}
	reports := reporter.TakeLearnerReports()
	for _, model := range slices.Sorted(maps.Keys(reports)) {
		rs := reports[model]
		if len(rs) == 0 {
			continue
		}
		e.metricsEmitter.RecordLearnerReports(rs)
		if e.learnerSeries == nil {
			e.learnerSeries = make(map[string]learnerSeriesModel)
		}
		e.learnerSeries[utils.GetNamespacedKey(rs[0].Namespace, rs[0].ModelID)] = learnerSeriesModel{
			namespace: rs[0].Namespace, modelID: rs[0].ModelID,
		}
	}
}

// retainLearnerSeries deletes the learner series of every model whose
// namespaced key is not in active; nil deletes them all. A model still
// reconciled keeps them through a park or a failed cycle: they say how old
// its evidence is, which is the question then.
func (e *Engine) retainLearnerSeries(active map[string]bool) {
	for key, m := range e.learnerSeries {
		if !active[key] {
			e.metricsEmitter.DeleteLearnerSeriesForModel(m.namespace, m.modelID)
			delete(e.learnerSeries, key)
		}
	}
}

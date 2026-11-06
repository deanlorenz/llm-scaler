package steadystate

import (
	"context"
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

// publishLearnerReports publishes what the cycle's Analyze calls reported, at
// the end of the cycle.
func (e *Engine) publishLearnerReports() {
	reporter := e.analyzers.saturationCaps().reporter
	if reporter == nil {
		return
	}
	reports := reporter.TakeLearnerReports()
	for _, model := range slices.Sorted(maps.Keys(reports)) {
		e.metricsEmitter.RecordLearnerReports(reports[model])
	}
}

// retainLearnerSeries deletes the learner series of every variant the
// controller no longer knows, once a cycle. Known, not active: a variant at
// zero replicas is in no model group, and how old its evidence is grows more
// interesting the longer it stays parked.
func (e *Engine) retainLearnerSeries(ctx context.Context) {
	type variant struct{ namespace, modelID, name string }
	known := map[variant]bool{}
	for _, va := range utils.KnownVariantAutoscalings(ctx, e.Variants) {
		known[variant{va.Namespace, va.Spec.ModelID, va.Name}] = true
	}
	e.metricsEmitter.RetainLearnerSeries(func(namespace, modelID, name string) bool {
		return known[variant{namespace, modelID, name}]
	})
}

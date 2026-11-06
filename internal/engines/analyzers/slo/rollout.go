package slo

import "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"

// SwitchITLRolloutGuard is the replay switch for the line's rollout guard
// (issue #481). On, while the variant's scale target rolls out
// (capacity.Record.InRollout) or its replicas report more than one revision
// (domain.RevisionsMixed), the line is offered no rows and fits from what it
// holds, as the saturation analyzer's: a reading may be an old pod's, and it
// would be judged against the new engine's sequence cap. A change of
// arguments forgets the line either way (linesUnderArgs). Off, as before,
// every row is offered.
const SwitchITLRolloutGuard = "slo-itl-rollout-guard"

// lineRows is what the variant's line is offered this cycle: the model's
// rows, or none mid-rollout (SwitchITLRolloutGuard).
func (a *Analyzer) lineRows(input domain.AnalyzerInput, fleet fleetReading, variant string) []domain.ReplicaMetrics {
	if !a.itlRolloutGuard {
		return input.ReplicaMetrics
	}
	if fleet.mixed[variant] {
		return nil
	}
	if a.store != nil && a.store.Get(input.Namespace, input.ModelID, variant).InRollout() {
		return nil
	}
	return input.ReplicaMetrics
}

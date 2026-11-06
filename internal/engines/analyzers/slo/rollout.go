package slo

import "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"

// SwitchITLRolloutGuard is the replay switch for the line's rollout guard
// (issue #481). On, while the variant's scale target rolls out
// (capacity.Record.InRollout) or its replicas report more than one revision
// (domain.RevisionsMixed), the line is offered no rows and fits from what it
// holds, as the saturation analyzer's: a reading may be an old pod's, and it
// would be judged against the new engine's sequence cap. A change of
// arguments forgets the line either way (linesUnderArgs). The cycle after a
// rollout resets the line's tier latch (itl.Admission.Rollout). Off, as
// before, every row is offered, and the latch stands.
const SwitchITLRolloutGuard = "slo-itl-rollout-guard"

// rollingOut reports that the line takes no rows this cycle
// (SwitchITLRolloutGuard).
func (a *Analyzer) rollingOut(input domain.AnalyzerInput, fleet fleetReading, variant string) bool {
	if !a.itlRolloutGuard {
		return false
	}
	return fleet.mixed[variant] ||
		(a.store != nil && a.store.Get(input.Namespace, input.ModelID, variant).InRollout())
}

package steadystate

// Handing borrowed capacity back to a waiting lender Kueue does not reclaim
// for, on the engine side (PR-C, kueue.releaseToNonReclaimingLenders): the
// governor decides (allocation/borrow_lender_release.go); this file tells it
// how long a release takes to land, from the cluster's own configuration.

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils/scaletarget"
)

// releaseLatency is how long a lowered target of a borrowing model takes to
// free its GPUs, the slowest over the variants of the namespaces that count
// borrowing: their ScaledObject's scale-down window and polling interval and
// their pods' termination grace, at the cluster's defaults where unset
// (allocation.ReleaseBound). A release to a lender lands no sooner.
func releaseLatency(e *Engine, constraints []*allocation.ResourceConstraints,
	requests []allocation.ModelScalingRequest, scaleTargets map[string]scaletarget.ScaleTargetAccessor) time.Duration {
	borrowing := map[string]bool{}
	for _, c := range constraints {
		if c == nil {
			continue
		}
		for ns := range c.NamespaceBorrow {
			borrowing[ns] = true
		}
	}
	slowest := time.Duration(0)
	for _, req := range requests {
		if !borrowing[req.Namespace] {
			continue
		}
		for _, st := range req.VariantStates {
			var window, polling, grace *time.Duration
			acc := scaleTargets[req.Namespace+"/"+st.VariantName]
			if acc != nil {
				for _, tpl := range []*corev1.PodTemplateSpec{acc.GetLeaderPodTemplateSpec(), acc.GetWorkerPodTemplateSpec()} {
					if tpl != nil && tpl.Spec.TerminationGracePeriodSeconds != nil {
						longer(&grace, time.Duration(*tpl.Spec.TerminationGracePeriodSeconds)*time.Second)
					}
				}
				if e.Variants != nil {
					entry, ok := e.Variants.FindByScaleTarget(req.Namespace, constants.DeploymentKind, acc.GetName())
					if !ok {
						entry, ok = e.Variants.FindByScaleTarget(req.Namespace, constants.LeaderWorkerSetKind, acc.GetName())
					}
					if ok {
						if w := entry.Target.ScaleDownWindowSeconds; w != nil {
							longer(&window, time.Duration(*w)*time.Second)
						}
						if p := entry.Target.PollingIntervalSeconds; p != nil {
							longer(&polling, time.Duration(*p)*time.Second)
						}
					}
				}
			}
			slowest = max(slowest, allocation.ReleaseBound(window, polling, grace))
		}
	}
	return slowest
}

// longer sets *cur to d when unset or shorter.
func longer(cur **time.Duration, d time.Duration) {
	if *cur == nil || d > **cur {
		v := d
		*cur = &v
	}
}

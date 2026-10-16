package variantmeta

import (
	"reflect"

	corev1 "k8s.io/api/core/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/kueue"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils/scaletarget"
)

// podPlacements is the node placement of each distinct pod template of one
// replica, leader first (domain.VariantMetadata.Placements): a LeaderWorkerSet
// whose worker template differs from its leader's carries both. Nil when no
// template constrains its nodes.
func podPlacements(acc scaletarget.ScaleTargetAccessor) []domain.PodPlacement {
	if acc == nil {
		return nil
	}
	var out []domain.PodPlacement
	leader, worker := acc.GetLeaderPodTemplateSpec(), acc.GetWorkerPodTemplateSpec()
	if leader != nil {
		if p := domain.PlacementOf(&leader.Spec); p != nil {
			out = append(out, *p)
		}
	}
	if worker != nil && worker != leader {
		if p := domain.PlacementOf(&worker.Spec); p != nil && !containsPlacement(out, *p) {
			out = append(out, *p)
		}
	}
	return out
}

// PodPlacements is the node placement of each distinct pod template of one
// replica of acc, leader first; nil when no template constrains its nodes.
// The scale-from-zero engine fits a wake with it (WN3).
func PodPlacements(acc scaletarget.ScaleTargetAccessor) []domain.PodPlacement {
	return podPlacements(acc)
}

// replicaRequests is what one replica's pods request in all of each resource,
// in milli-units (domain.VariantMetadata.ReplicaRequests): the leader pod once
// and, for a LeaderWorkerSet, each worker of the group. A resource no
// container names is absent. Nil when there is no template.
func replicaRequests(acc scaletarget.ScaleTargetAccessor) map[string]int64 {
	if acc == nil {
		return nil
	}
	leader, worker := acc.GetLeaderPodTemplateSpec(), acc.GetWorkerPodTemplateSpec()
	if leader == nil && worker == nil {
		return nil
	}
	out := map[string]int64{}
	add := func(t *corev1.PodTemplateSpec, n int64) {
		if t == nil {
			return
		}
		for name, milli := range kueue.PodRequestsMilli(&t.Spec) {
			out[name] += n * milli
		}
	}
	size := max(int64(acc.GetGroupSize()), 1)
	if leader != nil {
		add(leader, 1)
		add(worker, size-1)
	} else {
		add(worker, size)
	}
	return out
}

// priorityClass is the Kueue priority class of one replica's Workload, read
// from its leader template, or its worker template when it has no leader
// (domain.VariantMetadata.PriorityClass). Empty when there is no template.
func priorityClass(acc scaletarget.ScaleTargetAccessor) string {
	if acc == nil {
		return ""
	}
	if t := acc.GetLeaderPodTemplateSpec(); t != nil {
		return kueue.TemplatePriorityClass(t)
	}
	return kueue.TemplatePriorityClass(acc.GetWorkerPodTemplateSpec())
}

func containsPlacement(ps []domain.PodPlacement, p domain.PodPlacement) bool {
	for _, q := range ps {
		if reflect.DeepEqual(q, p) {
			return true
		}
	}
	return false
}

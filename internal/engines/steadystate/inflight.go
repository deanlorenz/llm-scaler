package steadystate

import (
	"context"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/variantmeta"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils/scaletarget"
)

// createdReplicas is each variant's replicas whose pods exist, keyed
// "namespace/variant" (allocation.InFlightLedger.ReleaseUncreated).
func createdReplicas(requests []allocation.ModelScalingRequest) map[string]int {
	out := map[string]int{}
	for _, req := range requests {
		for _, st := range req.VariantStates {
			out[utils.GetNamespacedKey(req.Namespace, st.VariantName)] = st.CurrentReplicas
		}
	}
	return out
}

// admitInFlight records this cycle's targets in the in-flight ledger
// (allocation.DefaultInFlight), when the cycle was fitted to nodes: each
// variant is to have its target's replicas holding a node, and until its pods
// do, the holes they were ordered into are held from every later decision of
// either engine. A variant at or below what it has placed ends its entry.
//
// The models the utilization share plans are left out: a fill's hole is held
// as a promise (allocation.ReservePromised) until its receiver's pods land.
//
// A variant with borrowed growth in flight (allocation.BorrowInFlight) has its
// holes held on its Kueue flavor's nodes, where the placer fitted them, with
// its pod templates' placement (borrowedFlavors). The ledger holds the node;
// the shared pool's GPUs are held apart, by the borrowed draw until the pods
// exist and by the gated-pod deduction while Kueue's admission gate holds them
// (prepareBorrowing): one is a hole on a node, the other GPUs of a cohort's
// pool, so neither is counted twice.
func (e *Engine) admitInFlight(requests []allocation.ModelScalingRequest, decisions []domain.VariantDecision,
	planned map[string]bool, scaleTargets map[string]scaletarget.ScaleTargetAccessor, now time.Time) {
	states := map[string]domain.VariantReplicaState{}
	flavors := e.borrowedFlavors()
	for _, req := range requests {
		if planned[utils.GetNamespacedKey(req.Namespace, req.ModelID)] {
			continue
		}
		for _, st := range req.VariantStates {
			states[utils.GetNamespacedKey(req.Namespace, st.VariantName)] = st
		}
	}
	for i := range decisions {
		d := &decisions[i]
		key := utils.GetNamespacedKey(d.Namespace, d.VariantName)
		st, ok := states[key]
		if !ok || !constants.IsAcceleratorResolved(d.AcceleratorName) {
			continue
		}
		// What holds a node now: the filled replicas when the pods were
		// listed, else every current replica (the growth alone is then held).
		placedNow := st.CurrentReplicas
		if st.HeldKnown {
			placedNow = st.FilledReplicas
		}
		var placed func() (int, bool)
		if acc, ns := scaleTargets[key], d.Namespace; acc != nil && e.client != nil {
			c := e.client
			placed = func() (int, bool) { return variantmeta.PlacedReplicas(context.Background(), c, ns, acc) }
		}
		shape := allocation.ReplicaShape{Variant: key, Accelerator: d.AcceleratorName,
			PodGPUs: st.PodGPUs, GPUs: max(st.GPUsPerReplica, 1), Tolerations: st.Tolerations, DomainKey: st.TopologyKey}
		if f, ok := flavors[key]; ok {
			shape.Placements, shape.Flavor = st.Placements, f
		}
		allocation.DefaultInFlight.Admit(key, shape, d.TargetReplicas, placedNow, placed, now)
	}
}

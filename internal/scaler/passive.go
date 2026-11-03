package scaler

import (
	"context"

	pb "github.com/kedacore/keda/v2/pkg/scalers/externalscaler"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/passive"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/registry"
)

// PassiveModels reports whether a model is passive (passive.Resolver.Model).
type PassiveModels interface {
	Model(namespace, modelID string) (bool, string)
}

// WithPassive makes the handler answer passive models without moving them
// (docs/reference/scaling-policy.md, "passive"). Nil leaves every model
// active. Returns h for chaining.
func (h *Handler) WithPassive(p PassiveModels) *Handler {
	h.passive = p
	return h
}

// passiveMode is the mode of the variant ref names, with scale target t.
func (h *Handler) passiveMode(ref *pb.ScaledObjectRef, t registry.Target) passive.Mode {
	if h.passive == nil {
		return passive.Active
	}
	p, _ := h.passive.Model(ref.GetNamespace(), ref.GetScalerMetadata()[registry.ModelIDKey])
	return passive.ModeOf(p, t)
}

// modelPassive reports whether the model ref names is passive, before its
// target is known: the standby's cheap first question, so an active model
// costs it no ScaledObject read.
func (h *Handler) modelPassive(ref *pb.ScaledObjectRef) bool {
	if h.passive == nil {
		return false
	}
	p, _ := h.passive.Model(ref.GetNamespace(), ref.GetScalerMetadata()[registry.ModelIDKey])
	return p
}

// passiveAnswer is what GetMetrics answers for a passive variant, whatever the
// decision store holds.
//
//   - Observe (other triggers beside ours): 0. Under AverageValue with target 1
//     the HPA proposes ceil(0/1) = 0 for our metric and takes the highest
//     proposal over its metrics, so ours never wins. Never an error: the HPA
//     refuses every scale-down while any metric fails
//     (computeReplicasForMetrics), and KEDA counts an error toward its
//     fallback.
//   - Hold (our trigger alone): the target's live spec.replicas -- what the HPA
//     itself takes as the current count -- clamped to the ScaledObject's
//     bounds. A desired count equal to the current one returns the current
//     one through any stabilization window, so the HPA holds, and a count an
//     operator sets by hand is the count held.
func (h *Handler) passiveAnswer(ctx context.Context, ref *pb.ScaledObjectRef, t registry.Target, mode passive.Mode) (int64, error) {
	if mode.Answers0() {
		return 0, nil
	}
	n, err := h.currentReplicas(ctx, ref, t)
	if err != nil {
		return 0, err
	}
	return passive.Clamp(n, t), nil
}

// passiveActive is the activation a passive variant reports: never in
// observe, so KEDA's any-trigger activation rests on the other triggers; in
// hold, whether the held count is above zero, so a target at zero stays there
// and a running one is never deactivated. An unreadable count is an error, not
// active: active would wake a target parked at zero on the strength of a
// failed read, and inactive would park a running one. KEDA counts the error
// as a failed trigger read and changes nothing (see errNotLeader).
func (h *Handler) passiveActive(ctx context.Context, ref *pb.ScaledObjectRef, t registry.Target, mode passive.Mode) (bool, error) {
	if mode.Answers0() {
		return false, nil
	}
	n, err := h.passiveAnswer(ctx, ref, t, mode)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// standbyObserve reports whether a replica that does not hold the lease can
// answer ref anyway: an observe answer needs no decision and no replica
// count, so a standby gives the same one the leader would, and the lease
// hand-off stops failing a metric the HPA would otherwise hold every
// scale-down for. Only a passive model costs the ScaledObject read.
func (h *Handler) standbyObserve(ctx context.Context, ref *pb.ScaledObjectRef) bool {
	if !h.modelPassive(ref) {
		return false
	}
	t, err := h.resolveTarget(ctx, ref)
	if err != nil {
		return false
	}
	return h.passiveMode(ref, t).Answers0()
}

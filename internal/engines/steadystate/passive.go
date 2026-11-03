package steadystate

import (
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/passive"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/registry"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
	llmdVariantAutoscalingV1alpha1 "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/variant"
)

// passiveVariantState is what the last cycle reported about one variant's
// passive mode, so a change is logged once rather than every cycle.
type passiveVariantState struct {
	mode   passive.Mode
	source string
	paused bool
}

// observePassive resolves every active variant's passive mode for this cycle,
// publishes wva_passive_mode, and logs each change: a variant entering or
// leaving passive mode, an unsupported scalingModifiers formula (observe-
// unsafe), and KEDA's own pause annotation set or cleared on its
// ScaledObject. It fills e.cyclePassive (models, keyed namespace/modelID)
// and e.cyclePassiveVariants (variants, keyed namespace/scale-target name),
// which the rest of the cycle reads.
//
// Without a resolver every variant is active and nothing is published.
func (e *Engine) observePassive(logger logr.Logger, vas []llmdVariantAutoscalingV1alpha1.VariantAutoscaling) {
	e.cyclePassive = map[string]bool{}
	e.cyclePassiveVariants = map[string]passive.Mode{}
	if e.Passive == nil {
		return
	}
	next := make(map[string]passiveVariantState, len(vas))
	published := make([]metrics.PassiveVariant, 0, len(vas))
	for i := range vas {
		va := &vas[i]
		target := va.GetScaleTargetName()
		key := utils.GetNamespacedKey(va.Namespace, target)
		var entry registry.Entry
		found := false
		if e.Variants != nil {
			if entry, found = e.Variants.Get(va.Namespace, va.Name); !found {
				entry, found = e.Variants.ByTargetName(va.Namespace, target)
			}
		}
		meta := map[string]string{registry.ModelIDKey: va.Spec.ModelID}
		if found {
			meta = entry.Metadata
		}
		mode, source := e.Passive.ForMetadata(va.Namespace, meta, entry.Target)
		st := passiveVariantState{mode: mode, source: source, paused: entry.Target.KEDAPaused}
		next[key] = st
		e.cyclePassiveVariants[key] = mode
		if mode.Passive() {
			e.cyclePassive[utils.GetNamespacedKey(va.Namespace, va.Spec.ModelID)] = true
		}
		published = append(published, metrics.PassiveVariant{
			Namespace: va.Namespace, ModelName: va.Spec.ModelID, Variant: va.Name, Mode: string(mode)})
		e.reportPassiveChange(logger, va, target, e.passiveLast[key], st, entry.Target)
	}
	e.passiveLast = next
	metrics.PublishPassiveModes(published)
}

// reportPassiveChange logs what changed between a variant's last reported
// passive state and this cycle's.
func (e *Engine) reportPassiveChange(logger logr.Logger, va *llmdVariantAutoscalingV1alpha1.VariantAutoscaling,
	target string, prev, cur passiveVariantState, t registry.Target) {
	kv := []any{"namespace", va.Namespace, "model", va.Spec.ModelID, "variant", target,
		"mode", string(cur.mode), "source", cur.source, "foreignTriggers", t.ForeignTriggers}
	prevPassive := prev.mode.Passive()
	switch {
	case cur.mode.Passive() && !prevPassive:
		logger.Info("Passive mode entered: the controller computes and publishes for this variant and changes none of its replica counts", kv...)
	case !cur.mode.Passive() && prevPassive:
		logger.Info("Passive mode left: the controller scales this variant again, under the HPA's own behavior", kv...)
	case cur.mode != prev.mode && cur.mode.Passive():
		logger.Info("Passive mode changed", append(kv, "previousMode", string(prev.mode))...)
	}
	if cur.mode == passive.ObserveUnsafe && prev.mode != passive.ObserveUnsafe {
		logger.Info("WARNING: passive observe is unsupported here: the ScaledObject's scalingModifiers formula names "+
			"this controller's trigger, so its answer of 0 enters the formula. Remove the trigger from the formula, "+
			"or remove the trigger", kv...)
	}
	if cur.paused != prev.paused {
		if cur.paused {
			logger.Info("The ScaledObject is paused by KEDA (autoscaling.keda.sh/paused or paused-replicas): "+
				"KEDA acts on no answer of this controller while it is set", kv...)
		} else if prev.mode != "" {
			logger.Info("The ScaledObject is no longer paused by KEDA", kv...)
		}
	}
}

// variantPassive reports whether the variant whose scale target is target is
// passive this cycle.
func (e *Engine) variantPassive(namespace, target string) bool {
	return e.cyclePassiveVariants[utils.GetNamespacedKey(namespace, target)].Passive()
}

// modelPassive reports whether a model is passive this cycle.
func (e *Engine) modelPassive(namespace, modelID string) bool {
	return e.cyclePassive[utils.GetNamespacedKey(namespace, modelID)]
}

// groupPassive reports whether any model of a utilization-share group is
// passive this cycle. Such a group is planned in shadow: no transfer, no pod
// mark, no history write, and its active models fall back to the ordinary
// optimizer's targets (docs/reference/scaling-policy.md, "passive").
func (e *Engine) groupPassive(g allocation.ShareGroup) bool {
	for _, o := range g.Origins {
		if e.modelPassive(o.Namespace, o.ModelID) {
			return true
		}
	}
	return false
}

// recordPassiveRecommendation is recordScalingEvent for a passive variant: the
// event says what the controller would do, never that it did it.
func (e *Engine) recordPassiveRecommendation(va *llmdVariantAutoscalingV1alpha1.VariantAutoscaling,
	action domain.SaturationAction, current, target int, reason string) {
	if e.Recorder == nil || !slices.Contains([]domain.SaturationAction{domain.ActionScaleUp, domain.ActionScaleDown}, action) {
		return
	}
	e.recordEvent(va, corev1.EventTypeNormal, constants.K8SEventPassiveRecommendation,
		fmt.Sprintf("passive: would scale from %d to %d replicas: %s", current, target, reason))
}

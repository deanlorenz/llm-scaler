package controller

import (
	"context"
	"fmt"
	"os"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
)

// passiveInManagerConfig reads WVA_PASSIVE from the manager ConfigMap's
// config.yaml. ok is false when the key is absent or not a boolean.
func passiveInManagerConfig(cm *corev1.ConfigMap) (passive, ok bool) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &m); err != nil {
		return false, false
	}
	v, found := m["WVA_PASSIVE"]
	if !found {
		return false, false
	}
	b, err := strconv.ParseBool(fmt.Sprint(v))
	if err != nil {
		return false, false
	}
	return b, true
}

// warnIfPassiveChanged warns, once per change, when the manager ConfigMap's
// WVA_PASSIVE differs from the value this process read at start: the flag is
// read only then, so the edit does nothing until the controller restarts. An
// absent key reads as the default, false. An environment variable overrides
// the ConfigMap, so then there is nothing to warn about.
func (r *ConfigMapReconciler) warnIfPassiveChanged(ctx context.Context, cm *corev1.ConfigMap) {
	if r.Config == nil || os.Getenv("WVA_PASSIVE") != "" {
		return
	}
	want, ok := passiveInManagerConfig(cm)
	if !ok {
		want = false
	}
	running := r.Config.PassiveDefault()
	if want == running {
		r.passiveWarned = ""
		return
	}
	key := strconv.FormatBool(want)
	if r.passiveWarned == key {
		return
	}
	r.passiveWarned = key
	msg := fmt.Sprintf("WVA_PASSIVE in %s is now %t and this controller runs with %t: it is read only at start, so restart the controller to apply it",
		cm.Name, want, running)
	log.FromContext(ctx).Info("WARNING: restart required: "+msg, "namespace", cm.Namespace, "configMap", cm.Name)
	if r.Recorder != nil {
		r.Recorder.Event(cm, corev1.EventTypeWarning, constants.K8SEventPassiveRestartRequired, msg)
	}
}

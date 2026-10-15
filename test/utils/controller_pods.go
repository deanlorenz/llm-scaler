package utils

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ControllerDeploymentName is the WVA controller Deployment every e2e install
// creates (config/base's controller-manager with the wva- prefix).
const ControllerDeploymentName = "wva-controller-manager"

// controllerLabelSelector narrows the list before ownership is checked. It
// does not identify the controller on its own: see IsControllerPod.
const controllerLabelSelector = "control-plane=controller-manager"

// IsControllerPod reports whether pod was created by the named controller
// Deployment.
//
// Labels cannot answer this. The e2e relay Pods wear the controller's labels on
// purpose (fixtures.ControllerDriverLabels), so the pool's NetworkPolicy admits
// them, and the warm-pool Pods carry app.kubernetes.io/name too. Both can live
// in the controller's namespace. Ownership can: a Deployment's ReplicaSet is
// named <deployment>-<pod-template-hash>, and the Pod carries that hash.
func IsControllerPod(pod *corev1.Pod, deployment string) bool {
	hash := pod.Labels["pod-template-hash"]
	if hash == "" {
		return false
	}
	owner := metav1.GetControllerOf(pod)
	return owner != nil && owner.Kind == "ReplicaSet" && owner.Name == deployment+"-"+hash
}

// ControllerPods lists the Pods of the named controller Deployment in
// namespace, and no other Pod that happens to share its labels.
func ControllerPods(ctx context.Context, k8s kubernetes.Interface, namespace, deployment string) ([]corev1.Pod, error) {
	list, err := k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: controllerLabelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("list controller pods in %s: %w", namespace, err)
	}
	var pods []corev1.Pod
	for i := range list.Items {
		if IsControllerPod(&list.Items[i], deployment) {
			pods = append(pods, list.Items[i])
		}
	}
	return pods, nil
}

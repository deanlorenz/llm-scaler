package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/controllerstatus"
)

// The controller-status ConfigMap is how a tool confirms a policy write: it
// writes the policy, then waits for policyResourceVersion to show that version.
// So the version must be recorded by every path that applies the authoritative
// policy -- and the accepted flag must say when the default entry was dropped.
var _ = Describe("Controller status from the ConfigMap reconciler", func() {
	const (
		systemNS = "wva-status-system"
		policyNS = "wva-status-policy"
	)

	// A shadow-mode utilization share bounded by a named quota: every derived
	// field differs from the publisher's defaults.
	const quotaShadowPolicy = `limiters:
  - type: quota
    name: install-quota
    scope: cluster
    quotas: { H100: 8 }
optimizer:
  type: utilizationShare
  utilizationShare:
    shadow: true
`
	const rejectedPolicy = "kvCacheThreshold: 1.5\n"

	var (
		ctx    context.Context
		cfg    *config.Config
		status *controllerstatus.Publisher
	)

	newReconciler := func(objs ...client.Object) *ConfigMapReconciler {
		s := runtime.NewScheme()
		Expect(corev1.AddToScheme(s)).To(Succeed())
		c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
		status = controllerstatus.New(c, c, systemNS)
		return &ConfigMapReconciler{Reader: c, Scheme: s, Config: cfg, Status: status}
	}

	policyConfigMap := func(namespace, rv, body string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: config.ScalingPolicyConfigMapName(), Namespace: namespace, ResourceVersion: rv,
			},
			Data: map[string]string{config.GlobalDefaultsKey: body},
		}
	}

	reconcile := func(r *ConfigMapReconciler, namespace string) {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{
			Name: config.ScalingPolicyConfigMapName(), Namespace: namespace,
		}})
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeEach(func() {
		ctx = context.Background()
		GinkgoT().Setenv("POD_NAMESPACE", systemNS)
		cfg = config.NewTestConfig()
	})

	Context("when the controller's own namespace holds the policy", func() {
		It("records the version it applied, the limiter and the optimizer mode", func() {
			r := newReconciler(policyConfigMap(systemNS, "", quotaShadowPolicy))
			reconcile(r, systemNS)

			var cm corev1.ConfigMap
			Expect(r.Get(ctx, client.ObjectKey{Namespace: systemNS, Name: config.ScalingPolicyConfigMapName()}, &cm)).To(Succeed())
			Expect(cm.ResourceVersion).NotTo(BeEmpty())

			d := status.Data()
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyNamespace, systemNS))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyConfigMap, config.ScalingPolicyConfigMapName()))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyResourceVersion, cm.ResourceVersion))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyAccepted, "true"))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyError, ""))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyLimiter, "quota/install-quota"))
			// Recomputed where the policy is applied, not at the next cycle.
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyUtilizationShare, constants.UtilizationShareModeShadow))
		})

		It("records a rejected default entry as not accepted, with the reason", func() {
			r := newReconciler(policyConfigMap(systemNS, "", rejectedPolicy))
			reconcile(r, systemNS)

			d := status.Data()
			Expect(d[controllerstatus.KeyPolicyResourceVersion]).NotTo(BeEmpty())
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyAccepted, "false"))
			Expect(d[controllerstatus.KeyPolicyError]).To(ContainSubstring("kvCacheThreshold"))
		})
	})

	Context("when a separate policy namespace holds the policy", func() {
		BeforeEach(func() {
			cfg.SetPolicyNamespace(policyNS, config.PolicySourceWellKnown)
		})

		It("records the policy namespace's version, not the controller's own", func() {
			r := newReconciler(
				policyConfigMap(policyNS, "", quotaShadowPolicy),
				policyConfigMap(systemNS, "", "kvCacheThreshold: 0.7\n"),
			)
			reconcile(r, policyNS)
			var policy corev1.ConfigMap
			Expect(r.Get(ctx, client.ObjectKey{Namespace: policyNS, Name: config.ScalingPolicyConfigMapName()}, &policy)).To(Succeed())

			d := status.Data()
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyNamespace, policyNS))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyResourceVersion, policy.ResourceVersion))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyAccepted, "true"))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyLimiter, "quota/install-quota"))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyUtilizationShare, constants.UtilizationShareModeShadow))

			// The controller's own ConfigMap carries no authority over limiters
			// here, so applying it must not move the recorded version.
			reconcile(r, systemNS)
			Expect(status.Data()).To(HaveKeyWithValue(controllerstatus.KeyPolicyNamespace, policyNS))
			Expect(status.Data()).To(HaveKeyWithValue(controllerstatus.KeyPolicyResourceVersion, policy.ResourceVersion))
		})

		It("records a rejected default entry in the policy namespace", func() {
			r := newReconciler(policyConfigMap(policyNS, "", rejectedPolicy))
			reconcile(r, policyNS)

			d := status.Data()
			Expect(d[controllerstatus.KeyPolicyResourceVersion]).NotTo(BeEmpty())
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyAccepted, "false"))
			Expect(d[controllerstatus.KeyPolicyError]).To(ContainSubstring("kvCacheThreshold"))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyLimiter, "none"))
		})

		It("records the bootstrap read, and a deletion as no policy in force", func() {
			r := newReconciler(policyConfigMap(policyNS, "", quotaShadowPolicy))
			Expect(r.bootstrapClusterPolicy(ctx)).To(Succeed())
			Expect(status.Data()[controllerstatus.KeyPolicyResourceVersion]).NotTo(BeEmpty())

			r.handleConfigMapDeletion(ctx, config.ScalingPolicyConfigMapName(), policyNS)
			d := status.Data()
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyResourceVersion, ""))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyPolicyAccepted, "true"))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyLimiter, "none"))
			Expect(d).To(HaveKeyWithValue(controllerstatus.KeyUtilizationShare, constants.UtilizationShareModeOff))
		})
	})
})

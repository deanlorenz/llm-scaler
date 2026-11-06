package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
)

// These tests are about the scaling-policy ConfigMap's name from before the
// project's rename. hack/rename rewrites this file, so the current name below
// changes and config.RenamedScalingPolicyConfigMapName does not. Until the rename
// the two are one name and the tests that need both are skipped: they prove
// nothing on such a tree, and a green run there says nothing about them.

func oldNameCM(namespace, defaultEntry string) *corev1.ConfigMap {
	cm := policyCM(namespace, defaultEntry)
	cm.Name = config.RenamedScalingPolicyConfigMapName
	return cm
}

func requireTwoNames(t *testing.T) {
	t.Helper()
	if config.ScalingPolicyConfigMapName() == config.RenamedScalingPolicyConfigMapName {
		t.Skip("before the rename there is only one name")
	}
}

// TestOldNameInPolicyNamespaceKeepsTheBound: an admin's limiters under the old
// name in the policy namespace. Nothing writes that ConfigMap on upgrade, so
// without the fall-back this reads NotFound, then an empty policy: no bound.
func TestOldNameInPolicyNamespaceKeepsTheBound(t *testing.T) {
	f := newRejectsFixture(t)
	f.cfg.SetPolicyNamespace(rejectsPolicyNS, config.PolicySourceWellKnown)
	if err := f.bootstrapWith(oldNameCM(rejectsPolicyNS, quotaDefault)); err != nil {
		t.Fatal(err)
	}
	if got := limiterSet(f.cfg); got != string(config.LimiterTypeQuota) {
		t.Errorf("limiter mode %q, want the quota the old ConfigMap declares", got)
	}
}

func TestCurrentNameBeatsOldNameInPolicyNamespace(t *testing.T) {
	requireTwoNames(t)
	f := newRejectsFixture(t)
	f.cfg.SetPolicyNamespace(rejectsPolicyNS, config.PolicySourceWellKnown)
	stale := oldNameCM(rejectsPolicyNS, quotaDefault)
	if err := f.bootstrapWith(stale, policyCM(rejectsPolicyNS, "{}")); err != nil {
		t.Fatal(err)
	}
	if got := limiterSet(f.cfg); got == string(config.LimiterTypeQuota) {
		t.Error("the old ConfigMap won over the current one at startup")
	}

	// A watch event on the stale copy must not bring it back.
	if _, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(stale)}); err != nil {
		t.Fatal(err)
	}
	if got := limiterSet(f.cfg); got == string(config.LimiterTypeQuota) {
		t.Error("an event on the old ConfigMap won over the current one")
	}
}

// TestDeletingTheOldNameKeepsTheBound is the last step off the fall-back: copy
// to the new name, delete the old one. The deletion must not lift the bound.
func TestDeletingTheOldNameKeepsTheBound(t *testing.T) {
	requireTwoNames(t)
	f := newRejectsFixture(t)
	f.cfg.SetPolicyNamespace(rejectsPolicyNS, config.PolicySourceWellKnown)
	stale := oldNameCM(rejectsPolicyNS, quotaDefault)
	if err := f.bootstrapWith(stale, policyCM(rejectsPolicyNS, quotaDefault)); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	f.r.handleConfigMapDeletion(context.Background(), stale.Name, stale.Namespace)
	if got := limiterSet(f.cfg); got != string(config.LimiterTypeQuota) {
		t.Errorf("deleting the old ConfigMap lifted the bound the current one carries: mode %q", got)
	}
}

// TestDeletingTheOnlyOneStillLiftsTheBound: an admin who removes the policy
// still removes it.
func TestDeletingTheOnlyOneStillLiftsTheBound(t *testing.T) {
	f := newRejectsFixture(t)
	f.cfg.SetPolicyNamespace(rejectsPolicyNS, config.PolicySourceWellKnown)
	only := oldNameCM(rejectsPolicyNS, quotaDefault)
	if err := f.bootstrapWith(only); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), only); err != nil {
		t.Fatal(err)
	}
	f.r.handleConfigMapDeletion(context.Background(), only.Name, only.Namespace)
	if got := limiterSet(f.cfg); got == string(config.LimiterTypeQuota) {
		t.Errorf("the bound outlived the only ConfigMap that carried it: mode %q", got)
	}
}

const oldNameTenantNS = "tenant-old-name"

func optedInNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{"wva.llmd.ai/config-enabled": "true"},
	}}
}

// TestOldNameTenantCopyLoads is the same loss one level down: a tenant's copy
// under the old name in an opted-in namespace.
func TestOldNameTenantCopyLoads(t *testing.T) {
	f := newRejectsFixture(t)
	if err := f.c.Create(context.Background(), optedInNamespace(oldNameTenantNS)); err != nil {
		t.Fatal(err)
	}
	if err := f.bootstrapWith(oldNameCM(oldNameTenantNS, "{}")); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.cfg.ScalingPolicyConfigForNamespace(oldNameTenantNS)[config.GlobalDefaultsKey]; !ok {
		t.Error("the tenant's ConfigMap under the old name was not loaded")
	}
}

// TestDeletingTheOldTenantCopyKeepsTheCurrentOne: removing the stale copy must
// not remove the namespace's configuration.
func TestDeletingTheOldTenantCopyKeepsTheCurrentOne(t *testing.T) {
	requireTwoNames(t)
	f := newRejectsFixture(t)
	if err := f.c.Create(context.Background(), optedInNamespace(oldNameTenantNS)); err != nil {
		t.Fatal(err)
	}
	stale := oldNameCM(oldNameTenantNS, "{}")
	if err := f.bootstrapWith(stale, policyCM(oldNameTenantNS, "{}")); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	f.r.handleConfigMapDeletion(context.Background(), stale.Name, stale.Namespace)
	if _, ok := f.cfg.ScalingPolicyConfigForNamespace(oldNameTenantNS)[config.GlobalDefaultsKey]; !ok {
		t.Error("deleting the old copy removed the configuration the current one carries")
	}
}

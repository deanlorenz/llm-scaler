package steadystate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/controllerstatus"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/kueue"
)

// lenderReleaseWorld is namespace "b" at guaranteed 16 holding 48 GPUs on
// H100 (six 8-GPU replicas of model "x"), in cohort "c", whose pool opts in
// to releaseToNonReclaimingLenders and reads a Never lender waiting for 16.
func lenderReleaseWorld(readAt time.Time) ([]*allocation.ResourceConstraints, []allocation.ModelScalingRequest) {
	c := &allocation.ResourceConstraints{
		ProviderName: "namespace-quota", NamespaceScoped: true, KueueBacked: true, KueueCountsBorrowing: true,
		NamespacePools:   map[string]map[string]allocation.ResourcePool{"b": {"H100": {Limit: 16, Used: 48}}},
		NamespaceBorrow:  map[string]map[string]allocation.NamespaceBorrow{"b": {"H100": {Cap: config.QuotaUnlimited}}},
		NamespaceCohorts: map[string][]string{"b": {"c"}},
		SharedPools: []allocation.SharedPoolChunk{{Cohort: "c", Flavor: "h100", Resource: "nvidia.com/gpu",
			FlavorPlacement: kueue.FlavorPlacement{AcceleratorType: "NVIDIA-H100-80GB-HBM3"},
			Lenders:         []kueue.LenderState{{Queue: "lender", Waiting: true, Reclaim: kueue.ReclaimNever, Unused: 32, PendingGPUs: 16}},
			ReadAt:          readAt, Borrowed: 32, ReleaseToNonReclaiming: true}},
	}
	reqs := []allocation.ModelScalingRequest{{ModelID: "x", Namespace: "b", Priority: 1,
		Variants: []domain.VariantMetadata{{VariantName: "x-v", AcceleratorName: "H100"}},
		VariantStates: []domain.VariantReplicaState{{VariantName: "x-v", CurrentReplicas: 6, GPUsPerReplica: 8,
			AcceleratorName: "H100"}}}}
	return []*allocation.ResourceConstraints{c}, reqs
}

func lenderDecisions(reqs []allocation.ModelScalingRequest) []domain.VariantDecision {
	out := make([]domain.VariantDecision, 0, len(reqs))
	for _, r := range reqs {
		for _, s := range r.VariantStates {
			out = append(out, domain.VariantDecision{Namespace: r.Namespace, ModelID: r.ModelID, VariantName: s.VariantName,
				AcceleratorName: s.AcceleratorName, CurrentReplicas: s.CurrentReplicas, TargetReplicas: s.CurrentReplicas,
				GPUsPerReplica: s.GPUsPerReplica})
		}
	}
	return out
}

// A release to a lender is persisted in the controller status, and a new
// leader term that reads it back holds the pin and releases nothing again.
func TestALenderReleaseSurvivesANewLeaderTerm(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(50000, 0)
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cs, reqs := lenderReleaseWorld(now)

	first := fake.NewClientBuilder().WithScheme(scheme).Build()
	e := &Engine{Config: config.NewTestConfig(), Status: controllerstatus.New(first, first, config.SystemNamespace())}
	e.borrowing.governor = allocation.NewBorrowGovernor(allocation.DefaultBorrowSettings())
	e.borrowing.ledgerLoaded = true
	e.borrowing.cycle = allocation.BorrowCycle{Now: now, Constraints: cs, Requests: reqs}
	e.borrowing.governor.Observe(e.borrowing.cycle)
	ds := lenderDecisions(reqs)
	e.releaseBorrowed(ctx, reqs, ds, nil, now)
	if ds[0].TargetReplicas != 4 {
		t.Fatalf("x's target is %d, want 4: two 8-GPU replicas for the lender's 16", ds[0].TargetReplicas)
	}
	raw := e.Status.Data()[controllerstatus.KeyBorrowReleaseLedger]
	if !strings.Contains(raw, `"lenderReleases"`) || !strings.Contains(raw, `"version":1`) {
		t.Fatalf("persisted ledger %q carries no versioned lender release", raw)
	}

	// The next term reads the ConfigMap the last one wrote.
	next := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: controllerstatus.ConfigMapName, Namespace: config.SystemNamespace()},
		Data:       map[string]string{controllerstatus.KeyBorrowReleaseLedger: raw}}).Build()
	e2 := &Engine{Config: config.NewTestConfig(), client: next, Status: controllerstatus.New(next, next, config.SystemNamespace())}
	if !e2.restoreHeldReleases(ctx) {
		t.Fatal("a cycle that counts no borrowing dropped the persisted lender release")
	}
	later := now.Add(time.Minute)
	ds2 := lenderDecisions(reqs)
	e2.releaseBorrowed(ctx, reqs, ds2, nil, later)
	if ds2[0].TargetReplicas != 4 {
		t.Errorf("after the new term x's target is %d, want the pin at 4", ds2[0].TargetReplicas)
	}
	e2.borrowing.cycle = allocation.BorrowCycle{Now: later, Constraints: cs, Requests: reqs}
	e2.borrowing.governor.Observe(e2.borrowing.cycle)
	ds3 := lenderDecisions(reqs)
	if rel := e2.borrowing.governor.Releases(e2.borrowing.cycle, ds3); len(rel) != 0 || ds3[0].TargetReplicas != 4 {
		t.Errorf("the new term released %+v (target %d); want nothing new and the pin at 4", rel, ds3[0].TargetReplicas)
	}
}

// Until the persisted ledger is read, no release to a lender is decided.
func TestNoLenderReleaseWhileTheLedgerIsUnread(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(50000, 0)
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	failing := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok {
				return errors.New("etcd is down")
			}
			return c.Get(ctx, key, obj, opts...)
		}}).Build()
	cs, reqs := lenderReleaseWorld(now)
	e := &Engine{Config: config.NewTestConfig(), client: failing}
	e.borrowing.inFlight = allocation.NewBorrowInFlight()
	e.governBorrowing(ctx, cs, reqs, nil, now)
	if !e.borrowing.cycle.LedgerUnread {
		t.Fatal("the cycle does not say the ledger is unread after a failed read")
	}
	ds := lenderDecisions(reqs)
	if rel := e.borrowing.governor.Releases(e.borrowing.cycle, ds); len(rel) != 0 {
		t.Errorf("released %+v before the persisted ledger was read", rel)
	}
}

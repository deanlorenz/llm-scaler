package steadystate

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/kueue"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils/scaletarget"
)

// kueueSnapshot is a QuotaSource reporting one fixed Kueue read, replaceable.
type kueueSnapshot struct {
	mu   sync.Mutex
	snap config.ExternalQuotas
}

func (s *kueueSnapshot) Quotas(context.Context) (config.ExternalQuotas, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap, nil
}

func (s *kueueSnapshot) set(q config.ExternalQuotas) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = q
}

// eligibleEverywhere is a borrowing eligibility check that finds every
// namespace eligible.
type eligibleEverywhere struct{}

func (eligibleEverywhere) Evaluate(_ context.Context, namespaces []string) map[string]kueue.NamespaceEligibility {
	out := map[string]kueue.NamespaceEligibility{}
	for _, ns := range namespaces {
		out[ns] = kueue.NamespaceEligibility{}
	}
	return out
}

// servingQueue is a Kueue read granting namespace serving one ClusterQueue of
// 16 H100 GPUs, in cohort (none when empty), with reserved GPUs reserved in it.
func servingQueue(cohort string, reserved int, now time.Time) config.ExternalQuotas {
	caps := config.ExternalCaps{ByType: map[string]int{"H100": 16}}
	row := config.ExternalBorrow{Queue: "a", Cohort: cohort, Flavor: "h100", Resource: "nvidia.com/gpu", Accelerator: "H100",
		Nominal: 16, Guaranteed: 16, WithinNominal: 16, Reserved: reserved}
	if cohort == "" {
		row.Reason = string(kueue.ReasonNoCohort)
	}
	return config.ExternalQuotas{
		Namespace:              map[string]config.ExternalCaps{"serving": caps},
		NamespaceGuaranteed:    map[string]config.ExternalCaps{"serving": caps},
		NamespaceWithinNominal: map[string]config.ExternalCaps{"serving": caps},
		Borrow:                 map[string][]config.ExternalBorrow{"serving": {row}},
		ObservedAt:             now,
	}
}

// A replica Kueue keeps at its admission gate, in a Kueue-backed namespace
// that counts no borrowing: an entry with countBorrowable: false, and a
// queue in no cohort. In 8-GPU units: serving's queue holds 16; an unmanaged
// training job holds 8 of it, serving's variant 8 admitted and 8 more gated.
// Kueue reserved 16 and will never admit the gated replica.
//
//   - The charge. The engine lists the gated pod, so the next read of the
//     quota does not take its GPUs off Kueue's reservation: the figure is the
//     training job's 8 less, 8, not 16. Before, the gated pods were listed only
//     where borrowing was counted: the gated 8 cancelled the training job's
//     charge, the figure read 16, and nothing was past it.
//   - The release. Past the placement timeout the gated replica is released,
//     2 -> 1, with the BorrowedCapacityReleased Event. Before, the placement
//     timeout ran only where borrowing was counted, and the replica waited
//     gated for good.
//   - Control: a replica Kueue admits within the timeout -- the training job
//     ended -- is not released.
func TestGatedReplicaOutsideBorrowingIsChargedAndReleased(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name   string
		cohort string
		entry  config.KueueQuotaSource
	}{
		{"countBorrowable false", "c", config.KueueQuotaSource{Enabled: true, CountBorrowable: &no}},
		{"queue in no cohort", "", config.KueueQuotaSource{Enabled: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			t0 := time.Now()
			timeout := config.DefaultPlacementTimeout
			usage := map[string]map[string]int{"serving": {"H100": 16}}

			src := &kueueSnapshot{}
			src.set(servingQueue(tc.cohort, 16, t0))
			entry := config.QuotaLimiterConfig{Name: "kq", Type: "quota", Scope: config.QuotaScopeNamespace,
				NamespaceQuotas: map[string]map[string]int{"default": {"H100": -1}}, Kueue: &tc.entry}
			rec := allocation.NewBorrowInFlight()
			inv := allocation.NewQuotaInventoryWithSource(entry, src)
			inv.SetGatedSource(rec)
			if entry.KueueCountsBorrowable() {
				inv.EnableBorrowing(allocation.BorrowOptions{Physical: true, Eligibility: eligibleEverywhere{}})
			}
			limiter := allocation.NewDefaultLimiter(entry.Name, inv)
			compute := func() []*allocation.ResourceConstraints {
				t.Helper()
				c, err := limiter.ComputeConstraints(ctx, nil, usage)
				if err != nil {
					t.Fatal(err)
				}
				if c.NamespaceBorrow != nil {
					t.Fatalf("the entry counts borrowing for %v; the case is one that counts none", c.NamespaceBorrow)
				}
				return []*allocation.ResourceConstraints{c}
			}
			figure := func(cs []*allocation.ResourceConstraints) int {
				return cs[0].NamespacePools["serving"]["H100"].Limit
			}

			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			gpus := corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("8")}
			pod := func(name string, gated bool) *corev1.Pod {
				p := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "serving", UID: k8stypes.UID("uid-" + name)},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c",
						Resources: corev1.ResourceRequirements{Requests: gpus, Limits: gpus}}}},
				}
				if gated {
					p.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: kueue.AdmissionGate}}
				} else {
					p.Spec.NodeName = "n1"
				}
				return p
			}
			engine := func(objs ...client.Object) (*Engine, *record.FakeRecorder) {
				r := record.NewFakeRecorder(10)
				e := &Engine{Config: config.NewTestConfig(), Recorder: r,
					client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()}
				e.borrowing.inFlight = rec
				return e, r
			}
			deployment := map[string]scaletarget.ScaleTargetAccessor{"serving/v": scaletarget.NewDeploymentAccessor(
				&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "serving"}})}
			gatedSince := t0.Add(-time.Minute)
			state := func(gated bool) []allocation.ModelScalingRequest {
				s := domain.VariantReplicaState{VariantName: "v", CurrentReplicas: 2, DesiredReplicas: 2, GPUsPerReplica: 8,
					AcceleratorName: "H100"}
				if gated {
					s.UnplacedReplicas, s.UnplacedSince, s.UnplacedPods = 1, gatedSince, []string{"v-1"}
					s.UnplacedAt = map[string]time.Time{"v-1": gatedSince}
					s.GatedAt = map[string]time.Time{"v-1": gatedSince}
				}
				return []allocation.ModelScalingRequest{{ModelID: "m", Namespace: "serving",
					VariantStates: []domain.VariantReplicaState{s}}}
			}
			decisions := func() []domain.VariantDecision {
				return []domain.VariantDecision{{Namespace: "serving", ModelID: "m", VariantName: "v", AcceleratorName: "H100",
					CurrentReplicas: 2, TargetReplicas: 2, GPUsPerReplica: 8}}
			}

			// The charge.
			e, events := engine(pod("v-0", false), pod("v-1", true))
			if got := figure(compute()); got != 16 {
				t.Fatalf("before any pod list serving's figure is %d, want 16: the gated 8 still read as reserved", got)
			}
			e.prepareBorrowing(ctx, compute(), state(true), deployment, t0)
			cs := compute()
			if got := figure(cs); got != 8 {
				t.Fatalf("serving's figure is %d, want 8: the training job's 8 stay charged; the gated replica is not Kueue's reservation", got)
			}

			// The release.
			e.prepareBorrowing(ctx, cs, state(true), deployment, t0)
			ds := decisions()
			e.releaseBorrowed(ctx, state(true), ds, deployment, t0)
			if ds[0].TargetReplicas != 2 {
				t.Fatalf("released at once to %d; the placement timeout has not passed", ds[0].TargetReplicas)
			}
			t1 := t0.Add(timeout)
			e.prepareBorrowing(ctx, compute(), state(true), deployment, t1)
			ds = decisions()
			e.releaseBorrowed(ctx, state(true), ds, deployment, t1)
			if ds[0].TargetReplicas != 1 {
				t.Fatalf("serving's target is %d after the placement timeout, want 1: the gated replica is released", ds[0].TargetReplicas)
			}
			select {
			case ev := <-events.Events:
				if !strings.Contains(ev, constants.K8SEventBorrowedCapacityReleased) || !strings.Contains(ev, "admission gate") {
					t.Errorf("event %q, want %s for a replica held at Kueue's admission gate", ev, constants.K8SEventBorrowedCapacityReleased)
				}
			default:
				t.Error("no Event for the release")
			}

			// Control: Kueue admits the replica within the timeout.
			rec.Reset()
			src.set(servingQueue(tc.cohort, 16, t0))
			e, _ = engine(pod("v-0", false), pod("v-1", true))
			e.prepareBorrowing(ctx, compute(), state(true), deployment, t0)
			e.prepareBorrowing(ctx, compute(), state(true), deployment, t0)
			e.releaseBorrowed(ctx, state(true), decisions(), deployment, t0)
			admitted := t0.Add(timeout - time.Minute)
			src.set(servingQueue(tc.cohort, 16, admitted)) // the training job ended; serving's 16 are reserved
			e.client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod("v-0", false), pod("v-1", false)).Build()
			for _, at := range []time.Time{admitted, t1, t1.Add(timeout)} {
				e.prepareBorrowing(ctx, compute(), state(false), deployment, at)
				ds = decisions()
				e.releaseBorrowed(ctx, state(false), ds, deployment, at)
				if ds[0].TargetReplicas != 2 {
					t.Fatalf("at +%s serving's target is %d, want 2: Kueue admitted the replica within the timeout",
						at.Sub(t0), ds[0].TargetReplicas)
				}
			}
		})
	}
}

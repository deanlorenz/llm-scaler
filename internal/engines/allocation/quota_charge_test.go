package allocation

import (
	"context"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/kueue"
)

// chargeRow is queue a's H100 row of figures f as the reader reports it, with
// reserved what Kueue has charged the queue.
func chargeRow(cohort string, f kueue.QueueFigures, reserved int) config.ExternalBorrow {
	reason := f.Reason
	if reason == "" {
		reason = f.GuaranteedReason
	}
	return config.ExternalBorrow{Queue: "a", Cohort: cohort, Flavor: "h100", Resource: "nvidia.com/gpu", Accelerator: "H100",
		Nominal: f.Nominal, Guaranteed: f.Guaranteed, Borrowable: f.Borrowable, BorrowCap: f.BorrowCap,
		WithinNominal: f.WithinNominal, LentOut: f.LentOut, PreemptAfter: f.PreemptAfter, PreemptKept: f.PreemptKept,
		SourceBorrowed: f.KueueBorrowed, Reason: string(reason), Reserved: reserved}
}

// chargeSnapshot is a read granting row's queue to every namespace of nss.
func chargeSnapshot(row config.ExternalBorrow, free int, nss ...string) config.ExternalQuotas {
	caps := func(n int) config.ExternalCaps { return config.ExternalCaps{ByType: map[string]int{"H100": n}} }
	s := config.ExternalQuotas{
		Namespace: map[string]config.ExternalCaps{}, NamespaceGuaranteed: map[string]config.ExternalCaps{},
		NamespaceWithinNominal: map[string]config.ExternalCaps{}, Borrow: map[string][]config.ExternalBorrow{},
		ObservedAt: time.Now(),
	}
	for _, ns := range nss {
		s.Namespace[ns], s.NamespaceGuaranteed[ns], s.NamespaceWithinNominal[ns] = caps(row.Nominal), caps(row.Guaranteed), caps(row.WithinNominal)
		s.Borrow[ns] = []config.ExternalBorrow{row}
	}
	if row.Cohort != "" {
		s.SharedPools = []config.ExternalSharedPool{{Cohort: row.Cohort, Flavor: "h100", Resource: "nvidia.com/gpu",
			Accelerator: "H100", Free: free}}
	}
	return s
}

// KU1: a Kueue figure is charged with what Kueue reserved for workloads the
// scaling manager does not manage. Queue a (nominal 16) is serving's; an
// unmanaged training job holds 8 of it. Serving holds what managed says and
// asks for two more 8-GPU replicas. Kueue admits only what the queue has left
// after everything it reserved, so the quota may order no more. Before the
// fix the figure was charged with the managed GPUs alone: the training job's
// 8 read as free, and the replicas ordered there waited gated for good.
func TestKueueFigureChargedWithNonManagedReservation(t *testing.T) {
	never := figuresOf(t, h100Queue("a", kueue.ReclaimNever, kueue.QueueResource{Nominal: 16, Reserved: 8}),
		h100Queue("b", kueue.ReclaimNever, kueue.QueueResource{Nominal: 0, Reserved: 8, Borrowed: 8}))
	anyQ := figuresOf(t, h100Queue("a", kueue.ReclaimAny, kueue.QueueResource{Nominal: 16, Reserved: 8}),
		h100Queue("b", kueue.ReclaimNever, kueue.QueueResource{Nominal: 0, Reserved: 8, Borrowed: 8}))
	noCohort := kueue.QueueFigures{Nominal: 16, Guaranteed: 16, WithinNominal: 16, BorrowCap: config.QuotaUnlimited,
		Reason: kueue.ReasonNoCohort}
	for _, tc := range []struct {
		name     string
		snap     config.ExternalQuotas
		held     int // GPUs serving's variants hold
		want     int
		reserved int
	}{
		{"Never, training 8, peers borrow 8, serving 0: no replica", chargeSnapshot(chargeRow("c", never, 8), never.CohortFree, "serving"), 0, 0, 8},
		{"Any, training 8, peers borrow 8, serving 0: one replica, Kueue reclaims 8", chargeSnapshot(chargeRow("c", anyQ, 8), anyQ.CohortFree, "serving"), 0, 8, 8},
		{"no cohort, training 8, serving 0: one replica", chargeSnapshot(chargeRow("", noCohort, 8), 0, "serving"), 0, 8, 8},
		{"queue shared with an unmanaged namespace holding 8: one replica", chargeSnapshot(chargeRow("", noCohort, 8), 0, "serving", "training"), 0, 8, 8},
		{"no cohort, training 8, serving holds 8 of its own: nothing left", chargeSnapshot(chargeRow("", noCohort, 16), 0, "serving"), 8, 0, 16},
		{"control: serving holds the queue's only 8: one replica", chargeSnapshot(chargeRow("", noCohort, 8), 0, "serving"), 8, 8, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := config.QuotaLimiterConfig{Name: "namespace-quota", Type: "quota", Scope: config.QuotaScopeNamespace,
				NamespaceQuotas: map[string]map[string]int{"default": {"H100": -1}},
				Kueue:           &config.KueueQuotaSource{Enabled: true}}
			src := &fakeQuotaSource{}
			src.push(tc.snap, nil)
			inv := NewQuotaInventoryWithSource(e, src)
			inv.EnableBorrowing(BorrowOptions{Physical: true, Eligibility: &fakeEligibility{}})
			c, err := NewDefaultLimiter(e.Name, inv).ComputeConstraints(context.Background(), nil,
				map[string]map[string]int{"serving": {"H100": tc.held}})
			if err != nil {
				t.Fatal(err)
			}
			ds := NewGreedyByScoreOptimizer().Optimize(context.Background(),
				[]ModelScalingRequest{borrowModel("serving", "m", 8, tc.held/8, 2, nil)}, []*ResourceConstraints{c})
			if got := addedGPUs(ds)["m"]; got != tc.want {
				t.Fatalf("ordered %d GPUs, want %d (Kueue reserved %d, serving holds %d; pool %+v)",
					got, tc.want, tc.reserved, tc.held, c.NamespacePools["serving"])
			}
		})
	}
}

// KU1: the charge goes in before the static entry bounds the figure, so a
// static cap tighter than what Kueue leaves is not charged a second time.
func TestKueueChargeBeforeStaticBound(t *testing.T) {
	noCohort := kueue.QueueFigures{Nominal: 16, Guaranteed: 16, WithinNominal: 16, BorrowCap: config.QuotaUnlimited,
		Reason: kueue.ReasonNoCohort}
	e := config.QuotaLimiterConfig{Name: "namespace-quota", Type: "quota", Scope: config.QuotaScopeNamespace,
		NamespaceQuotas: map[string]map[string]int{"default": {"H100": 4}},
		Kueue:           &config.KueueQuotaSource{Enabled: true, CountBorrowable: new(bool)}}
	src := &fakeQuotaSource{}
	src.push(chargeSnapshot(chargeRow("", noCohort, 8), 0, "serving"), nil)
	inv := NewQuotaInventoryWithSource(e, src)
	inv.SetManagedUsage(map[string]map[string]int{"serving": {}})
	if err := inv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := inv.EffectiveConfig().NamespaceQuotas["serving"]["H100"]; got != 4 {
		t.Fatalf("cap %d, want 4: min(static 4, Kueue 16 less the 8 it reserved for others)", got)
	}
}

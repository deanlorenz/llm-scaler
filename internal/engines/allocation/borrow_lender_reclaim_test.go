package allocation

import (
	"context"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/kueue"
)

// noEligibilityLimits answers every namespace as managed, with readable
// ResourceQuotas.
type noEligibilityLimits struct{}

func (noEligibilityLimits) Evaluate(_ context.Context, namespaces []string) map[string]kueue.NamespaceEligibility {
	out := map[string]kueue.NamespaceEligibility{}
	for _, ns := range namespaces {
		out[ns] = kueue.NamespaceEligibility{}
	}
	return out
}

// lenderSnapshot is one Kueue read of cohort "c": namespace "serve" borrows
// through serve-cq (nominal 0, so all it reaches is borrowed), beside the given
// lenders; Kueue's free pool is what they lend unused.
func lenderSnapshot(at time.Time, lenders ...config.ExternalLender) config.ExternalQuotas {
	free := 0
	for _, l := range lenders {
		free += l.Unused
	}
	lenders = append(lenders, config.ExternalLender{Queue: "serve-cq", Reclaim: kueue.ReclaimNever})
	return config.ExternalQuotas{
		Namespace:              map[string]config.ExternalCaps{"serve": {ByType: map[string]int{"H100": 0}}},
		NamespaceGuaranteed:    map[string]config.ExternalCaps{"serve": {ByType: map[string]int{"H100": 0}}},
		NamespaceWithinNominal: map[string]config.ExternalCaps{"serve": {ByType: map[string]int{"H100": 0}}},
		Borrow: map[string][]config.ExternalBorrow{"serve": {{
			Queue: "serve-cq", Cohort: "c", Flavor: "h100", Resource: "nvidia.com/gpu", Accelerator: "H100",
			Borrowable: free, BorrowCap: config.QuotaUnlimited,
		}}},
		SharedPools: []config.ExternalSharedPool{{Cohort: "c", Flavor: "h100", Resource: "nvidia.com/gpu",
			Accelerator: "H100", Free: free, Lenders: lenders}},
		ObservedAt: at,
	}
}

// borrowingOf is what a namespace-scope Kueue entry counts of snap for
// "serve": its shared pool's free GPUs, whether it borrows, and why not.
func borrowingOf(t *testing.T, snap config.ExternalQuotas) (free int, borrows bool, withheld string) {
	t.Helper()
	e := config.QuotaLimiterConfig{
		Name: "namespace-quota", Type: "quota", Scope: config.QuotaScopeNamespace,
		NamespaceQuotas: map[string]map[string]int{"default": {"H100": -1}},
		Kueue:           &config.KueueQuotaSource{Enabled: true},
	}
	src := &fakeQuotaSource{}
	src.push(snap, nil)
	inv := NewQuotaInventoryWithSource(e, src)
	inv.now = func() time.Time { return snap.ObservedAt.Add(10 * time.Second) }
	inv.EnableBorrowing(BorrowOptions{Physical: true, Eligibility: noEligibilityLimits{}})
	active := map[string]map[string]int{}
	for ns := range snap.Borrow {
		active[ns] = map[string]int{}
	}
	c, err := NewDefaultLimiter(e.Name, inv).ComputeConstraints(context.Background(), nil, active)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range c.SharedPools {
		free += p.Free
	}
	_, borrows = c.NamespaceBorrow["serve"]
	return free, borrows, c.BorrowWithheld["serve"]
}

// A lender's unused nominal is borrowable only where Kueue would take it back
// for the lender: under Never it would not, and borrowing it would hold the
// lender's nominal until the borrower shrinks on its own.
func TestBorrowCountsOnlyWhatLendersCanReclaim(t *testing.T) {
	at := time.Unix(1000, 0)
	lender := func(name, reclaim string, unused int) config.ExternalLender {
		return config.ExternalLender{Queue: name, Reclaim: reclaim, Unused: unused, LentOut: unused}
	}

	t.Run("a lender on Never counts nothing, and the namespace is withheld as lender-cannot-reclaim", func(t *testing.T) {
		free, borrows, why := borrowingOf(t, lenderSnapshot(at, lender("batch-cq", kueue.ReclaimNever, 8)))
		if free != 0 || borrows {
			t.Errorf("counted %d GPUs of a lender that cannot reclaim (borrows %t), want 0", free, borrows)
		}
		if why != string(kueue.ReasonLenderCannotReclaim) {
			t.Errorf("withheld as %q, want %q", why, kueue.ReasonLenderCannotReclaim)
		}
	})

	t.Run("a lender on Any counts what it lends", func(t *testing.T) {
		free, borrows, why := borrowingOf(t, lenderSnapshot(at, lender("batch-cq", kueue.ReclaimAny, 8)))
		if free != 8 || !borrows || why != "" {
			t.Errorf("counted %d (borrows %t, withheld %q), want 8, borrowing, not withheld", free, borrows, why)
		}
	})

	// Kueue preempts, for a LowerPriority lender, a borrowed Workload of lower
	// priority than the lender's: what it lends is borrowable while the lender
	// outranks at least one Workload of the borrowing namespaces.
	lowerPriority := func(lenderPriority int32, borrowerLowest *int32) config.ExternalQuotas {
		l := lender("batch-cq", kueue.ReclaimLowerPriority, 8)
		l.Priority = &lenderPriority
		snap := lenderSnapshot(at, l)
		if borrowerLowest != nil {
			snap.NamespaceLowestPriority = map[string]int32{"serve": *borrowerLowest}
		}
		return snap
	}
	p := func(n int32) *int32 { return &n }
	t.Run("a lender on LowerPriority that outranks one borrowing Workload (100) but not the other (1000) counts", func(t *testing.T) {
		free, borrows, why := borrowingOf(t, lowerPriority(500, p(100)))
		if free != 8 || !borrows || why != "" {
			t.Errorf("counted %d (borrows %t, withheld %q), want 8: Kueue reclaims from the Workload at 100", free, borrows, why)
		}
		// The same across two borrowing namespaces: serve's Workloads at
		// 1000, canary's at 100.
		snap := lowerPriority(500, p(1000))
		canary := snap.Borrow["serve"][0]
		canary.Queue = "canary-cq"
		snap.Borrow["canary"] = []config.ExternalBorrow{canary}
		for _, m := range []map[string]config.ExternalCaps{snap.Namespace, snap.NamespaceGuaranteed, snap.NamespaceWithinNominal} {
			m["canary"] = m["serve"]
		}
		snap.SharedPools[0].Lenders = append(snap.SharedPools[0].Lenders, config.ExternalLender{Queue: "canary-cq", Reclaim: kueue.ReclaimNever})
		snap.NamespaceLowestPriority["canary"] = 100
		free, borrows, why = borrowingOf(t, snap)
		if free != 8 || !borrows || why != "" {
			t.Errorf("two borrowing namespaces: counted %d (borrows %t, withheld %q), want 8: Kueue reclaims from canary's Workload at 100",
				free, borrows, why)
		}
	})
	t.Run("a lender on LowerPriority that outranks no borrowing Workload counts nothing", func(t *testing.T) {
		for name, lowest := range map[string]*int32{"outranked": p(1000), "equal": p(500), "borrowers unread": nil} {
			free, borrows, why := borrowingOf(t, lowerPriority(500, lowest))
			if free != 0 || borrows || why != string(kueue.ReasonLenderCannotReclaim) {
				t.Errorf("%s: counted %d (borrows %t, withheld %q), want 0, withheld as %s",
					name, free, borrows, why, kueue.ReasonLenderCannotReclaim)
			}
		}
	})

	t.Run("beside each other only the one on Any counts", func(t *testing.T) {
		free, borrows, why := borrowingOf(t, lenderSnapshot(at,
			lender("any-cq", kueue.ReclaimAny, 8), lender("never-cq", kueue.ReclaimNever, 8)))
		if free != 8 || !borrows || why != "" {
			t.Errorf("counted %d of 16 (borrows %t, withheld %q), want the 8 of the lender on Any", free, borrows, why)
		}
	})
}

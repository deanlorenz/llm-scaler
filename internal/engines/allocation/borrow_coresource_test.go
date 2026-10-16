package allocation

import (
	"context"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/kueue"
)

// cpuModel is an H100 model of 8-GPU, 32-CPU replicas in ns wanting want more.
func cpuModel(ns string, want int) ModelScalingRequest {
	m := borrowModel(ns, "m", 8, 0, want, nil)
	m.VariantStates[0].ReplicaRequests = map[string]int64{"cpu": 32000, "nvidia.com/gpu": 8000}
	return m
}

// setCo sets namespace ns's co-resource figures on q's H100 borrowing.
func setCo(q *ResourceConstraints, ns string, headroom, kept map[string]int64) {
	b := q.NamespaceBorrow[ns]["H100"]
	b.CoHeadroom, b.CoKept = headroom, kept
	q.NamespaceBorrow[ns]["H100"] = b
}

// K2b: Kueue stops a queue's CPU at nominal plus borrowingLimit whatever the
// cohort holds free. 40 CPUs of headroom hold one 32-CPU replica, not two,
// though the pools hold 16 GPUs and plenty of CPU.
func TestCoResourceHeadroomBoundsBorrowing(t *testing.T) {
	q := borrowQuota(16, nsBorrow{ns: "b", cap: config.QuotaUnlimited})
	q.SharedPools[0].CoFree = map[string]int64{"cpu": 1 << 20}
	setCo(q, "b", map[string]int64{"cpu": 40000}, nil)
	got := addedGPUs(NewGreedyByScoreOptimizer().Optimize(context.Background(), []ModelScalingRequest{cpuModel("b", 2)},
		[]*ResourceConstraints{physicalH100(64), q}))
	if got["m"] != 8 {
		t.Errorf("ordered %d GPUs, want 8: the second replica's 32 CPUs pass the queue's CPU borrowingLimit", got["m"])
	}
}

// K2a: a pool whose CPU is charged on a flavor not known (its resource group
// offers several) withholds a variant as co-resource-bound, even one that
// states its CPU request.
func TestUnboundCoResourceWithholds(t *testing.T) {
	q := borrowQuota(16, nsBorrow{ns: "b", cap: config.QuotaUnlimited})
	q.SharedPools[0].CoUnbound = []string{"cpu"}
	got := WithholdByVariants([]*ResourceConstraints{q}, []ModelScalingRequest{cpuModel("b", 1)})
	if nb := got[0].NamespaceBorrow["b"]["H100"]; !nb.Frozen || nb.Withheld != "co-resource-bound" {
		t.Errorf("borrowing = %+v, want withheld as co-resource-bound", nb)
	}
}

// K2c: a replica within the guaranteed GPU figure is admitted only when its
// CPU fits too. Guaranteed 16 GPUs, 40 CPUs free in the pool and none kept:
// one 32-CPU replica, not two.
func TestGuaranteedReplicaChecksCoResources(t *testing.T) {
	run := func(kept map[string]int64) int {
		q := borrowQuota(0, nsBorrow{ns: "b", guaranteed: 16, cap: config.QuotaUnlimited})
		q.SharedPools[0].CoFree = map[string]int64{"cpu": 40000}
		setCo(q, "b", nil, kept)
		return addedGPUs(NewGreedyByScoreOptimizer().Optimize(context.Background(), []ModelScalingRequest{cpuModel("b", 2)},
			[]*ResourceConstraints{physicalH100(64), q}))["m"]
	}
	if got := run(nil); got != 8 {
		t.Errorf("ordered %d GPUs within guaranteed, want 8: the second replica's CPU does not fit", got)
	}
	if got := run(map[string]int64{"cpu": 32000}); got != 16 {
		t.Errorf("ordered %d GPUs with 32 CPUs kept unlent, want 16: the kept CPU holds the second replica", got)
	}
}

// K2 on the nominal path: Kueue admits a replica within nominal only when its
// CPU fits the queue's nominal CPU too, borrowing counted or not. A queue of
// 16 GPUs and 64 CPUs, nothing reserved: one 48-CPU replica, not two. Before,
// the co-resources were checked only where borrowing was counted, so an entry
// with countBorrowable false, or a queue in no cohort, ordered both and the
// second waited gated.
func TestNominalPathChecksCoResources(t *testing.T) {
	noCohort := kueue.QueueFigures{Nominal: 16, Guaranteed: 16, WithinNominal: 16, BorrowCap: config.QuotaUnlimited,
		Reason: kueue.ReasonNoCohort}
	inCohort := kueue.QueueFigures{Nominal: 16, Guaranteed: 16, WithinNominal: 16, BorrowCap: config.QuotaUnlimited}
	f := false
	for _, tc := range []struct {
		name  string
		row   config.ExternalBorrow
		count *bool
	}{
		{"countBorrowable false", chargeRow("c", inCohort, 0), &f},
		{"no cohort", chargeRow("", noCohort, 0), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.row.CoUnused = map[string]int64{"cpu": 64000}
			e := config.QuotaLimiterConfig{Name: "namespace-quota", Type: "quota", Scope: config.QuotaScopeNamespace,
				NamespaceQuotas: map[string]map[string]int{"default": {"H100": -1}},
				Kueue:           &config.KueueQuotaSource{Enabled: true, CountBorrowable: tc.count}}
			src := &fakeQuotaSource{}
			src.push(chargeSnapshot(tc.row, 0, "c"), nil)
			inv := NewQuotaInventoryWithSource(e, src)
			inv.EnableBorrowing(BorrowOptions{Physical: true, Eligibility: &fakeEligibility{}})
			c, err := NewDefaultLimiter(e.Name, inv).ComputeConstraints(context.Background(), nil, map[string]map[string]int{"c": {}})
			if err != nil {
				t.Fatal(err)
			}
			m := cpuModel("c", 2)
			m.VariantStates[0].ReplicaRequests["cpu"] = 48000
			got := addedGPUs(NewGreedyByScoreOptimizer().Optimize(context.Background(), []ModelScalingRequest{m},
				[]*ResourceConstraints{physicalH100(64), c}))
			if got["m"] != 8 {
				t.Errorf("ordered %d GPUs, want 8: the second 48-CPU replica does not fit the queue's 64 CPUs", got["m"])
			}
		})
	}
}

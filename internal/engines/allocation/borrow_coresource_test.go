package allocation

import (
	"context"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
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

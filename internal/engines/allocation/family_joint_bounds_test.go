package allocation

import (
	"context"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
)

// Bounds that only hold together: several cluster quotas that each list
// products of one family, and a namespace that lists the family and denies one
// of its products. Every replica below is a whole 8-GPU node.

// jbClusterQuota is a cluster-scope quota called name, with used GPUs already
// held under it.
func jbClusterQuota(t *testing.T, name string, quotas, used map[string]int) *ResourceConstraints {
	t.Helper()
	if used == nil {
		used = map[string]int{}
	}
	rc, err := NewDefaultLimiter(name, NewQuotaInventory(config.QuotaLimiterConfig{
		Name: name, Type: "quota", Scope: config.QuotaScopeCluster, ClusterQuotas: quotas,
	})).ComputeConstraints(context.Background(), used, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rc
}

// A share's promise of 8 H100s to namespace a, which may use the family, under
// a cluster quota of 8 HBM3 and 8 PCIe. The warm pool of a namespace the
// quotas exclude reads each product at what is left of the family: 8 each
// with 8 promised, 0 each with 16 promised. Taking either 8 leaves the
// promise its 8, on the other product, and the next reading of the other is 0.
func TestClusterHeadroomHoldsAFamilyPromise(t *testing.T) {
	decision.DefaultHeadroom.Reset()
	t.Cleanup(decision.DefaultHeadroom.Reset)
	build := func(usedHBM3 int) []*ResourceConstraints {
		return []*ResourceConstraints{
			jbClusterQuota(t, "cq0", map[string]int{famHBM3: 8, famPCIe: 8}, map[string]int{famHBM3: usedHBM3}),
			fkNamespaceQuota(t, "ns-quota", map[string]map[string]int{"a": {"H100": -1}}, "e"),
		}
	}
	read := func(c []*ResourceConstraints, promised int) map[string]int {
		o := NewGreedyByScoreOptimizer()
		o.Optimize(context.Background(), []ModelScalingRequest{famWanting("a", "ma", famHBM3, 0, 8), famWanting("e", "me", famPCIe, 0, 8)}, c)
		o.RepublishHeadroom(nil, map[string]map[string]int{"a": {"H100": promised}}, time.Now())
		out := map[string]int{}
		for _, acc := range []string{famHBM3, famPCIe} {
			v, state := decision.GPUHeadroomReading("e", acc, 0, time.Minute, time.Now())
			if state != decision.HeadroomBounded {
				v = -1
			}
			out[acc] = v
		}
		return out
	}

	if got := read(build(0), 8); got[famHBM3] != 8 || got[famPCIe] != 8 {
		t.Errorf("8 H100 promised: e reads %v, want 8 HBM3 and 8 PCIe, either one alone", got)
	}
	if got := read(build(0), 16); got[famHBM3] != 0 || got[famPCIe] != 0 {
		t.Errorf("16 H100 promised: e reads %v, want 0 of each", got)
	}
	// e's pool took the 8 HBM3 it read.
	c := build(8)
	if got := read(c, 8); got[famHBM3] != 0 || got[famPCIe] != 0 {
		t.Errorf("8 H100 promised, 8 HBM3 taken: e reads %v, want 0 of each", got)
	}
	if !FitsGPUBudget(c, "a", map[string]int{"H100": 8}) {
		t.Errorf("8 HBM3 taken: the 8 H100 promised to a no longer fit")
	}
}

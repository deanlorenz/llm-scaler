package allocation

import (
	"fmt"
	"testing"
)

// A namespace allowance that lists a family key (H100) under a cluster quota
// that lists only products of it. The cluster bound on the family key is the
// sum of those products, whichever GPU names the cycle's workloads declare. It
// used to be 0 unless some workload named the bare family, so the same
// configuration planned differently from cycle to cycle.

// familyUnderProducts is a cluster quota of two 8-GPU products of H100 and a
// namespace quota that lists only the family: unlimited in x, 12 in y, and
// nothing in w.
func familyUnderProducts(t *testing.T) []*ResourceConstraints {
	t.Helper()
	return []*ResourceConstraints{
		famClusterQuota(t, map[string]int{famHBM3: 8, famPCIe: 8}),
		famNamespaceQuota(t, map[string]map[string]int{"x": {"H100": -1}, "y": {"H100": 12}, "w": {}}),
	}
}

func TestNamespaceFamilyKeyBoundIgnoresDeclaredNames(t *testing.T) {
	c := familyUnderProducts(t)
	for _, declared := range [][]string{nil, {famHBM3}, {famPCIe}, {"H100"}, {famHBM3, famPCIe}, {famHBM3, "H100"}, {famSXM5}} {
		for ns, want := range map[string]int{"x": 16, "y": 12} {
			budgets, closed := gpuBudgets(c, ns, declared...)
			if !closed || budgets["H100"] != want {
				t.Errorf("%s, declared %v: budgets = %v (closed %v), want H100: %d", ns, declared, budgets, closed, want)
			}
		}
	}
}

func TestNamespaceFamilyKeyFitsWholeNodesOfClusterProducts(t *testing.T) {
	c := familyUnderProducts(t)
	for _, tc := range []struct {
		ns     string
		demand map[string]int
		fits   bool
	}{
		{"x", map[string]int{famHBM3: 8}, true},
		{"x", map[string]int{famPCIe: 8}, true},
		{"x", map[string]int{famHBM3: 8, famPCIe: 8}, true},
		{"x", map[string]int{famHBM3: 16}, false}, // the product's own 8
		{"x", map[string]int{"H100": 16}, true},
		{"x", map[string]int{"H100": 24}, false},
		{"x", map[string]int{famSXM5: 8}, false}, // a product the cluster quota does not list
		{"y", map[string]int{famHBM3: 8}, true},
		{"y", map[string]int{famHBM3: 8, famPCIe: 8}, false}, // the namespace's 12
		{"y", map[string]int{"H100": 16}, false},
	} {
		if got := FitsGPUBudget(c, tc.ns, tc.demand); got != tc.fits {
			t.Errorf("%s %v: fits = %v, want %v", tc.ns, tc.demand, got, tc.fits)
		}
	}
}

// The optimizer sizes 8-GPU replicas alike whether or not another workload in
// the cycle names the bare family. w is allowed nothing, so its model only
// adds the name.
func TestNamespaceFamilyKeyPlansAlikeWhateverNamesTheCycleHolds(t *testing.T) {
	for _, withBareFamily := range []bool{false, true} {
		name := fmt.Sprintf("bare H100 declared=%v", withBareFamily)
		reqs := []ModelScalingRequest{
			famWanting("x", "mh", famHBM3, 3, 8),
			famWanting("x", "mp", famPCIe, 3, 8),
			famWanting("y", "yh", famHBM3, 3, 8),
		}
		if withBareFamily {
			reqs = append(reqs, famWanting("w", "mw", "H100", 1, 8))
		}
		got := famTargets(familyUnderProducts(t), reqs...)
		if got["mw-v"] != 0 {
			t.Errorf("%s: w took %d replicas with no allowance, want 0", name, got["mw-v"])
		}
		if got["mh-v"]+got["yh-v"] != 1 || got["mp-v"] != 1 {
			t.Errorf("%s: x HBM3=%d x PCIe=%d y HBM3=%d, want one HBM3 replica (8 of 8) and one PCIe replica (8 of 8)",
				name, got["mh-v"], got["mp-v"], got["yh-v"])
		}
	}
}

// The warm pool's headroom names no workload at all, so it read every such key
// as 0. A product key under a cluster quota that lists its family and a sibling
// product reads the family's figure, as a workload naming it does.
func TestNamespaceKeyHeadroomReadsTheClusterBound(t *testing.T) {
	got := namespaceHeadroom(familyUnderProducts(t))
	if got["x"]["H100"] != 16 || got["y"]["H100"] != 12 {
		t.Errorf("family key under products: headroom = %v, want x H100: 16, y H100: 12", got)
	}

	c := []*ResourceConstraints{
		famClusterQuota(t, map[string]int{"H100": 16, famPCIe: 8}),
		famNamespaceQuota(t, map[string]map[string]int{"x": {famHBM3: 8}}),
	}
	got = namespaceHeadroom(c)
	if got["x"][famHBM3] != 8 {
		t.Errorf("product key under a family and a sibling: headroom = %v, want x %s: 8", got, famHBM3)
	}
	if !FitsGPUBudget(c, "x", map[string]int{famHBM3: 8}) {
		t.Errorf("x %s: 8 refused, want fits (the family's 16, the namespace's 8)", famHBM3)
	}
}

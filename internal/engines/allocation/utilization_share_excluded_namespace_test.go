package allocation

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
)

// A namespace-scoped quota that excludes a namespace bounds that namespace not
// at all (quota-limiter.md, "exclude"). Its pools are the sum of the caps of
// the namespaces it lists, net of only their holdings, so a cluster group
// planned over them gave the excluded namespace the other tenants' free quota
// plus whatever it already held: a budget of 12, 24 and 36 as it held 2, 14
// and 26, and an idle model there was filled toward its maxReplicas.
var _ = Describe("BuildShareGroups: a namespace a namespace quota excludes", func() {
	namespaceQuota := config.QuotaLimiterConfig{
		Name: "tenants", Type: "quota", Scope: config.QuotaScopeNamespace,
		Exclude:         []string{"ns-x"},
		NamespaceQuotas: map[string]map[string]int{"team-a": {"A100": 8}, "team-b": {"A100": 6}},
	}
	constraintsOf := func(byNS map[string]map[string]int, entries ...config.QuotaLimiterConfig) []*ResourceConstraints {
		byType := map[string]int{}
		for _, perType := range byNS {
			for acc, n := range perType {
				byType[acc] += n
			}
		}
		out := make([]*ResourceConstraints, 0, len(entries))
		for _, q := range entries {
			rc, err := NewDefaultLimiter(q.Name, NewQuotaInventory(q)).ComputeConstraints(context.Background(), byType, byNS)
			Expect(err).NotTo(HaveOccurred())
			out = append(out, rc)
		}
		return out
	}
	// build returns the groups by scope ("" for the cluster group) and the
	// models reported unshared, as "namespace/model: why".
	build := func(reqs []ModelScalingRequest, cons []*ResourceConstraints, opts ShareGroupOptions) (map[string]ShareGroup, []string) {
		var unshared []string
		opts.Unshared = func(req ModelScalingRequest, why string) {
			unshared = append(unshared, modelKey(req)+": "+why)
		}
		groups := map[string]ShareGroup{}
		for _, g := range BuildShareGroups(reqs, cons, opts) {
			groups[g.Scope] = g
		}
		return groups, unshared
	}
	fleet := func(held int) []ModelScalingRequest {
		return []ModelScalingRequest{
			shareReq("ma", "team-a", 1000, 2),
			shareReq("mb", "team-b", 1000, 2),
			shareReq("mx", "ns-x", 100, held), // nearly idle: a need of 0.125 GPU
		}
	}
	excluded := []string{"ns-x/mx: " + UnsharedExcludedNamespace}

	It("forms no cluster group without a cluster quota, however much the namespace holds", func() {
		for _, held := range []int{2, 14, 26} {
			cons := constraintsOf(map[string]map[string]int{
				"team-a": {"A100": 2}, "team-b": {"A100": 2}, "ns-x": {"A100": held},
			}, namespaceQuota)
			// ClusterIsQuota as the engine used to pass it for any quota entry:
			// the namespace quota's sum must not be read as a cluster budget.
			for _, opts := range []ShareGroupOptions{{ClusterIsQuota: true}, {}} {
				groups, unshared := build(fleet(held), cons, opts)
				Expect(groups).NotTo(HaveKey(""), "held=%d: a cluster group over the other tenants' quota", held)
				Expect(unshared).To(Equal(excluded), "held=%d", held)
				// The tenants keep their own groups and their own budgets.
				Expect(groups).To(HaveKey("team-a"))
				Expect(groups).To(HaveKey("team-b"))
				Expect(groups["team-a"].Budget).To(Equal(8), "held=%d: team-a's free 6 + its 2", held)
				Expect(groups["team-b"].Budget).To(Equal(6), "held=%d: team-b's free 4 + its 2", held)
				// Nothing bounds a namespace group's fill but its own budget:
				// the tenants' summed quota is not the cluster's free GPUs.
				Expect(groups["team-a"].PhysicalFree).To(BeNumerically(">", 1<<30))
			}
		}
	})

	It("forms no cluster group when the excluded namespace is the only one active", func() {
		// No active namespace is capped, so the namespace quota carries no
		// NamespacePools; its Pools are still only the tenants' summed caps.
		cons := constraintsOf(map[string]map[string]int{"ns-x": {"A100": 3}}, namespaceQuota)
		Expect(cons[0].NamespacePools).To(BeEmpty())
		Expect(cons[0].NamespaceScoped).To(BeTrue())
		groups, unshared := build([]ModelScalingRequest{shareReq("mx", "ns-x", 100, 3)}, cons, ShareGroupOptions{ClusterIsQuota: true})
		Expect(groups).To(BeEmpty())
		Expect(unshared).To(Equal(excluded))
	})

	It("plans it in the cluster group over the cluster quota alone when one is declared", func() {
		clusterQuota := config.QuotaLimiterConfig{
			Name: "cluster", Type: "quota", Scope: config.QuotaScopeCluster,
			ClusterQuotas: map[string]int{"A100": 20},
		}
		for _, c := range []struct {
			held, budget, idle int
		}{
			// 20 less the tenants' 4 is 16 for ns-x however much it holds;
			// past that it keeps what it holds and fills nothing.
			{held: 2, budget: 16, idle: 14},
			{held: 14, budget: 16, idle: 2},
			{held: 26, budget: 26, idle: 0},
		} {
			cons := constraintsOf(map[string]map[string]int{
				"team-a": {"A100": 2}, "team-b": {"A100": 2}, "ns-x": {"A100": c.held},
			}, namespaceQuota, clusterQuota)
			groups, unshared := build(fleet(c.held), cons, ShareGroupOptions{ClusterIsQuota: true})
			Expect(unshared).To(BeEmpty())
			Expect(groups).To(HaveKey(""))
			g := groups[""]
			Expect(g.Roles).To(HaveLen(1))
			Expect(g.Roles[0].Key).To(Equal("ns-x/mx/both"))
			// Not min(cluster free, the tenants' free 10): the tenants' quota
			// does not bound a namespace it excludes.
			Expect(g.Budget).To(Equal(c.budget), "held=%d", c.held)
			Expect(g.Budget-g.Committed["ns-x/mx/both"]).To(Equal(c.idle), "held=%d", c.held)
			Expect(g.PhysicalFree).To(Equal(c.idle), "held=%d: the cluster quota's free GPUs", c.held)
		}
	})

	It("forms no physical group in quota mode: the namespace quota's sum is not the inventory", func() {
		cons := constraintsOf(map[string]map[string]int{
			"team-a": {"A100": 2}, "team-b": {"A100": 2}, "ns-x": {"A100": 2},
		}, namespaceQuota)
		groups, unshared := build(fleet(2), cons, ShareGroupOptions{PhysicalGroups: true})
		Expect(groups).NotTo(HaveKey(""))
		Expect(unshared).To(Equal(excluded))
	})

	It("still plans every namespace in a physical group in inventory mode", func() {
		cons := []*ResourceConstraints{{Pools: map[string]ResourcePool{"A100": {Limit: 16, Used: 6}}}}
		groups, unshared := build(fleet(2), cons, ShareGroupOptions{PhysicalGroups: true})
		Expect(unshared).To(BeEmpty())
		Expect(groups).To(HaveKey(""))
		Expect(groups[""].Roles).To(HaveLen(3))
		Expect(groups[""].Budget).To(Equal(10 + 6))
	})
})

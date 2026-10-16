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
	// node is a model whose one replica takes a whole 8-GPU node.
	node := func(id, ns string, replicas int) ModelScalingRequest {
		req := shareReq(id, ns, 9000, replicas)
		req.VariantStates[0].GPUsPerReplica = 8
		return req
	}
	excluded := []string{"ns-x/mx: " + UnsharedExcludedNamespace("A100")}
	// expectNotPlanned checks that no group plans a role or a variant of the
	// excluded namespace, not only that the cluster group is absent.
	expectNotPlanned := func(groups map[string]ShareGroup) {
		GinkgoHelper()
		for scope, g := range groups {
			for _, r := range g.Roles {
				Expect(r.Key).NotTo(HavePrefix("ns-x/"), "group %q plans %s", scope, r.Key)
			}
			for k := range g.Variants {
				Expect(k).NotTo(HavePrefix("ns-x/"), "group %q lists variants of %s", scope, k)
			}
		}
	}

	It("forms no cluster group without a cluster quota, however much the namespace holds", func() {
		for _, held := range []int{2, 14, 26} {
			cons := constraintsOf(map[string]map[string]int{
				"team-a": {"A100": 2}, "team-b": {"A100": 2}, "ns-x": {"A100": held},
			}, namespaceQuota)
			// ClusterIsQuota as the engine used to pass it for any quota entry:
			// the namespace quota's sum must not be read as a cluster budget.
			for _, opts := range []ShareGroupOptions{{ClusterIsQuota: true}, {PhysicalGroups: true}, {}} {
				groups, unshared := build(fleet(held), cons, opts)
				Expect(groups).NotTo(HaveKey(""), "held=%d: a cluster group over the other tenants' quota", held)
				expectNotPlanned(groups)
				Expect(unshared).To(Equal(excluded), "held=%d", held)
				// The tenants keep their own groups and their own budgets.
				Expect(groups).To(HaveKey("team-a"))
				Expect(groups).To(HaveKey("team-b"))
				Expect(groups["team-a"].Budget).To(Equal(8), "held=%d: team-a's free 6 + its 2", held)
				Expect(groups["team-b"].Budget).To(Equal(6), "held=%d: team-b's free 4 + its 2", held)
				// Nothing bounds a namespace group's fill but its own budget:
				// the tenants' summed quota is not the cluster's free GPUs.
				Expect(groups["team-a"].ClusterFree).To(BeNumerically(">", 1<<30))
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

	It("says which accelerator type has no finite cluster quota", func() {
		Expect(UnsharedExcludedNamespace("H100")).To(Equal(
			"excluded from the namespace quota, and no finite scope: cluster quota covers H100; add one to share it"))
	})

	It("asks nothing more of an unshared model: no exclusion check, no weight", func() {
		cons := constraintsOf(map[string]map[string]int{
			"team-a": {"A100": 2}, "team-b": {"A100": 2}, "ns-x": {"A100": 2},
		}, namespaceQuota)
		var asked []string
		opts := ShareGroupOptions{
			ClusterIsQuota: true,
			Exclude: func(req ModelScalingRequest, _ bool) string {
				asked = append(asked, "exclude "+modelKey(req))
				return ""
			},
			Weight: func(req ModelScalingRequest, _ bool) float64 {
				asked = append(asked, "weight "+modelKey(req))
				return 1
			},
		}
		groups, unshared := build(fleet(2), cons, opts)
		Expect(unshared).To(Equal(excluded))
		expectNotPlanned(groups)
		Expect(asked).NotTo(ContainElement(HaveSuffix("ns-x/mx")))
		Expect(asked).To(ContainElement("exclude team-a/ma"))
	})

	It("does not share it under a cluster quota of -1 for its type: an unlimited quota is no finite bound", func() {
		unlimited := config.QuotaLimiterConfig{
			Name: "cluster", Type: "quota", Scope: config.QuotaScopeCluster,
			ClusterQuotas: map[string]int{"A100": -1},
		}
		cons := constraintsOf(map[string]map[string]int{
			"team-a": {"A100": 2}, "team-b": {"A100": 2}, "ns-x": {"A100": 2},
		}, namespaceQuota, unlimited)
		groups, unshared := build(fleet(2), cons, ShareGroupOptions{ClusterIsQuota: true})
		Expect(groups).NotTo(HaveKey(""))
		expectNotPlanned(groups)
		Expect(unshared).To(Equal(excluded))
	})

	It("plans it in the cluster group over what the cluster quota leaves after the tenants' claims", func() {
		clusterQuota := config.QuotaLimiterConfig{
			Name: "cluster", Type: "quota", Scope: config.QuotaScopeCluster,
			ClusterQuotas: map[string]int{"A100": 20},
		}
		for _, c := range []struct {
			held, budget, idle int
		}{
			// The cluster quota's free 20-4-held, less what team-a (free 6)
			// and team-b (free 4) may still claim of it. Past that ns-x keeps
			// what it holds and fills nothing.
			{held: 2, budget: 2 + 4, idle: 4}, // 14 free; 14-(6+4) = 4
			{held: 8, budget: 8 + 0, idle: 0}, // 8 free; 8-(6+4) < 0
			{held: 14, budget: 14, idle: 0},   // 2 free
			{held: 26, budget: 26, idle: 0},   // over the quota: 0 free
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
			Expect(g.Budget).To(Equal(c.budget), "held=%d", c.held)
			Expect(g.Budget-g.Committed["ns-x/mx/both"]).To(Equal(c.idle), "held=%d", c.held)
			Expect(g.ClusterFree).To(Equal(c.idle), "held=%d: what the tenants leave of the cluster free", c.held)
			// The tenants keep their own budgets; their fills share the
			// cluster free with the cluster group's (splitClusterFree), so
			// the three together never fill more than it holds.
			clusterFree := max(0, 20-4-c.held)
			Expect(groups["team-a"].Budget).To(Equal(8))
			Expect(groups["team-b"].Budget).To(Equal(6))
			Expect(groups["team-a"].ClusterFree).To(BeNumerically("<=", clusterFree), "held=%d", c.held)
			Expect(g.ClusterFree+groups["team-a"].ClusterFree+groups["team-b"].ClusterFree).
				To(BeNumerically("<=", clusterFree), "held=%d", c.held)
		}
	})

	// Every group's idle fill is charged to the cluster quota, so the groups
	// together must not plan more than it has free. a6d6e1828 gave the cluster
	// group the cluster quota's whole free count beside the tenants' own:
	// 14 + 6 + 4 = 24 planned at a cluster quota of 20 with 14 free.
	It("plans no more idle fill across all groups than the cluster quota has free", func() {
		for _, limit := range []int{20, 100} {
			clusterQuota := config.QuotaLimiterConfig{
				Name: "cluster", Type: "quota", Scope: config.QuotaScopeCluster,
				ClusterQuotas: map[string]int{"A100": limit},
			}
			cons := constraintsOf(map[string]map[string]int{
				"team-a": {"A100": 2}, "team-b": {"A100": 2}, "ns-x": {"A100": 2},
			}, namespaceQuota, clusterQuota)
			reqs := []ModelScalingRequest{
				shareReq("ma", "team-a", 9000, 2), shareReq("mb", "team-b", 9000, 2), shareReq("mx", "ns-x", 9000, 2),
			}
			groups, unshared := build(reqs, cons, ShareGroupOptions{ClusterIsQuota: true})
			Expect(unshared).To(BeEmpty())
			Expect(groups).To(HaveLen(3))
			clusterFree := limit - 6
			idle := map[string]int{}
			total := 0
			for scope, g := range groups {
				committed := 0
				for _, n := range g.Committed {
					committed += n
				}
				idle[scope] = max(0, min(g.Budget-committed, g.ClusterFree))
				total += idle[scope]
			}
			Expect(total).To(BeNumerically("<=", clusterFree), "limit=%d: idle by group %v", limit, idle)
			// The tenants' own fill is untouched; the excluded namespace gets
			// the rest of the cluster free.
			Expect(idle).To(Equal(map[string]int{"team-a": 6, "team-b": 4, "": clusterFree - 10}), "limit=%d", limit)
		}
	})

	It("never budgets the cluster group below what it holds when the tenants claim more than is free", func() {
		// Whole 8-GPU nodes: the tenants may claim 16 + 8, the cluster has 8
		// free. The cluster group keeps its 16 GPUs, and fills nothing. The
		// one free node goes to one tenant whole (splitClusterFree), not half
		// to each, where neither half places a replica.
		cons := []*ResourceConstraints{
			{Pools: map[string]ResourcePool{"A100": {Limit: 48, Used: 40}}},
			{NamespaceScoped: true, NamespacePools: map[string]map[string]ResourcePool{
				"team-a": {"A100": {Limit: 24, Used: 8}},
				"team-b": {"A100": {Limit: 16, Used: 8}},
			}},
		}
		reqs := []ModelScalingRequest{node("ma", "team-a", 1), node("mb", "team-b", 1), node("mx", "ns-x", 2)}
		groups, _ := build(reqs, cons, ShareGroupOptions{ClusterIsQuota: true})
		Expect(groups).To(HaveKey(""))
		Expect(groups[""].Budget).To(Equal(16))
		Expect(groups[""].ClusterFree).To(Equal(0))
		Expect(groups["team-a"].ClusterFree).To(Equal(8))
		Expect(groups["team-b"].ClusterFree).To(Equal(0))
	})

	// A tenant claims its free quota in whole replicas only. With 8-GPU
	// replicas, team-a's 4 free GPUs place nothing; counting them against the
	// cluster free left one free node that no group could fill: idle 4 in the
	// cluster group and 4 in team-a's, and neither fits an 8-GPU replica.
	It("claims a tenant's free quota in whole replicas, so a free node stays usable", func() {
		for _, c := range []struct {
			teamALimit, clusterIdle, teamIdle int
		}{
			// 4 free: no whole replica, claims 0, and fills none of the
			// cluster free (splitClusterFree), which it could not place.
			{teamALimit: 12, clusterIdle: 8, teamIdle: 0},
			{teamALimit: 16, clusterIdle: 0, teamIdle: 8}, // 8 free: one replica, claims 8
		} {
			cons := []*ResourceConstraints{
				{Pools: map[string]ResourcePool{"A100": {Limit: 48, Used: 40}}},
				{NamespaceScoped: true, NamespacePools: map[string]map[string]ResourcePool{
					"team-a": {"A100": {Limit: c.teamALimit, Used: 8}},
				}},
			}
			reqs := []ModelScalingRequest{node("ma", "team-a", 1), node("mx", "ns-x", 1)}
			groups, unshared := build(reqs, cons, ShareGroupOptions{ClusterIsQuota: true})
			Expect(unshared).To(BeEmpty())
			idle := map[string]int{}
			for scope, g := range groups {
				committed := 0
				for _, n := range g.Committed {
					committed += n
				}
				idle[scope] = max(0, min(g.Budget-committed, g.ClusterFree))
			}
			Expect(idle).To(Equal(map[string]int{"": c.clusterIdle, "team-a": c.teamIdle}),
				"team-a limit %d", c.teamALimit)
		}
	})

	It("reports nothing unshared without a namespace quota", func() {
		reqs := []ModelScalingRequest{shareReq("mx", "ns-x", 100, 2)}
		for name, cons := range map[string][]*ResourceConstraints{
			// A cluster quota of -1: no finite bound, and no namespace quota.
			"unlimited cluster quota": {{Pools: map[string]ResourcePool{"A100": {Limit: -1}}}},
			// An inventory with no pool of the model's type.
			"inventory without the type": {{Pools: map[string]ResourcePool{"H100": {Limit: 8}}}},
		} {
			for _, opts := range []ShareGroupOptions{{ClusterIsQuota: true}, {PhysicalGroups: true}} {
				groups, unshared := build(reqs, cons, opts)
				Expect(unshared).To(BeEmpty(), name)
				Expect(groups).To(BeEmpty(), name)
			}
		}
	})

	It("forms no physical group in quota mode: the namespace quota's sum is not the inventory", func() {
		cons := constraintsOf(map[string]map[string]int{
			"team-a": {"A100": 2}, "team-b": {"A100": 2}, "ns-x": {"A100": 2},
		}, namespaceQuota)
		groups, unshared := build(fleet(2), cons, ShareGroupOptions{PhysicalGroups: true})
		Expect(groups).NotTo(HaveKey(""))
		expectNotPlanned(groups)
		Expect(unshared).To(Equal(excluded))
	})

	// NamespaceResourcePools documents that a namespace-scoped inventory
	// returns a map; one that carries NamespacePools without setting
	// NamespaceScoped (a third-party inventory, or an older one) is still no
	// budget over every namespace.
	It("reads a provider with namespace pools as namespace-scoped, whether or not it says so", func() {
		cluster := &ResourceConstraints{Pools: map[string]ResourcePool{"A100": {Limit: 20, Used: 6}}}
		unflagged := &ResourceConstraints{
			Pools:          map[string]ResourcePool{"A100": {Limit: 14, Used: 4}},
			NamespacePools: map[string]map[string]ResourcePool{"team-a": {"A100": {Limit: 8, Used: 2}}},
		}
		available, namespaced := clusterBudgets([]*ResourceConstraints{unflagged, cluster, nil})
		Expect(namespaced).To(BeTrue())
		Expect(available).To(Equal(map[string]int{"A100": 14}), "the cluster quota's free, not the namespace sum's 10")

		available, namespaced = clusterBudgets([]*ResourceConstraints{unflagged})
		Expect(namespaced).To(BeTrue())
		Expect(available).To(BeEmpty())
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

package allocation

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// A namespace that a namespace quota lists in `exclude` bypasses that quota
// (quota-limiter.md, "exclude"). Today's optimizer and the scale-from-zero
// check still bounded it by the quota's per-type pools, which are the other
// tenants' free quota: it took a capped tenant's own free GPUs, and its wakes
// were refused once the tenants had spent theirs (#169).
var _ = Describe("a namespace a namespace quota excludes", func() {
	tenants := config.QuotaLimiterConfig{
		Name: "tenants", Type: "quota", Scope: config.QuotaScopeNamespace,
		Exclude:         []string{"ns-x"},
		NamespaceQuotas: map[string]map[string]int{"team-a": {"A100": 8}, "team-b": {"A100": 6}},
	}
	clusterQuota := func(limit int) config.QuotaLimiterConfig {
		return config.QuotaLimiterConfig{
			Name: "cluster", Type: "quota", Scope: config.QuotaScopeCluster,
			ClusterQuotas: map[string]int{"A100": limit},
		}
	}
	// constraintsOf builds the constraints with the real limiter, fed what each
	// namespace holds, as the engine feeds a quota.
	constraintsOf := func(byNS map[string]map[string]int, entries ...config.QuotaLimiterConfig) []*ResourceConstraints {
		GinkgoHelper()
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
	// model holds `held` replicas of `gpus` GPUs each on A100 and wants `want`
	// more. Its one variant is named after the model.
	model := func(id, ns string, held, want, gpus int) ModelScalingRequest {
		const perReplica = 10000.0
		r := &satEntryFixture{
			ModelID: id, Namespace: ns, AnalyzedAt: time.Now(),
			TotalDemand:      float64(held+want) * perReplica,
			RequiredCapacity: float64(want) * perReplica,
			VariantCapacities: []vcFixture{
				{VariantName: id + "-v", AcceleratorName: "A100", Cost: 10, ReplicaCount: held, PerReplicaCapacity: perReplica},
			},
		}
		return withSatEntry(r, ModelScalingRequest{
			ModelID: id, Namespace: ns,
			VariantStates: []domain.VariantReplicaState{
				{VariantName: id + "-v", CurrentReplicas: held, GPUsPerReplica: gpus},
			},
		})
	}
	targets := func(o *GreedyByScoreOptimizer, reqs []ModelScalingRequest, cons []*ResourceConstraints) map[string]domain.VariantDecision {
		return decisionMap(o.Optimize(context.Background(), reqs, cons))
	}

	Describe("today's optimizer", func() {
		// team-a holds 1 of its 8, team-b all 6 of its 6, ns-x 1. The tenants'
		// per-type pool has 7 free, all of them team-a's.
		held := map[string]map[string]int{"team-a": {"A100": 1}, "team-b": {"A100": 6}, "ns-x": {"A100": 1}}

		It("gives a capped tenant its own free quota beside an excluded namespace", func() {
			cons := constraintsOf(held, tenants)
			alone := targets(NewGreedyByScoreOptimizer(), []ModelScalingRequest{model("ma", "team-a", 1, 7, 1)}, cons)
			Expect(alone["ma-v"].TargetReplicas).To(Equal(8))

			dm := targets(NewGreedyByScoreOptimizer(), []ModelScalingRequest{
				model("ma", "team-a", 1, 7, 1), model("mx", "ns-x", 1, 20, 1),
			}, cons)
			Expect(dm["ma-v"].TargetReplicas).To(Equal(8), "team-a's own 7 free GPUs")
			Expect(dm["mx-v"].TargetReplicas).To(Equal(21), "no quota bounds ns-x: it gets its demand")
			Expect(dm["mx-v"].WasLimited).To(BeFalse())
		})

		It("gives a tenant its free node beside an excluded namespace of whole-node replicas", func() {
			// 8-GPU replicas, one per node. team-a may hold 2 and holds 1;
			// team-b holds its 1. The tenants' pool has one node free, team-a's.
			wholeNode := config.QuotaLimiterConfig{
				Name: "tenants", Type: "quota", Scope: config.QuotaScopeNamespace,
				Exclude:         []string{"ns-x"},
				NamespaceQuotas: map[string]map[string]int{"team-a": {"A100": 16}, "team-b": {"A100": 8}},
			}
			cons := constraintsOf(map[string]map[string]int{
				"team-a": {"A100": 8}, "team-b": {"A100": 8}, "ns-x": {"A100": 16},
			}, wholeNode)
			dm := targets(NewGreedyByScoreOptimizer(), []ModelScalingRequest{
				model("ma", "team-a", 1, 1, 8), model("mx", "ns-x", 2, 3, 8),
			}, cons)
			Expect(dm["ma-v"].TargetReplicas).To(Equal(2), "team-a's free node")
			Expect(dm["mx-v"].TargetReplicas).To(Equal(5), "no quota bounds ns-x")
		})

		It("bounds an excluded namespace by a cluster quota, not by the tenants' quota", func() {
			// Cluster quota 100, 8 held: 92 free. The tenants' pool has 7.
			cons := constraintsOf(held, tenants, clusterQuota(100))
			dm := targets(NewGreedyByScoreOptimizer(), []ModelScalingRequest{
				model("ma", "team-a", 1, 7, 1), model("mx", "ns-x", 1, 20, 1),
			}, cons)
			Expect(dm["ma-v"].TargetReplicas).To(Equal(8))
			Expect(dm["mx-v"].TargetReplicas).To(Equal(21))

			// Alone and wanting more than the cluster quota has free, ns-x is
			// stopped by the cluster quota, and the decision names it.
			cons = constraintsOf(held, tenants, clusterQuota(58)) // 50 free
			dm = targets(NewGreedyByScoreOptimizer(), []ModelScalingRequest{model("mx", "ns-x", 1, 200, 1)}, cons)
			Expect(dm["mx-v"].TargetReplicas).To(Equal(51))
			Expect(dm["mx-v"].WasLimited).To(BeTrue())
			Expect(dm["mx-v"].LimitedBy).To(Equal("cluster"))
		})

		It("never spends more than a cluster quota has free across tenants and the excluded namespace", func() {
			for _, c := range []struct {
				name             string
				quota            config.QuotaLimiterConfig
				byNS             map[string]map[string]int
				limit, gpus      int
				teamWant, nsWant int
				teamCap          int
			}{
				{
					name: "1-GPU replicas, 4 free", quota: tenants, limit: 12, gpus: 1, teamWant: 7, nsWant: 20, teamCap: 8,
					byNS: held,
				},
				{
					// One 8-GPU node free in the cluster quota; both want it.
					name: "whole-node replicas, one node free", limit: 40, gpus: 8, teamWant: 1, nsWant: 3, teamCap: 2,
					quota: config.QuotaLimiterConfig{
						Name: "tenants", Type: "quota", Scope: config.QuotaScopeNamespace,
						Exclude:         []string{"ns-x"},
						NamespaceQuotas: map[string]map[string]int{"team-a": {"A100": 16}, "team-b": {"A100": 8}},
					},
					byNS: map[string]map[string]int{"team-a": {"A100": 8}, "team-b": {"A100": 8}, "ns-x": {"A100": 16}},
				},
			} {
				cons := constraintsOf(c.byNS, c.quota, clusterQuota(c.limit))
				teamHeld, nsHeld := c.byNS["team-a"]["A100"]/c.gpus, c.byNS["ns-x"]["A100"]/c.gpus
				// Both orders: whoever the fair share serves first, the other
				// must see what it took.
				for _, reqs := range [][]ModelScalingRequest{
					{model("ma", "team-a", teamHeld, c.teamWant, c.gpus), model("mx", "ns-x", nsHeld, c.nsWant, c.gpus)},
					{model("mx", "ns-x", nsHeld, c.nsWant, c.gpus), model("ma", "team-a", teamHeld, c.teamWant, c.gpus)},
				} {
					dm := targets(NewGreedyByScoreOptimizer(), reqs, cons)
					total := (dm["ma-v"].TargetReplicas + dm["mx-v"].TargetReplicas + c.byNS["team-b"]["A100"]/c.gpus) * c.gpus
					Expect(total).To(BeNumerically("<=", c.limit), c.name)
					Expect(dm["ma-v"].TargetReplicas).To(BeNumerically("<=", c.teamCap), c.name)
					Expect(total).To(Equal(c.limit), "%s: the cluster quota's free GPUs are used", c.name)
				}
			}
		})

		It("does not rescale an excluded namespace over the tenants' quota", func() {
			// Rescale on for the cluster group. The excluded namespace's two
			// models want 16 together; without a cluster quota nothing bounds
			// them, so there is nothing to rescale and each gets its demand.
			o := NewGreedyByScoreOptimizer()
			o.Rescale = RescaleFlags{Cluster: true}
			cons := constraintsOf(map[string]map[string]int{
				"team-a": {"A100": 1}, "team-b": {"A100": 6}, "ns-x": {"A100": 2},
			}, tenants)
			dm := targets(o, []ModelScalingRequest{model("mx", "ns-x", 1, 7, 1), model("my", "ns-x", 1, 7, 1)}, cons)
			Expect(dm["mx-v"].TargetReplicas).To(Equal(8))
			Expect(dm["my-v"].TargetReplicas).To(Equal(8))
		})

		It("still caps a listed tenant by its own entry, and denies a namespace with no entry", func() {
			cons := constraintsOf(map[string]map[string]int{
				"team-a": {"A100": 1}, "team-b": {"A100": 6}, "ns-x": {"A100": 1}, "team-c": {},
			}, tenants)
			dm := targets(NewGreedyByScoreOptimizer(), []ModelScalingRequest{
				model("ma", "team-a", 1, 20, 1), model("mb", "team-b", 6, 5, 1),
				model("mc", "team-c", 0, 5, 1), model("mx", "ns-x", 1, 20, 1),
			}, cons)
			Expect(dm["ma-v"].TargetReplicas).To(Equal(8), "team-a's own quota")
			Expect(dm["mb-v"].TargetReplicas).To(Equal(6), "team-b is full")
			Expect(dm["mc-v"].TargetReplicas).To(Equal(0), "no entry and no default: denied")
			Expect(dm["mx-v"].TargetReplicas).To(Equal(21))
		})
	})

	Describe("the scale-from-zero capacity check", func() {
		// team-a and team-b have spent their quota; ns-x holds nothing.
		full := map[string]map[string]int{"team-a": {"A100": 8}, "team-b": {"A100": 6}, "ns-x": {}}

		It("wakes an excluded namespace when the tenants have spent their quota", func() {
			cons := constraintsOf(full, tenants)
			Expect(FitsGPUBudget(cons, "ns-x", map[string]int{"A100": 1})).To(BeTrue())
			Expect(FitsGPUBudget(cons, "ns-x", map[string]int{"A100": 8})).To(BeTrue(), "a whole node")
			Expect(FitsGPUBudget(cons, "ns-x", map[string]int{constants.DefaultAcceleratorName: 8})).To(BeTrue())

			budgets, nsScoped := GPUBudgets(cons, "ns-x")
			Expect(nsScoped).To(BeFalse())
			Expect(budgets).To(Equal(map[string]int{"A100": -1}), "unbounded, not 0")
		})

		It("bounds an excluded namespace's wake by a cluster quota alone", func() {
			// 22 - 14 = 8 free: one whole node.
			cons := constraintsOf(full, tenants, clusterQuota(22))
			Expect(FitsGPUBudget(cons, "ns-x", map[string]int{"A100": 8})).To(BeTrue())
			Expect(FitsGPUBudget(cons, "ns-x", map[string]int{"A100": 9})).To(BeFalse())
			budgets, nsScoped := GPUBudgets(cons, "ns-x")
			Expect(nsScoped).To(BeFalse())
			Expect(budgets).To(Equal(map[string]int{"A100": 8}))

			cons = constraintsOf(full, tenants, clusterQuota(14))
			Expect(FitsGPUBudget(cons, "ns-x", map[string]int{"A100": 1})).To(BeFalse(), "the cluster quota is full")
		})

		It("still refuses a capped tenant past its quota, and a namespace with no entry", func() {
			cons := constraintsOf(map[string]map[string]int{
				"team-a": {"A100": 8}, "team-b": {"A100": 6}, "ns-x": {}, "team-c": {},
			}, tenants)
			Expect(FitsGPUBudget(cons, "team-a", map[string]int{"A100": 1})).To(BeFalse())
			Expect(FitsGPUBudget(cons, "team-c", map[string]int{"A100": 1})).To(BeFalse())
		})
	})
})

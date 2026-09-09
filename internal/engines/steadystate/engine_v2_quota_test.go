package steadystate

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
)

// gpuUsageViews assembles both measures of usage, and each carries an obligation.
//
// A namespace-scoped quota is materialised only for namespaces PRESENT in the
// per-namespace usage map (DefaultLimiter.ComputeConstraints treats its keys as
// the active set). Discovery only sees namespaces that have a pod holding a GPU
// right now — so a namespace whose fleet is parked, or which is being optimized
// before anything starts, would drop out and lose its cap entirely, judged
// instead against the cluster aggregate.
//
// And the two measures must not be confused: the physical view counts every GPU
// on the nodes, the managed view only what WVA's own variants hold, and a quota
// fed the former binds on consumption it does not govern.
var _ = Describe("gpuUsageViews", func() {
	BeforeEach(func() { decision.DefaultGPUUsage.Reset() })
	AfterEach(func() { decision.DefaultGPUUsage.Reset() })

	// managedRequest is one variant holding replicas GPUs of the given type.
	// CompositeSignal.Result carries one VariantCapacity with a real decision
	// path (not C4-no-signal) so the request passes hasSaturationResult's
	// usable-signal gate (allocation.HasUsableCompositeSignal) exactly as a
	// real per-cycle composite would — an empty *domain.AnalyzerResult{} has
	// no capacity signal at all and is correctly excluded from the quota
	// charge.
	managedRequest := func(namespace, accelerator string, replicas int) allocation.ModelScalingRequest {
		return allocation.ModelScalingRequest{
			Namespace: namespace,
			CompositeSignal: allocation.NamedAnalyzerResult{
				Name: domain.SaturationAnalyzerName,
				Result: &domain.AnalyzerResult{
					VariantCapacities: []domain.VariantCapacity{
						{VariantName: "v", PerReplicaCapacity: 100, Reason: allocation.DecisionSingle},
					},
				},
			},
			Variants: []domain.VariantMetadata{
				{VariantName: "v", AcceleratorName: accelerator},
			},
			VariantStates: []domain.VariantReplicaState{
				{VariantName: "v", CurrentReplicas: replicas, GPUsPerReplica: 1},
			},
		}
	}

	It("reports the physical view as not-observed before anything is published", func() {
		views := gpuUsageViews(nil)
		Expect(views.Has(allocation.PhysicalUsage)).To(BeFalse(),
			"absent must stay distinguishable from zero usage")
	})

	It("always has a managed view, since the population is in hand", func() {
		views := gpuUsageViews(nil)
		Expect(views.Has(allocation.ManagedUsage)).To(BeTrue())
	})

	It("passes the discovered figures through untouched as the physical view", func() {
		decision.PublishGPUUsage(
			map[string]int{"A100": 4, "H100": 2},
			map[string]map[string]int{"team-a": {"A100": 4}},
		)
		views := gpuUsageViews([]allocation.ModelScalingRequest{{Namespace: "team-a"}})
		Expect(views.PhysicalByType).To(HaveKeyWithValue("A100", 4))
		Expect(views.PhysicalByType).To(HaveKeyWithValue("H100", 2),
			"usage WVA does not manage is still counted as physically held")
		Expect(views.PhysicalByNamespace["team-a"]).To(HaveKeyWithValue("A100", 4))
	})

	It("counts only WVA's own variants in the managed view", func() {
		// The cluster holds 6 GPUs; WVA placed 2 of them. A quota is an allowance
		// granted to WVA, so it may only ever be charged the 2.
		decision.PublishGPUUsage(
			map[string]int{"A100": 6},
			map[string]map[string]int{"team-a": {"A100": 6}},
		)
		views := gpuUsageViews([]allocation.ModelScalingRequest{managedRequest("team-a", "A100", 2)})
		Expect(views.PhysicalByType).To(HaveKeyWithValue("A100", 6))
		Expect(views.ManagedByType).To(HaveKeyWithValue("A100", 2))
		Expect(views.ManagedByNamespace["team-a"]).To(HaveKeyWithValue("A100", 2))
	})

	It("materialises a namespace being optimized that holds no GPUs, in both views", func() {
		decision.PublishGPUUsage(
			map[string]int{"A100": 4},
			map[string]map[string]int{"team-a": {"A100": 4}},
		)
		views := gpuUsageViews([]allocation.ModelScalingRequest{
			{Namespace: "team-a"}, {Namespace: "team-parked"},
		})
		Expect(views.PhysicalByNamespace).To(HaveKey("team-parked"),
			"a namespace with no GPU-holding pod must still be constrained by its quota")
		Expect(views.PhysicalByNamespace["team-parked"]).To(BeEmpty())
		Expect(views.ManagedByNamespace).To(HaveKey("team-parked"))
		Expect(views.ManagedByNamespace["team-parked"]).To(BeEmpty())
	})

	It("does not mutate the stored snapshot", func() {
		// Get documents its return as the shared copy. Materialising in place
		// would leak invented namespaces into every later reader, including the
		// scale-from-zero engine.
		decision.PublishGPUUsage(map[string]int{"A100": 4}, map[string]map[string]int{"team-a": {"A100": 4}})

		_ = gpuUsageViews([]allocation.ModelScalingRequest{{Namespace: "team-parked"}})

		snap, ok := decision.LatestGPUUsage()
		Expect(ok).To(BeTrue())
		Expect(snap.ByNamespace).ToNot(HaveKey("team-parked"),
			"the shared snapshot must be left exactly as the producer published it")
	})

	It("feeds a quota provider the managed view and a physical one the physical view", func() {
		// The whole point of the split, stated end to end: the same cycle hands
		// two providers two different figures.
		decision.PublishGPUUsage(map[string]int{"A100": 6}, map[string]map[string]int{"team-a": {"A100": 6}})
		views := gpuUsageViews([]allocation.ModelScalingRequest{managedRequest("team-a", "A100", 2)})

		quota := allocation.NewDefaultLimiter("q", allocation.NewQuotaInventory(config.QuotaLimiterConfig{
			Name: "q", Type: "quota", Scope: config.QuotaScopeCluster,
			ClusterQuotas: map[string]int{"A100": 4},
		}))
		byType, _ := views.For(quota)
		Expect(byType).To(HaveKeyWithValue("A100", 2), "a quota is charged only for what WVA holds")

		physical := allocation.NewDefaultLimiter("gpu", allocation.NewTypeInventory("gpu", nil))
		byType, _ = views.For(physical)
		Expect(byType).To(HaveKeyWithValue("A100", 6), "a physical inventory sees every GPU held")
	})
})

// hasSaturationResult (spec §5.1.3/A11') delegates to
// allocation.HasUsableCompositeSignal so the quota guard tests "is there a
// usable signal", never "is this specifically saturation" — the check the
// old implementation used, which the composite's rename (spec §8) breaks.
var _ = Describe("hasSaturationResult (quota guard, post-rename)", func() {
	requestWithComposite := func(composite allocation.NamedAnalyzerResult, replicas int) allocation.ModelScalingRequest {
		return allocation.ModelScalingRequest{
			Namespace:       "team-a",
			CompositeSignal: composite,
			Variants:        []domain.VariantMetadata{{VariantName: "v", AcceleratorName: "A100"}},
			VariantStates:   []domain.VariantReplicaState{{VariantName: "v", CurrentReplicas: replicas, GPUsPerReplica: 1}},
		}
	}

	informativeComposite := func(name string) allocation.NamedAnalyzerResult {
		return allocation.NamedAnalyzerResult{
			Name: name,
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "v", PerReplicaCapacity: 100, Reason: allocation.DecisionSingle},
				},
			},
		}
	}

	// test 14: renamed composite -> the guard still fires (charges the
	// quota) — the guard-not-silently-disabled test. Before the rename this
	// request would have been charged because Name == "saturation"; after
	// the rename Name == "CompositeSignal", and the OLD name-checking
	// hasSaturationResult would have wrongly excluded it, silently
	// disabling the quota guard it protects.
	It("still charges usage for a request whose composite is named CompositeSignal, not saturation (test 14)", func() {
		req := requestWithComposite(informativeComposite("CompositeSignal"), 3)
		usage := computeCurrentGPUUsage([]allocation.ModelScalingRequest{req})
		Expect(usage).To(HaveKeyWithValue("A100", 3))
	})

	// test 8d: no signal at all -> no autoscaling, and specifically here, no
	// quota charge for a request that was not usefully measured this cycle.
	// The composite's own Reason vocabulary is the decision path (C0-agree,
	// C1-single, C2-sat-fallback, C4-no-signal — spec §5.1.2), not an
	// analyzer's no-data/error/P0-store sentinels: a real composite with no
	// usable signal for an SO records allocation.DecisionNoSignal there.
	It("does not charge usage for a request whose composite carries no usable signal (test 8d)", func() {
		noSignal := allocation.NamedAnalyzerResult{
			Name: "CompositeSignal",
			Result: &domain.AnalyzerResult{
				VariantCapacities: []domain.VariantCapacity{{VariantName: "v", Reason: allocation.DecisionNoSignal}},
			},
		}
		req := requestWithComposite(noSignal, 3)
		usage := computeCurrentGPUUsage([]allocation.ModelScalingRequest{req})
		Expect(usage).ToNot(HaveKey("A100"))
	})

	It("does not charge usage for a request with a nil composite Result (absent — not measured this cycle)", func() {
		req := requestWithComposite(allocation.NamedAnalyzerResult{Name: "CompositeSignal", Result: nil}, 3)
		usage := computeCurrentGPUUsage([]allocation.ModelScalingRequest{req})
		Expect(usage).ToNot(HaveKey("A100"))
	})

	It("applies the same guard in the per-namespace view", func() {
		req := requestWithComposite(informativeComposite("CompositeSignal"), 2)
		usage := computeCurrentGPUUsageByNamespace([]allocation.ModelScalingRequest{req})
		Expect(usage["team-a"]).To(HaveKeyWithValue("A100", 2))
	})
})

var _ = Describe("gpuConstraintProviders", func() {
	clusterQuota := func(name string) *allocation.DefaultLimiter {
		inv := allocation.NewQuotaInventory(config.QuotaLimiterConfig{
			Name: name, Type: "quota", Scope: config.QuotaScopeCluster,
			ClusterQuotas: map[string]int{"A100": 4},
		})
		return allocation.NewDefaultLimiter(name, inv)
	}

	It("returns a DefaultLimiter (ConstraintProvider) as its own single provider", func() {
		dl := clusterQuota("q")
		got := gpuConstraintProviders(dl)
		Expect(got).To(HaveLen(1))
		Expect(got[0]).To(BeIdenticalTo(dl))
	})

	It("returns each ConstraintProvider constituent of a CompositeLimiter", func() {
		comp := allocation.NewCompositeLimiter("c", []allocation.Limiter{clusterQuota("a"), clusterQuota("b")})
		Expect(gpuConstraintProviders(comp)).To(HaveLen(2))
	})

	It("returns nil for a limiter that is not a ConstraintProvider (NoOpLimiter)", func() {
		Expect(gpuConstraintProviders(allocation.NewNoOpLimiter("noop"))).To(BeNil())
	})
})

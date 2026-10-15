package steadystate

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/locator"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
	llmdVariantAutoscalingV1alpha1 "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/variant"
)

// oneReplicaSource is a metrics source that reports one vLLM replica of the
// model, so a cycle gets past collection and reaches the scale-to-zero stage.
type oneReplicaSource struct {
	modelID, pod string
}

func (s oneReplicaSource) QueryList() *source.QueryList { return source.NewQueryList() }

func (s oneReplicaSource) Refresh(context.Context, source.RefreshSpec) (map[string]*source.MetricResult, error) {
	labels := map[string]string{"model_name": s.modelID, "pod": s.pod, "instance": "10.0.0.1:8000"}
	now := time.Now()
	sample := func(v float64) *source.MetricResult {
		return &source.MetricResult{Values: []source.MetricValue{{Labels: labels, Value: v, Timestamp: now}}}
	}
	return map[string]*source.MetricResult{
		"kv_cache_usage":        sample(0.1),
		"kv_usage_instant":      sample(0.1),
		"generation_token_rate": sample(10),
		"request_rate":          sample(0.1),
	}, nil
}

func (s oneReplicaSource) Get(string, map[string]string) *source.CachedValue { return nil }

// onePodLocator attributes every pod to one variant.
type onePodLocator struct{ variant string }

func (l onePodLocator) Locate(_ context.Context, namespace, _ string) (*locator.ManagedScaler, error) {
	return &locator.ManagedScaler{Namespace: namespace, Name: l.variant}, nil
}

func (l onePodLocator) ResolveScaleTarget(context.Context, string, string) (autoscalingv2.CrossVersionObjectReference, bool, error) {
	return autoscalingv2.CrossVersionObjectReference{}, false, nil
}

func (l onePodLocator) GetPodLabels(context.Context, string, string) map[string]string { return nil }

// The tiers a model's variants name must reach the scale-to-zero stage of a
// real cycle. scaleToZeroPolicy is correct on its own (scale_to_zero_tier_test.go),
// but optimizeV2 collects the tiers in stage 1 and reads them back in stage 3;
// dropping either half resolves the default entry alone, and every function-level
// test still passes.
func TestOptimizeV2ResolvesScaleToZeroFromTheVariantsTier(t *testing.T) {
	const (
		modelID   = "m"
		namespace = "chat"
		variant   = "chat-a"
	)
	on, off := true, false

	for _, tc := range []struct {
		name        string
		policies    map[string]config.ScalingPolicy
		tier        string
		wantBlocked bool
	}{
		{
			name: "default allows, the variant's tier forbids",
			policies: map[string]config.ScalingPolicy{
				config.GlobalDefaultsKey: {ScaleToZero: &config.ScaleToZeroEnvelope{Enabled: &on, RetentionPeriod: "1m"}},
				"interactive":            {ScaleToZero: &config.ScaleToZeroEnvelope{Enabled: &off}},
			},
			tier:        "interactive",
			wantBlocked: true,
		},
		{
			name: "default forbids, the variant's tier allows",
			policies: map[string]config.ScalingPolicy{
				config.GlobalDefaultsKey: {ScaleToZero: &config.ScaleToZeroEnvelope{Enabled: &off}},
				"batch":                  {ScaleToZero: &config.ScaleToZeroEnvelope{Enabled: &on, RetentionPeriod: "1m"}},
			},
			tier:        "batch",
			wantBlocked: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: variant, Namespace: namespace},
				Spec: appsv1.DeploymentSpec{
					Replicas: utils.Ptr(int32(1)),
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "server", Image: "vllm/vllm-openai:latest"}},
					}},
				},
				Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
			}
			k8s := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(deployment).Build()

			cfg := config.NewTestConfig()
			cfg.UpdateScalingPolicyConfigForNamespace(namespace, tc.policies)

			src := oneReplicaSource{modelID: modelID, pod: variant + "-0"}
			registry := source.NewSourceRegistry()
			registry.MustRegister("prometheus", src)
			e := NewEngine(k8s, k8s, clientgoscheme.Scheme, nil, registry, cfg, allocation.NewNoOpLimiter("test"))
			e.ReplicaMetricsCollector = collector.NewReplicaMetricsCollector(src, k8s, nil, nil, onePodLocator{variant: variant})
			e.ScaleToZeroEnforcer = allocation.NewEnforcer(
				func(context.Context, inferenceengine.Engine, string, string, time.Duration) (float64, error) {
					return 0, nil // idle
				},
				nil,
			)

			va := llmdVariantAutoscalingV1alpha1.VariantAutoscaling{
				ObjectMeta: metav1.ObjectMeta{Name: variant, Namespace: namespace},
				Spec: llmdVariantAutoscalingV1alpha1.VariantAutoscalingSpec{
					ModelID:        modelID,
					ScalingPolicy:  tc.tier,
					MaxReplicas:    4,
					ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: variant, APIVersion: "apps/v1"},
				},
			}
			decisions := e.optimizeV2(context.Background(), map[string][]llmdVariantAutoscalingV1alpha1.VariantAutoscaling{
				utils.GetNamespacedKey(namespace, modelID): {va},
			}, nil)

			ref, seen := e.lastBlockedModels[utils.GetNamespacedKey(namespace, modelID)]
			if !seen {
				t.Fatalf("the cycle never reached the scale-to-zero stage (decisions: %+v)", decisions)
			}
			blocked := ref.reasons == constants.ScalingBlockedPolicyForbidsZero
			if blocked != tc.wantBlocked {
				t.Errorf("blocked reasons = %q, want policy-forbids-zero = %v", ref.reasons, tc.wantBlocked)
			}
			if len(decisions) == 0 {
				t.Fatal("the cycle produced no decision to enforce on")
			}
			for _, d := range decisions {
				if parked := d.TargetReplicas == 0; parked == tc.wantBlocked {
					t.Errorf("variant %s: target %d, want parked = %v", d.VariantName, d.TargetReplicas, !tc.wantBlocked)
				}
			}
		})
	}
}

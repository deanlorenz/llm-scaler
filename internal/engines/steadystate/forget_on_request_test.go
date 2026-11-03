package steadystate

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/annotations"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/saturation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils/scaletarget"
	llmdVariantAutoscalingV1alpha1 "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/variant"
)

// TestBuildAnalyzerInput_ForgetsOneVariantOnRequest drives the real call site:
// the annotation on the scale target, read where the engine loads the
// variant's capacity record, acted on once per token.
func TestBuildAnalyzerInput_ForgetsOneVariantOnRequest(t *testing.T) {
	e, _ := engineConfigEngine(t)
	analyzer := saturation.NewSaturationAnalyzer(e.capacityStore)
	e.setSaturation(analyzer)

	va := &llmdVariantAutoscalingV1alpha1.VariantAutoscaling{
		ObjectMeta: metav1.ObjectMeta{Name: "v1", Namespace: "ns"},
		Spec: llmdVariantAutoscalingV1alpha1.VariantAutoscalingSpec{
			ModelID:        "m",
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "d"},
		},
	}
	deploy := deployWithArgs("--max-num-seqs=128")
	run := func(token string) {
		if token != "" {
			deploy.Annotations = map[string]string{annotations.ForgetLearnedState: token}
		}
		_, err := e.buildInputAndRunSaturation(
			context.Background(),
			config.ScalingPolicy{KvCacheThreshold: 0.8, QueueLengthThreshold: 5, AnalyzerName: "saturation",
				ScaleUpThreshold: 0.85, ScaleDownBoundary: 0.70},
			map[string]scaletarget.ScaleTargetAccessor{
				utils.GetNamespacedKey("ns", "d"): scaletarget.NewDeploymentAccessor(deploy),
			},
			map[string]*llmdVariantAutoscalingV1alpha1.VariantAutoscaling{"ns/v1": va},
		)
		require.NoError(t, err)
	}
	// learnLive files a measured live record, as a serving cycle does.
	learnLive := func() {
		rec := e.capacityStore.Get("ns", "m", "v1")
		require.NotNil(t, rec)
		e.capacityStore.Update("ns", "m", "v1", capacity.Record{
			EffectiveCapacity: 4000, TotalKvCapacityTokens: 16000,
			EngineParams: rec.EngineParams, LearnedFrom: capacity.LearnedFromLive,
		}, time.Now())
	}
	isLive := func() bool { return e.capacityStore.Get("ns", "m", "v1").LearnedFrom == capacity.LearnedFromLive }

	run("")
	learnLive()
	run("")
	require.True(t, isLive(), "precondition: with no annotation the live record stands")

	run("2026-10-15T12:00")
	assert.False(t, isLive(), "the annotation did not forget the variant's live capacity")
	assert.Equal(t, "2026-10-15T12:00", analyzer.ForgottenToken("ns", "v1"))

	learnLive()
	run("2026-10-15T12:00")
	assert.True(t, isLive(), "an annotation left in place forgot the variant again on the next cycle")

	run("2026-10-15T13:00")
	assert.False(t, isLive(), "a new token did not ask again")
}

// The forget reaches the slo analyzer too (issue #431), on the token the
// saturation analyzer acts on: once per token, and again for a new one.
// Before, the slo analyzer's ITL line kept pricing from the readings taken
// before the forget.
func TestForgetOnRequest_ReachesTheSLOAnalyzerOncePerToken(t *testing.T) {
	e, _ := engineConfigEngine(t)
	e.setSaturation(saturation.NewSaturationAnalyzer(e.capacityStore))
	spy := &sloKeeperSpy{}
	e.sloAnalyzer = spy
	deploy := deployWithArgs("--max-num-seqs=128")
	forget := func(token string) {
		deploy.Annotations = map[string]string{annotations.ForgetLearnedState: token}
		e.forgetOnRequest(context.Background(), "ns", "m", "v1", scaletarget.NewDeploymentAccessor(deploy))
	}

	e.forgetOnRequest(context.Background(), "ns", "m", "v1", scaletarget.NewDeploymentAccessor(deployWithArgs()))
	assert.Empty(t, spy.forgotten, "control: no annotation, nothing forgotten")

	forget("t1")
	assert.Equal(t, []string{"ns/m/v1"}, spy.forgotten, "the annotation did not reach the slo analyzer")
	forget("t1")
	assert.Len(t, spy.forgotten, 1, "a token left in place forgot the slo line again")
	forget("t2")
	assert.Equal(t, []string{"ns/m/v1", "ns/m/v1"}, spy.forgotten, "a new token did not ask the slo analyzer again")
}

// An engine with no slo analyzer (a hand-built one; NewEngine always builds
// it) still forgets in the saturation analyzer, and does not reach for the
// missing one.
func TestForgetOnRequest_NoSLOAnalyzerStillForgetsInSaturation(t *testing.T) {
	e, _ := engineConfigEngine(t)
	sat := saturation.NewSaturationAnalyzer(e.capacityStore)
	e.setSaturation(sat)
	require.Nil(t, e.sloAnalyzer, "premise: the engine has no slo analyzer")
	deploy := deployWithArgs()
	deploy.Annotations = map[string]string{annotations.ForgetLearnedState: "t1"}
	require.NotPanics(t, func() {
		e.forgetOnRequest(context.Background(), "ns", "m", "v1", scaletarget.NewDeploymentAccessor(deploy))
	})
	assert.Equal(t, "t1", sat.ForgottenToken("ns", "v1"), "the saturation analyzer acted on the token")
}

// With no saturation analyzer to keep the token, nothing is forgotten in the
// slo analyzer either: it would forget again on every cycle.
func TestForgetOnRequest_NoSaturationForgetterForgetsNothingInSLO(t *testing.T) {
	e, _ := engineConfigEngine(t)
	e.setSaturation(plainAnalyzer{})
	spy := &sloKeeperSpy{}
	e.sloAnalyzer = spy
	deploy := deployWithArgs()
	deploy.Annotations = map[string]string{annotations.ForgetLearnedState: "t1"}
	e.forgetOnRequest(context.Background(), "ns", "m", "v1", scaletarget.NewDeploymentAccessor(deploy))
	assert.Empty(t, spy.forgotten)
}

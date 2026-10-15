package capacity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
)

func vllmContainer(command []string, env ...corev1.EnvVar) corev1.Container {
	return corev1.Container{Name: "vllm", Image: "vllm/vllm-openai:v0.11.0", Command: command, Env: env}
}

// A wide-EP P/D decode engine: every flag that decides what one replica holds
// and that the fields before version 3 did not describe.
func TestVLLMParsesTheWideEPAndPDFlags(t *testing.T) {
	p := ParseEngineArgs(inferenceengine.EngineVLLM, deploymentWithContainers(vllmContainer([]string{
		"vllm", "serve", "deepseek-ai/DeepSeek-V3",
		"--revision", "r1",
		"--data-parallel-size", "8",
		"--pipeline-parallel-size=2",
		"--enable-expert-parallel",
		"--speculative-config", `{"num_speculative_tokens": 1, "method": "mtp"}`,
		"--kv-transfer-config", `{"kv_connector":"NixlConnector","kv_role":"kv_both"}`,
		"--kv-offloading-size", "64",
		"--kv-offloading-backend", testOffloadBackend,
		"--no-enable-prefix-caching",
	})))

	require.True(t, p.Complete(), "unresolved: %v", p.Unresolved)
	assert.False(t, p.ModelUnresolved)
	assert.Equal(t, 8, p.DataParallelSize)
	assert.Equal(t, 2, p.PipelineParallelSize)
	assert.True(t, p.ExpertParallel)
	assert.Equal(t, `{"method":"mtp","num_speculative_tokens":1}`, p.SpeculativeConfig,
		"re-encoded with sorted keys and no spacing")
	assert.Equal(t, testKVConnector, p.KVConnector)
	assert.Equal(t, 64.0, p.KVOffloadingSize)
	assert.Equal(t, testOffloadBackend, p.KVOffloadingBackend)
	assert.True(t, p.PrefixCachingDisabled)
	assert.Equal(t, "deepseek-ai/DeepSeek-V3", p.ModelPath)
	assert.Equal(t, "r1", p.ModelRevision)
}

// The defaults are the engine's: unset parallel sizes are 1, prefix caching
// is on, and no feature is named.
func TestVLLMDefaultsForTheNewFields(t *testing.T) {
	p := ParseEngineArgs(inferenceengine.EngineVLLM, deploymentWithContainers(vllmContainer(
		[]string{"vllm", "serve", "m"})))
	assert.Equal(t, 1, p.DataParallelSize)
	assert.Equal(t, 1, p.PipelineParallelSize)
	assert.False(t, p.ExpertParallel)
	assert.False(t, p.PrefixCachingDisabled)
	assert.Empty(t, p.SpeculativeConfig)
	assert.Empty(t, p.KVConnector)
	assert.Equal(t, "m", p.ModelPath)
}

// Key order and spacing in a JSON flag are formatting, not configuration.
func TestSpeculativeConfigFormattingDoesNotSplitTheKey(t *testing.T) {
	parse := func(spec string) EngineParams {
		return ParseEngineArgs(inferenceengine.EngineVLLM, deploymentWithContainers(vllmContainer(
			[]string{"vllm", "serve", "m", "--speculative-config", spec})))
	}
	a := parse(`{"method":"mtp","num_speculative_tokens":1}`)
	b := parse(`{ "num_speculative_tokens": 1,  "method": "mtp" }`)
	c := parse(`{"method":"mtp","num_speculative_tokens":2}`)
	assert.Equal(t, a.Fingerprint(), b.Fingerprint())
	assert.NotEqual(t, a.Fingerprint(), c.Fingerprint())
}

// A config that is not JSON still tells two configurations apart, and does
// not make the read incomplete.
func TestKVTransferConfigThatIsNotJSONIsKeptWhole(t *testing.T) {
	p := ParseEngineArgs(inferenceengine.EngineVLLM, deploymentWithContainers(vllmContainer(
		[]string{"vllm", "serve", "m", "--kv-transfer-config", "{kv_connector: Nixl}"})))
	assert.True(t, p.Complete())
	assert.Equal(t, "{kv_connector:Nixl}", p.KVConnector)
}

// A new flag whose value cannot be read is a gap like any other: recorded,
// so the configuration is not used as an equality.
func TestAnUnreadableParallelSizeMakesTheReadIncomplete(t *testing.T) {
	p := ParseEngineArgs(inferenceengine.EngineVLLM, deploymentWithContainers(vllmContainer(
		[]string{"vllm", "serve", "m", "--data-parallel-size", "$DP_SIZE"})))
	assert.False(t, p.Complete())
	assert.Contains(t, p.Unresolved, "data_parallel_size")
	assert.Equal(t, 1, p.DataParallelSize, "the default stands in")
}

// An unread checkpoint is NOT an incomplete read: that would withhold the
// variant's own restore. It is marked on its own field.
func TestAnUnreadModelIsMarkedButDoesNotMakeTheReadIncomplete(t *testing.T) {
	for name, cmd := range map[string][]string{
		"positional": {"vllm", "serve", "$MODEL"},
		"flag":       {"python", "-m", "vllm.entrypoints.openai.api_server", "--model", "$MODEL"},
	} {
		t.Run(name, func(t *testing.T) {
			p := ParseEngineArgs(inferenceengine.EngineVLLM, deploymentWithContainers(vllmContainer(cmd)))
			assert.True(t, p.ModelUnresolved)
			assert.True(t, p.Complete(), "unresolved: %v", p.Unresolved)
			assert.Empty(t, p.ModelPath)
		})
	}

	resolved := ParseEngineArgs(inferenceengine.EngineVLLM, deploymentWithContainers(vllmContainer(
		[]string{"vllm", "serve", "$MODEL"}, corev1.EnvVar{Name: "MODEL", Value: "org/m"})))
	assert.False(t, resolved.ModelUnresolved)
	assert.Equal(t, "org/m", resolved.ModelPath)
}

func TestSGLangParsesTheWideEPAndPDFlags(t *testing.T) {
	p := ParseEngineArgs(inferenceengine.EngineSGLang, deploymentWithContainers(corev1.Container{
		Name: "sglang", Image: "lmsysorg/sglang:v0.5", Command: []string{
			"python", "-m", "sglang.launch_server",
			"--model-path", "org/m", "--revision", "r2",
			"--dp-size", "4", "--pp-size", "2", "--ep-size", "8",
			"--speculative-num-steps", "3", "--speculative-algorithm", "EAGLE",
			"--disaggregation-transfer-backend", "mooncake",
			"--enable-hierarchical-cache", "--hicache-size", "32",
			"--disable-radix-cache",
		}}))

	require.True(t, p.Complete(), "unresolved: %v", p.Unresolved)
	assert.Equal(t, 4, p.DataParallelSize)
	assert.Equal(t, 2, p.PipelineParallelSize)
	assert.True(t, p.ExpertParallel)
	assert.Equal(t, "algorithm=EAGLE;num_steps=3", p.SpeculativeConfig, "sorted, whatever the flag order")
	assert.Equal(t, "mooncake", p.KVConnector)
	assert.Equal(t, "hierarchical", p.KVOffloadingBackend)
	assert.Equal(t, 32.0, p.KVOffloadingSize)
	assert.True(t, p.PrefixCachingDisabled)
	assert.Equal(t, "org/m", p.ModelPath)
	assert.Equal(t, "r2", p.ModelRevision)
}

// A wide-EP flag of the other engine is the wrong-parser signal, as every
// mapped value key is.
func TestTheVLLMParserOverSGLangParallelFlagsIsIncomplete(t *testing.T) {
	p := ParseEngineArgs(inferenceengine.EngineVLLM, deploymentWithContainers(vllmContainer(
		[]string{"vllm", "serve", "m", "--dp-size", "4"})))
	assert.False(t, p.Complete())
	assert.Contains(t, p.Unresolved, "dp_size")
}

// A configuration built without the parallel sizes is the same configuration
// as one parsed from flags that do not set them.
func TestUnsetParallelSizesCompareAsOne(t *testing.T) {
	parsed := ParseEngineArgs(inferenceengine.EngineVLLM, deploymentWithContainers(vllmContainer(
		[]string{"vllm", "serve", "m"})))
	built := parsed
	built.DataParallelSize, built.PipelineParallelSize = 0, 0
	assert.True(t, parsed.IsCapacityCompatible(&built))
	assert.Equal(t, parsed.Fingerprint(), built.Fingerprint())
}

// The KV total measured under one layout or checkpoint does not stand as
// another's k1.
func TestTheKVTotalDoesNotCarryAcrossALayoutOrCheckpointChange(t *testing.T) {
	base := defaultEngineParams()
	resolveEffectiveMaxBatchedTokens(&base)
	require.True(t, kvTotalCarries(base, base))
	for name, mutate := range map[string]func(*EngineParams){
		"data parallel":     func(p *EngineParams) { p.DataParallelSize = 8 },
		"pipeline parallel": func(p *EngineParams) { p.PipelineParallelSize = 2 },
		"expert parallel":   func(p *EngineParams) { p.ExpertParallel = true },
		"speculative":       func(p *EngineParams) { p.SpeculativeConfig = testSpeculativeConfig },
		"checkpoint":        func(p *EngineParams) { p.ModelPath = "org/m-FP8" },
		"revision":          func(p *EngineParams) { p.ModelRevision = "r2" },
	} {
		t.Run(name, func(t *testing.T) {
			cur := base
			mutate(&cur)
			assert.False(t, kvTotalCarries(base, cur))
		})
	}
}

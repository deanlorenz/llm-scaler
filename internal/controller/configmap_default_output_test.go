package controller

import (
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
)

// The ConfigMap body is the whole path: yaml.Unmarshal is NOT strict here, so a
// field the struct does not know is dropped without an error and the entry still
// parses, logs, and validates. A green deployment therefore says nothing about
// whether `defaultOutputTokens` arrived -- these specs are what says it.
var _ = Describe("defaultOutputTokens through the ConfigMap path", func() {

	// Trimmed from the entry a run is staged with: the comment block is kept
	// because comments are where a silently-ignored key usually hides.
	const body = `analyzers:
  - name: saturation
    score: 1.0
scaleUpThreshold: 0.85
scaleDownBoundary: 0.70
kvCacheThreshold: 0.80
queueLengthThreshold: 5

# Price a request waiting in the router queue at this generation length while
# no replica has measured one of its own.
defaultOutputTokens: 6000
`

	It("reaches the stored entry and the resolved policy", func() {
		configs, count := parseScalingPolicyConfig(map[string]string{"default": body}, logr.Discard())
		Expect(count).To(Equal(1))
		Expect(configs["default"].DefaultOutputTokens).To(Equal(6000),
			"the yaml key must map to the field; a typo in the tag reads as 0 with no error")

		resolved := config.ResolveScalingPolicyForTier(configs, "Qwen/Qwen3-0.6B", "ns", "")
		Expect(resolved.DefaultOutputTokens).To(Equal(6000),
			"resolution starts from the default entry, so the figure must survive it")
		Expect(resolved.ExpectedOutputTokens(0, 0, 512)).To(Equal(6000.0),
			"and must then win over the built-in net, which is what the analyzer asks")
	})

	It("is absent as a zero, not as a guess, when the entry omits it", func() {
		short := "analyzers:\n  - name: saturation\n    score: 1.0\nkvCacheThreshold: 0.80\n"
		configs, count := parseScalingPolicyConfig(map[string]string{"default": short}, logr.Discard())
		Expect(count).To(Equal(1))

		resolved := config.ResolveScalingPolicyForTier(configs, "Qwen/Qwen3-0.6B", "ns", "")
		Expect(resolved.DefaultOutputTokens).To(BeZero())
		Expect(resolved.ExpectedOutputTokens(0, 0, 512)).To(Equal(512.0),
			"an unset field hands the decision to the built-in net")
	})

	It("lets a per-model override name its own generation length", func() {
		data := map[string]string{
			"default":    body,
			"chatty#ns":  "model_id: chatty\nnamespace: ns\ndefaultOutputTokens: 250\n",
			"silent2#ns": "model_id: silent2\nnamespace: ns\nkvCacheThreshold: 0.75\n",
		}
		configs, count := parseScalingPolicyConfig(data, logr.Discard())
		Expect(count).To(Equal(3))

		Expect(config.ResolveScalingPolicyForTier(configs, "chatty", "ns", "").DefaultOutputTokens).
			To(Equal(250), "the override wins over the default entry")
		Expect(config.ResolveScalingPolicyForTier(configs, "silent2", "ns", "").DefaultOutputTokens).
			To(Equal(6000), "an override that says nothing about it inherits the default entry")
	})
})

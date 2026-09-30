package saturation_v2

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
)

var _ = Describe("derivePrefillMu", func() {
	// The model speaks tokens per second, which is the honest unit for
	// prefill; the demand floor divides by a REQUEST rate. This conversion is
	// the whole boundary between them, so an error here mis-sizes the fleet by
	// exactly the prompt length -- three orders of magnitude on this trace.
	It("converts a token velocity into a request rate at the prompt length", func() {
		// 140,000 tokens/s over 30,000-token prompts is 4.67 req/s.
		m := prefill.Model{A: 1.0 / 140000.0, B: 0.05}
		d := derivePrefillMu(m, 30000)
		Expect(d.ok).To(BeTrue())
		Expect(d.rate).To(BeNumerically("~", 140000.0/30000.0, 1e-9))
		Expect(d.tokenSec).To(BeNumerically("~", 140000.0, 1e-6))
	})

	It("gives a HIGHER request rate for shorter prompts at the same velocity", func() {
		// The property that makes the unit worth having: the same replica
		// serves more short requests than long ones, and a request-denominated
		// capacity cannot express that.
		m := prefill.Model{A: 1.0 / 140000.0, B: 0.05}
		Expect(derivePrefillMu(m, 1000).rate).
			To(BeNumerically(">", derivePrefillMu(m, 30000).rate))
	})

	It("declines an unfitted model rather than reporting an infinite rate", func() {
		Expect(derivePrefillMu(prefill.Model{}, 30000).ok).To(BeFalse())
	})

	It("declines a non-positive prompt length", func() {
		m := prefill.Model{A: 1.0 / 140000.0, B: 0.05}
		Expect(derivePrefillMu(m, 0).ok).To(BeFalse())
		Expect(derivePrefillMu(m, -1).ok).To(BeFalse())
	})

	It("declines an inverted fit, which would otherwise be a negative capacity", func() {
		Expect(derivePrefillMu(prefill.Model{A: -1e-5, B: 0.05}, 30000).ok).To(BeFalse())
	})
})

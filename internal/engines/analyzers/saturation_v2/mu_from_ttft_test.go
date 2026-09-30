package saturation_v2

import (
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
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

var _ = Describe("notePrefill while decode is saturated", func() {
	// The lesson computeK2 already paid for, applied to the new window: with
	// decode full, a prefill replica's resident KV is tokens held for a
	// transfer decode cannot accept and its queue is requests decode will not
	// admit. A (T, TTFT) pair taken then measures blocked admission, not the
	// cost of computing another prompt token, and the window holds only 20
	// points -- so a sustained episode could replace the whole fit basis.
	newAnalyzer := func() *SaturationAnalyzer {
		return NewSaturationAnalyzer(capacity.NewStore())
	}
	replica := func() domain.ReplicaMetrics {
		return domain.ReplicaMetrics{
			VariantName: "p", PodName: "p-1", Ready: true,
			TokensInUse: 400000, QueueLength: 20, AvgTTFT: 12.0,
		}
	}

	It("records nothing while decode is saturated", func() {
		a := newAnalyzer()
		for i := 0; i < 30; i++ {
			a.notePrefill("k", []domain.ReplicaMetrics{replica()}, "p", 30000, 0,
				true, time.Now(), logr.Discard())
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		Expect(a.prefillWindows["k"].Len()).To(Equal(0),
			"decode-saturated readings must not enter the fit")
	})

	It("records once decode is no longer saturated", func() {
		a := newAnalyzer()
		a.notePrefill("k", []domain.ReplicaMetrics{replica()}, "p", 30000, 0,
			false, time.Now(), logr.Discard())
		a.mu.Lock()
		defer a.mu.Unlock()
		Expect(a.prefillWindows["k"].Len()).To(Equal(1))
	})

	It("discounts resident tokens for the prefix cache, as it does the queue", func() {
		// Half the prompt cached halves both halves of the backlog, so the
		// regressor must come out at half the undiscounted figure. Asymmetry
		// here flattens the slope and inflates the velocity, which under-orders.
		a := newAnalyzer()
		a.notePrefill("k", []domain.ReplicaMetrics{replica()}, "p", 15000, 0.5,
			false, time.Now(), logr.Discard())
		a.mu.Lock()
		defer a.mu.Unlock()
		obs := a.prefillWindows["k"].Observations()
		Expect(obs).To(HaveLen(1))
		// 400000*(1-0.5) + 20*15000 = 200000 + 300000
		Expect(obs[0].Tokens).To(BeNumerically("~", 500000, 1e-6))
	})
})

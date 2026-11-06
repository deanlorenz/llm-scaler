package whatif

import "testing"

// The callers' stop sits inside the model's bound, so a spread clamped to it
// is priced and never refused.
func TestPromptCVStop_IsInsideTheModelsBound(t *testing.T) {
	stop, bound := float64(PromptCVStop), float64(maxPromptCV)
	if stop >= bound {
		t.Errorf("PromptCVStop %g, want below the model's bound %g", stop, bound)
	}
}

package saturation

import (
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// A restore reports how many k2 windows the document held for the variant,
// so a variant that never measured one of its own -- it borrowed, or never
// saturated -- is not reported as having had none recent enough.
func TestARestoreCountsTheWindowsTheDocumentHeld(t *testing.T) {
	restore := func(windows map[string]capacity.RollingAverageSnapshot) RestoredVariant {
		a, _, _ := waProcess(mtParams(64, "vllm:v1"))
		// A variant with an entry of its own -- its liveness -- and no k2
		// window, as a variant that only borrowed is exported.
		seen := a.itlWindowKey("test-ns", "test-model", "variant-a", "H100", 1)
		a.RestoreLearnedState(LearnedState{
			Config:        map[string]string{"test-ns/variant-a": mtParams(256, "vllm:v1").Fingerprint()},
			Args:          map[string]string{"test-ns/variant-a": mtParams(256, "vllm:v1").ArgsFingerprint()},
			K2:            windows,
			VariantSeenAt: map[string]time.Time{seen: time.Now()},
		})
		r, done := a.installRestored("test-ns", "test-model", "variant-a")
		if !done {
			t.Fatal("precondition: the variant's entries were pending")
		}
		return r
	}
	if r := restore(nil); r.Stored != 0 || r.Outcome != RestoredPrior {
		t.Errorf("a document with no window for the variant: want stored 0 and a prior outcome, got %+v", r)
	}
	a, _, _ := waProcess(mtParams(64, "vllm:v1"))
	key := rrKey(a, "variant-a")
	if r := restore(map[string]capacity.RollingAverageSnapshot{key: rrWindow(4000).Snapshot()}); r.Stored != 1 {
		t.Errorf("a document with one window for the variant: want stored 1, got %+v", r)
	}
}

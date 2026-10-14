package saturation

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// A model with every variant at zero replicas skips analysis, so nothing in
// Analyze stamps it. NoteVariantsPresent is the stamp the engine gives it
// instead; these specs pin what that keeps and what it does not.
var _ = Describe("a model parked at zero keeps what it learned", func() {
	const (
		ns    = "ns"
		model = "m"
		accel = "NVIDIA-H200"
	)
	var (
		a   *SaturationAnalyzer
		now time.Time
	)
	timeout := capacity.DefaultHorizons().VariantTimeout

	BeforeEach(func() {
		a = NewSaturationAnalyzer(capacity.NewStore())
		now = time.Date(2026, 10, 14, 18, 0, 0, 0, time.UTC)
		a.now = func() time.Time { return now }
	})

	// learn gives a variant the two fitted figures a wake depends on, under
	// the key Analyze builds for it, stamped as Analyze stamps it.
	learn := func(variant, accelerator string) string {
		key := a.itlWindowKey(ns, model, variant, accelerator, 1)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.noteVariantSeen(key, now)
		a.itlBaseline[key] = 0.012
		a.startSeconds[key] = 70
		return key
	}
	present := func(variant, accelerator string) domain.VariantReplicaState {
		return domain.VariantReplicaState{VariantName: variant, AcceleratorName: accelerator, GPUsPerReplica: 1}
	}
	// parkFor runs the engine's order for a skipped model -- sweep, then the
	// stamp -- in steps well under the timeout, for longer than it in total.
	parkFor := func(total time.Duration, states ...domain.VariantReplicaState) {
		step := timeout / 4
		for elapsed := time.Duration(0); elapsed < total; elapsed += step {
			now = now.Add(step)
			a.EvictStaleHistory()
			a.NoteVariantsPresent(ns, model, states)
		}
	}

	It("keeps a parked variant's ITL baseline and start estimate past the variant timeout", func() {
		parked := learn("parked", accel)
		deleted := learn("deleted", accel)

		parkFor(2*timeout, present("parked", accel))

		Expect(a.itlBaseline).To(HaveKey(parked), "the parked variant woke on the default ITL baseline")
		Expect(a.startSeconds).To(HaveKey(parked), "the parked variant lost its start estimate")
		// The control in the same run: the sweep does evict, so the case
		// above is kept by the stamp and not by a sweep that never fires.
		Expect(a.itlBaseline).NotTo(HaveKey(deleted), "a variant no longer discovered must still age out")
		Expect(a.startSeconds).NotTo(HaveKey(deleted))
	})

	It("stamps the key the state was learned under when the accelerator no longer resolves", func() {
		// Parked, the variant has no Pods to observe its accelerator from,
		// so discovery reports it unresolved. The memo from when it ran is
		// what Analyze would key it by on wake, and the stamp must hit that
		// key rather than a fresh one nobody reads.
		parked := learn("parked", accel)

		parkFor(2*timeout, present("parked", ""))

		Expect(a.itlBaseline).To(HaveKey(parked))
		Expect(a.itlWindowKey(ns, model, "parked", "", 1)).To(Equal(parked),
			"the accelerator memo itself must survive the park, or the wake builds another key")
	})
})

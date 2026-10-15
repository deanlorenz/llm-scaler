package itl

import (
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

var _ = Describe("Tracker", func() {
	var (
		t   *Tracker
		now time.Time
	)
	BeforeEach(func() {
		t = NewTracker()
		now = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	})
	row := func(pod string, k, itl float64) domain.ReplicaMetrics {
		return domain.ReplicaMetrics{PodName: pod, VariantName: "v", Ready: true, KvUsageInstant: k, AvgITL: itl}
	}
	// onLine is n readings on ITL = 0.02*k + 0.01, k spread from lo to hi.
	onLine := func(n int, lo, hi float64) []domain.ReplicaMetrics {
		rows := make([]domain.ReplicaMetrics, n)
		for i := range n {
			k := lo + (hi-lo)*float64(i)/float64(n-1)
			rows[i] = row("p", k, 0.02*k+0.01)
		}
		return rows
	}

	It("admits a row only when it is a reading, and counts each reason it turned one away", func() {
		r := t.Observe("key", "v", []domain.ReplicaMetrics{
			row("ok", 0.5, 0.02),
			{PodName: "other", VariantName: "w", Ready: true, KvUsageInstant: 0.5, AvgITL: 0.02},
			{PodName: "starting", VariantName: "v", KvUsageInstant: 0.5, AvgITL: 0.02},
			{PodName: "pool", VariantName: "v", Ready: true, FromWarmPool: true, KvUsageInstant: 0.5, AvgITL: 0.02},
			row("no-itl", 0.5, 0),
			row("inf-itl", 0.5, math.Inf(1)),
			row("no-k", 0, 0.02),
			row("above", 0.9, 0.02),
			row("below", 0.05, 0.02),
		}, now)
		Expect(r.Considered).To(Equal(8), "another variant's row is not considered at all")
		Expect(r.NotReady).To(Equal(2))
		Expect(r.NoITL).To(Equal(2), "an infinite ITL is not a reading either")
		Expect(r.NoK).To(Equal(1))
		Expect(r.AboveBand).To(Equal(1))
		Expect(r.Offered).To(Equal(2), "the reading below the band is offered")
		Expect(r.Held).To(Equal(1), "and dropped by the window itself")
		Expect(r.Tier).To(BeEmpty())
	})

	It("fits the full line once the window spans enough of k, and remembers its B", func() {
		r := t.Observe("key", "v", onLine(10, 0.2, 0.7), now)
		Expect(r.Ready).To(BeTrue())
		Expect(r.Tier).To(Equal(TierOLS))
		Expect(r.Model.A).To(BeNumerically("~", 0.02, 1e-9))
		Expect(r.Model.B).To(BeNumerically("~", 0.01, 1e-9))
		b, ok := t.Baseline("key")
		Expect(ok).To(BeTrue())
		Expect(b).To(BeNumerically("~", 0.01, 1e-9))
	})

	It("does not remember a B that is not a physical floor", func() {
		rows := make([]domain.ReplicaMetrics, 10)
		for i := range rows {
			k := 0.2 + 0.05*float64(i)
			rows[i] = row("p", k, 0.05*k-0.005) // B < 0, positive at saturation
		}
		r := t.Observe("key", "v", rows, now)
		Expect(r.Tier).To(Equal(TierOLS))
		Expect(r.Model.B).To(BeNumerically("<", 0))
		_, ok := t.Baseline("key")
		Expect(ok).To(BeFalse())
	})

	It("pins B to the key's own last full fit when the spread is short", func() {
		t.Observe("key", "v", onLine(10, 0.2, 0.7), now)
		r := t.Observe("key", "v", onLine(10, 0.40, 0.45), now.Add(time.Hour))
		Expect(r.Ready).To(BeFalse(), "the old readings aged out; the new ones span 0.05")
		Expect(r.Tier).To(Equal(TierPinnedB))
		Expect(r.BaselineStored).To(BeTrue())
		Expect(r.BaselineLearned).To(BeTrue())
		Expect(r.Model.B).To(BeNumerically("~", 0.01, 1e-9))
	})

	It("pins B to the default before the key has a full fit of its own", func() {
		r := t.Observe("key", "v", onLine(10, 0.40, 0.45), now)
		Expect(r.Tier).To(Equal(TierPinnedB))
		Expect(r.BaselineStored).To(BeFalse())
		Expect(r.BaselineLearned).To(BeFalse())
		Expect(r.Model.B).To(Equal(DefaultBaselineSec))
	})

	It("treats a stored intercept that is not positive as no intercept", func() {
		t.SetBaseline("key", 0)
		r := t.Observe("key", "v", onLine(10, 0.40, 0.45), now)
		Expect(r.BaselineStored).To(BeTrue())
		Expect(r.BaselineLearned).To(BeFalse())
		Expect(r.Model.B).To(Equal(DefaultBaselineSec))
	})

	It("fits nothing from fewer than the minimum samples, spread or not", func() {
		r := t.Observe("key", "v", onLine(DefaultMinSamples-1, 0.40, 0.45), now)
		Expect(r.Tier).To(BeEmpty())
		Expect(r.Declined).To(BeFalse())
		Expect(r.Model.IsZero()).To(BeTrue())
	})

	It("declines when the pinned fit refuses the readings", func() {
		rows := make([]domain.ReplicaMetrics, 10)
		for i := range rows {
			rows[i] = row("p", 0.40+0.005*float64(i), DefaultBaselineSec/2) // below the floor: A < 0
		}
		r := t.Observe("key", "v", rows, now)
		Expect(r.Tier).To(BeEmpty())
		Expect(r.Declined).To(BeTrue())
		Expect(r.Model.IsZero()).To(BeTrue())
	})

	It("keeps each key's window and intercept apart", func() {
		t.Observe("a", "v", onLine(10, 0.2, 0.7), now)
		t.Observe("b", "v", onLine(3, 0.2, 0.7), now)
		Expect(t.WindowLens()).To(Equal(map[string]int{"a": 10, "b": 3}))
		Expect(t.Baselines()).To(HaveKey("a"))
		Expect(t.Baselines()).NotTo(HaveKey("b"))
	})

	It("sweeps what the caller calls stale, windows and intercepts apart", func() {
		t.Observe("live", "v", onLine(10, 0.2, 0.7), now)
		t.Observe("gone", "v", onLine(10, 0.2, 0.7), now)
		t.RestoreBaseline("restored-gone", 0.01) // an intercept with no window
		w, b := t.Sweep(now, func(key string) bool { return key != "live" })
		Expect(w).To(Equal(1))
		Expect(b).To(Equal(2))
		Expect(t.WindowLens()).To(HaveKey("live"))
		Expect(t.Baselines()).To(HaveKey("live"))
		Expect(t.Baselines()).To(HaveLen(1))
	})

	It("prunes aged readings on a sweep even from keys it keeps", func() {
		t.Observe("live", "v", onLine(10, 0.2, 0.7), now)
		t.Sweep(now.Add(DefaultObservationMaxAge+time.Minute), func(string) bool { return false })
		Expect(t.WindowLens()["live"]).To(BeZero())
	})

	It("restores an intercept only where none was learned since", func() {
		t.Observe("key", "v", onLine(10, 0.2, 0.7), now)
		t.RestoreBaseline("key", 0.5)
		t.RestoreBaseline("new", 0.5)
		b, _ := t.Baseline("key")
		Expect(b).To(BeNumerically("~", 0.01, 1e-9))
		b, _ = t.Baseline("new")
		Expect(b).To(Equal(0.5))
	})

	It("forgets intercepts and keeps the readings", func() {
		t.Observe("key", "v", onLine(10, 0.2, 0.7), now)
		t.ForgetBaselines()
		Expect(t.Baselines()).To(BeEmpty())
		Expect(t.WindowLens()["key"]).To(Equal(10))
	})
})

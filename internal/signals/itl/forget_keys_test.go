package itl

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learn"
)

var _ = Describe("Tracker.ForgetKeys", func() {
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	rows := []domain.ReplicaMetrics{{PodName: "p", VariantName: "v", Ready: true, KvCacheUsage: 0.5, KvUsageInstant: 0.5,
		AvgITL: 0.03, AvgITLKnown: true}}
	adm := Admission{MinK: DefaultMinObservableK, MaxK: DefaultMaxObservableK}

	It("forgets what match names, counts it, and reports each key sorted, by whom", func() {
		t := NewTracker()
		t.Observe("b", "v", rows, adm, at)
		t.Observe("a", "v", rows, adm, at)
		t.Observe("c", "v", rows, adm, at)
		t.SetBaseline("a", 0.01)
		t.RestoreSlope("d", Slope{A: 0.05, FittedAt: at})

		windows, baselines, forgotten := t.ForgetKeys(at, learn.ByEvicted, func(k string) bool { return k != "c" })
		Expect(windows).To(Equal(2))
		Expect(baselines).To(Equal(1))
		Expect(forgotten).To(Equal([]learn.Forgotten{
			{Key: "a", By: learn.ByEvicted},
			{Key: "b", By: learn.ByEvicted},
			{Key: "d", By: learn.ByEvicted}, // a held slope alone is still a key
		}))
		Expect(t.WindowLen("a")).To(BeZero())
		Expect(t.WindowLen("c")).To(Equal(1), "a key match does not name keeps its window")
		Expect(t.Slopes()).NotTo(HaveKey("d"))
	})

	It("reports nothing when nothing matches", func() {
		t := NewTracker()
		t.Observe("a", "v", rows, adm, at)
		windows, baselines, forgotten := t.ForgetKeys(at, learn.ByOperator, func(string) bool { return false })
		Expect(windows).To(BeZero())
		Expect(baselines).To(BeZero())
		Expect(forgotten).To(BeEmpty())
		Expect(t.WindowLen("a")).To(Equal(1))
	})
})

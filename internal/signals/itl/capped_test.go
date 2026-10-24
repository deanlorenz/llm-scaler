package itl

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

var _ = Describe("Tracker on a replica at its sequence cap", func() {
	const (
		cycle   = 15 * time.Second
		maxSeqs = 256
	)
	var (
		t   *Tracker
		now time.Time
	)
	BeforeEach(func() {
		t = NewTracker()
		now = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	})

	// The shape of issue #287, cycle by cycle: one decode replica took all the
	// traffic, at its max_num_seqs of 256 running with 768 waiting and k
	// between 0.86 and 0.89, reading ITL 73-85 ms, while three siblings sat
	// idle below the band.
	hot := func(i int) domain.ReplicaMetrics {
		ks := []float64{0.86, 0.87, 0.89, 0.88, 0.87}
		itls := []float64{0.073, 0.077, 0.085, 0.081, 0.078}
		return domain.ReplicaMetrics{
			PodName: "decode-hot", VariantName: "v", Ready: true,
			KvUsageInstant: ks[i%len(ks)], KvUsageInstantKnown: true, AvgITL: itls[i%len(itls)],
			RunningRequests: maxSeqs, QueueLength: 768,
			PreemptionRate: 0, PreemptionRateKnown: true,
		}
	}
	idle := func(n int) domain.ReplicaMetrics {
		return domain.ReplicaMetrics{
			PodName: fmt.Sprintf("decode-idle-%d", n), VariantName: "v", Ready: true,
			KvUsageInstant: 0.004, KvUsageInstantKnown: true, AvgITL: 0.012,
			RunningRequests: 1, PreemptionRateKnown: true,
		}
	}
	// replay runs 20 cycles -- five minutes -- of the shape, with change
	// applied to the hot replica's row, and returns the last cycle's reading.
	replay := func(seqs int64, change func(*domain.ReplicaMetrics)) Reading {
		var r Reading
		for i := range 20 {
			h := hot(i)
			if change != nil {
				change(&h)
			}
			rows := []domain.ReplicaMetrics{h, idle(0), idle(1), idle(2)}
			r = t.Observe("key", "v", rows, seqs, now)
			now = now.Add(cycle)
		}
		return r
	}

	It("fits a line from the capped replica's readings above the band", func() {
		r := replay(maxSeqs, nil)
		Expect(r.AboveBand).To(BeZero(), "a replica at its sequence cap is not thrashing")
		Expect(r.Capped).To(Equal(1))
		Expect(r.BelowBand).To(Equal(3), "the idle siblings still teach nothing")
		Expect(r.Tier).To(Equal(TierPinnedB), "the window must hold a line, or derived mu never exists")
		// One replica spans no k, so B is pinned and the line passes through
		// the readings: 79 ms at k 0.874, their means.
		Expect(r.Model.ITLAt(0.874)).To(BeNumerically("~", 0.079, 0.002))
	})

	It("counts the cap per rank", func() {
		r := replay(maxSeqs, func(h *domain.ReplicaMetrics) {
			h.EngineRanks = 2
			h.RunningRequests = 2 * maxSeqs
		})
		Expect(r.Tier).To(Equal(TierPinnedB))
		r = NewTracker().Observe("key", "v", []domain.ReplicaMetrics{{
			PodName: "dp", VariantName: "v", Ready: true, KvUsageInstant: 0.87, AvgITL: 0.08,
			EngineRanks: 2, RunningRequests: maxSeqs, PreemptionRateKnown: true,
		}}, maxSeqs, now)
		Expect(r.AboveBand).To(Equal(1), "one of two ranks at its cap is not the pod at its cap")
	})

	DescribeTable("keeps the band whenever the cap is not shown",
		func(seqs int64, change func(*domain.ReplicaMetrics)) {
			r := replay(seqs, change)
			Expect(r.AboveBand).To(Equal(1))
			Expect(r.Capped).To(BeZero())
			Expect(r.Tier).To(BeEmpty())
		},
		Entry("max_num_seqs unknown", int64(0), nil),
		Entry("the batch below the cap: memory, not the cap, binds", int64(maxSeqs),
			func(h *domain.ReplicaMetrics) { h.RunningRequests = maxSeqs - 1 }),
		Entry("the engine preempted in the ITL's minute", int64(maxSeqs),
			func(h *domain.ReplicaMetrics) { h.PreemptionRate = 0.05 }),
		Entry("no preemption reading", int64(maxSeqs),
			func(h *domain.ReplicaMetrics) { h.PreemptionRateKnown = false }),
	)
})

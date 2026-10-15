package kueue

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Review finding (security, Kueue maintainer): a lender counted as waiting
// whenever Kueue reported any pending Workload, so a CPU job, or one too
// large to admit, made the borrowers give back capacity for nothing; and the
// release was bounded by everything lent out, not by what the lender asked
// for.
var _ = Describe("Lender waiting from its pending Workloads", func() {
	cpu := PendingWorkload{GPUs: nil}
	amd := PendingWorkload{GPUs: map[string]int64{"amd.com/gpu": 2}}
	lender := func(pending ...PendingWorkload) QueueState {
		q := member("lender", QueueResource{Nominal: 8, Reserved: 3}) // 5 unused
		q.PendingWorkloads = pending
		return q
	}

	DescribeTable("what it waits for",
		func(q QueueState, want int) {
			Expect(lenderPending(q, gpuKey)).To(Equal(want))
			Expect(LenderWaiting(q, gpuKey)).To(Equal(want > 0))
		},
		Entry("a CPU-only Workload is not waiting for GPUs", lender(cpu), 0),
		Entry("another resource's Workload is not waiting for this one", lender(amd), 0),
		Entry("a Workload larger than the unused nominal would have to borrow itself", lender(pendingGPU(6, 0)), 0),
		Entry("the largest Workload that fits is what it waits for", lender(pendingGPU(2, 0), pendingGPU(5, 0), pendingGPU(9, 0)), 5),
	)

	p := func(n int32) *int32 { return &n }
	lenderOf := func(qs ...QueueState) LenderState {
		b := ComputeBorrowing(topology(qs...), false)
		Expect(b.Pools).To(HaveLen(1))
		for _, ls := range b.Pools[0].Lenders {
			if ls.Queue == "lender" {
				return ls
			}
		}
		Fail("no lender")
		return LenderState{}
	}
	borrower := member("a", QueueResource{Nominal: 0, Reserved: 4, Borrowed: 4})

	It("carries the lender's priority as read, for the reclaim judgement", func() {
		l := withReclaim(lender(pendingGPU(2, 1000), pendingGPU(9, 50)), ReclaimLowerPriority)
		l.LenderPriority = p(50)
		got := lenderOf(l, borrower)
		Expect(got).To(Equal(LenderState{Queue: "lender", Waiting: true, Reclaim: ReclaimLowerPriority,
			Unused: 5, Priority: p(50)}))
	})

	It("carries no priority when none of its Workloads was read", func() {
		Expect(lenderOf(withReclaim(lender(), ReclaimLowerPriority), borrower).Priority).To(BeNil())
	})
})

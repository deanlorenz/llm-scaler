package saturation_v2

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
)

// Prefill's floor is a quotient: the scheduler queue's charge divided by a mu
// that saturatedCompletionRate built as PrefillComputedTokenRate / ILeff. Both
// halves carry a (1 - prefixHitRate) factor, and the quotient is a replica
// count only while it is the SAME factor.
//
// It was not. The divisor takes fleetPrefixHitRate -- prefill replicas only,
// request-rate weighted -- and the charge took computeModelWorkloadAverages,
// which averages over every replica with token activity, decode included and
// unweighted. On a P/D fleet those diverge with the role ratio, and the
// direction is the one this whole file exists to prevent: mu inflated against
// the demand it is divided into, so prefill is under-ordered, worse the larger
// decode grows.
var _ = Describe("prefill's charge and prefill's divisor use one hit rate", func() {
	// One prefill replica reading 0.8, nine decode replicas reading 0.0: the
	// shape of the fleet the role attribution exists for.
	const (
		prefillHit = 0.8
		avgInput   = 10_000.0
		avgOutput  = 1_000.0
		queued     = 100
	)
	fleet := func() ([]domain.ReplicaMetrics, map[string]string) {
		out := []domain.ReplicaMetrics{{
			PodName: "p-0", VariantName: "p", Ready: true,
			TokensInUse: 1, TotalKvCapacityTokens: 100_000,
			AvgInputTokens: avgInput, AvgOutputTokens: 1,
			PrefixCacheHitRate: prefillHit, RequestRate: 4,
		}}
		for i := 0; i < 9; i++ {
			out = append(out, domain.ReplicaMetrics{
				PodName: "d-" + string(rune('0'+i)), VariantName: "d", Ready: true,
				TokensInUse: 1, TotalKvCapacityTokens: 100_000,
				AvgInputTokens: avgInput, AvgOutputTokens: avgOutput,
				PrefixCacheHitRate: 0, RequestRate: 4,
			})
		}
		roles := map[string]string{"p": domain.RolePrefill, "d": domain.RoleDecode}
		return out, roles
	}

	It("charges prefill at the rate its divisor was built from", func() {
		metrics, roles := fleet()
		// The figure the divisor is built from, read from the same helper
		// Analyze passes in -- not a constant retyped here.
		divisorHit := fleetPrefixHitRate(metrics, roles)
		Expect(divisorHit).To(BeNumerically("~", prefillHit, 1e-9),
			"the divisor reads PREFILL's rate, not the fleet mean")

		sq := &domain.SchedulerQueueMetrics{QueueSize: queued}
		activeRoles := map[string]bool{domain.RolePrefill: true, domain.RoleDecode: true}
		got := estimateSchedulerQueueDemand(sq, metrics, roles, activeRoles, divisorHit)

		// ILeff is what saturatedCompletionRate divides by, so the charge is
		// the queue's count at exactly that per-request figure.
		ileff := shape.New(avgInput, avgOutput, divisorHit).ILeff
		Expect(got.byRole[domain.RolePrefill]).To(BeNumerically("~", queued*ileff, 1e-6))
	})

	It("does not charge prefill at the fleet mean, which decode dominates", func() {
		// The negative control, and the measured size of the defect: with ten
		// replicas reading 0.8 once and 0.0 nine times the mean is 0.08, so the
		// charge would keep 92% of the prompt while the divisor kept 20% of it.
		metrics, roles := fleet()
		_, _, mean := computeModelWorkloadAverages(metrics, roles)
		Expect(mean).To(BeNumerically("~", 0.08, 1e-9))

		sq := &domain.SchedulerQueueMetrics{QueueSize: queued}
		activeRoles := map[string]bool{domain.RolePrefill: true, domain.RoleDecode: true}
		got := estimateSchedulerQueueDemand(sq, metrics, roles, activeRoles,
			fleetPrefixHitRate(metrics, roles))

		atMean := queued * avgInput * (1 - mean)
		Expect(got.byRole[domain.RolePrefill]).To(BeNumerically("<", atMean),
			"the mean would over-charge, which reads as under-capacity once mu divides it")
		// 0.92/0.20: the factor prefill was under-ordered by.
		Expect(atMean / got.byRole[domain.RolePrefill]).To(BeNumerically("~", 4.6, 0.01))
	})

	It("leaves the model total on the model-wide average", func() {
		// The control. Prefill's hit rate reaches prefill's charge and nothing
		// else: the total is the figure every other consumer already reads, and
		// it stays on the model-wide mean.
		//
		// Decode's charge is the queue's OUTPUT tokens and carries no hit rate
		// at all -- the discount never applied to output, and the prompt half
		// is prefill's charge now (see decode_queue_charge_test.go).
		metrics, roles := fleet()
		_, _, mean := computeModelWorkloadAverages(metrics, roles)
		sq := &domain.SchedulerQueueMetrics{QueueSize: queued}
		activeRoles := map[string]bool{domain.RolePrefill: true, domain.RoleDecode: true}
		got := estimateSchedulerQueueDemand(sq, metrics, roles, activeRoles,
			fleetPrefixHitRate(metrics, roles))

		wantIn := queued * avgInput * (1 - mean)
		wantOut := queued * avgOutput
		Expect(got.total).To(BeNumerically("~", wantIn+wantOut, 1e-6))
		Expect(got.byRole[domain.RoleDecode]).To(BeNumerically("~", wantOut, 1e-6))
	})

	It("takes no discount on either side when prefill publishes no rate", func() {
		// A fleet whose prefill replicas report nothing readable: both sides
		// fall back to 0 together, so the quotient is still a replica count.
		// Consistency is the invariant, not the value.
		metrics, roles := fleet()
		for i := range metrics {
			metrics[i].PrefixCacheHitRate = -1 // out of range, so unreadable
		}
		divisorHit := fleetPrefixHitRate(metrics, roles)
		Expect(divisorHit).To(Equal(0.0))

		sq := &domain.SchedulerQueueMetrics{QueueSize: queued}
		activeRoles := map[string]bool{domain.RolePrefill: true}
		got := estimateSchedulerQueueDemand(sq, metrics, roles, activeRoles, divisorHit)
		Expect(got.byRole[domain.RolePrefill]).To(BeNumerically("~", queued*avgInput, 1e-6))
	})

	It("clamps a rate out of range the way the divisor's shape does", func() {
		// shape.New clamps to [0,1]; so does the charge, or a caller handing in
		// 1.5 would make the charge negative while ILeff stayed at zero.
		metrics, roles := fleet()
		sq := &domain.SchedulerQueueMetrics{QueueSize: queued}
		activeRoles := map[string]bool{domain.RolePrefill: true}

		hi := estimateSchedulerQueueDemand(sq, metrics, roles, activeRoles, 1.5)
		Expect(hi.byRole[domain.RolePrefill]).To(Equal(0.0))
		Expect(shape.New(avgInput, avgOutput, 1.5).ILeff).To(Equal(0.0))

		lo := estimateSchedulerQueueDemand(sq, metrics, roles, activeRoles, -0.5)
		Expect(lo.byRole[domain.RolePrefill]).To(BeNumerically("~", queued*avgInput, 1e-6))
	})
})

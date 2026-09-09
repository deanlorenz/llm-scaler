package aggregation

import (
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

var _ = Describe("PRCCom", func() {

	It("computes D_sat[role]/N_com for a non-disaggregated result", func() {
		sat := &domain.AnalyzerResult{TotalDemand: 1000}
		prc, ok := PRCCom(sat, domain.RoleBoth, 5.0, true)
		Expect(ok).To(BeTrue())
		Expect(prc).To(Equal(200.0))
	})

	It("computes D_sat[role]/N_com per role for a disaggregated result", func() {
		sat := &domain.AnalyzerResult{RoleDemand: map[string]float64{"prefill": 400, "decode": 900}}
		prc, ok := PRCCom(sat, "prefill", 4.0, true)
		Expect(ok).To(BeTrue())
		Expect(prc).To(Equal(100.0))

		prc, ok = PRCCom(sat, "decode", 3.0, true)
		Expect(ok).To(BeTrue())
		Expect(prc).To(Equal(300.0))
	})

	// spec §4.4: sat-only identity -- N_com = N_sat implies PRC_com = D_sat/N_sat = PRC_sat exactly.
	It("recovers PRC_sat exactly on the sat-only identity (spec §4.4)", func() {
		sat := &domain.AnalyzerResult{
			TotalDemand:       1000,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}},
		}
		nSat, ok := replicasNeeded(sat, "v") // N_sat(SO) = 1000/200 = 5
		Expect(ok).To(BeTrue())

		prcCom, ok := PRCCom(sat, domain.RoleBoth, nSat, true)
		Expect(ok).To(BeTrue())
		Expect(prcCom).To(Equal(200.0), "PRC_com must equal PRC_sat exactly on the sat-only path")
	})

	It("is undefined when N_com is not ok", func() {
		sat := &domain.AnalyzerResult{TotalDemand: 1000}
		_, ok := PRCCom(sat, domain.RoleBoth, 0, false)
		Expect(ok).To(BeFalse())
	})

	It("is undefined when N_com is zero", func() {
		sat := &domain.AnalyzerResult{TotalDemand: 1000}
		_, ok := PRCCom(sat, domain.RoleBoth, 0, true)
		Expect(ok).To(BeFalse())
	})

	It("is undefined when N_com is negative", func() {
		sat := &domain.AnalyzerResult{TotalDemand: 1000}
		_, ok := PRCCom(sat, domain.RoleBoth, -1, true)
		Expect(ok).To(BeFalse())
	})

	It("is undefined when D_sat has no defined demand for the role at all (A4)", func() {
		sat := &domain.AnalyzerResult{RoleDemand: map[string]float64{"decode": 900}} // silent on prefill
		_, ok := PRCCom(sat, "prefill", 4.0, true)
		Expect(ok).To(BeFalse())
	})

	// A real zero demand with a positive N_com is defined and yields PRC_com == 0,
	// not undefined -- distinguishing present-and-zero from absent (A13/A14).
	It("is defined and zero when demand is a real zero (not manufactured, not undefined)", func() {
		sat := &domain.AnalyzerResult{TotalDemand: 0}
		prc, ok := PRCCom(sat, domain.RoleBoth, 5.0, true)
		Expect(ok).To(BeTrue())
		Expect(prc).To(BeZero())
	})

	// spec §5.3 self-consistency assertion: ceil(D_sat[role]/PRC_com(SO))
	// recovers N_com(SO). Test 12.
	It("round-trips through ceil: ceil(D_sat[role]/PRC_com(SO)) recovers N_com(SO) (test 12)", func() {
		sat := &domain.AnalyzerResult{TotalDemand: 1000}
		nCom := 7.0 // an aggregated N that need not divide D_sat evenly
		prcCom, ok := PRCCom(sat, domain.RoleBoth, nCom, true)
		Expect(ok).To(BeTrue())

		recovered := math.Ceil(sat.TotalDemand / prcCom)
		Expect(recovered).To(Equal(math.Ceil(nCom)))
	})

	It("round-trips through ceil for a disaggregated role too", func() {
		sat := &domain.AnalyzerResult{RoleDemand: map[string]float64{"prefill": 777}}
		nCom := 11.0
		prcCom, ok := PRCCom(sat, "prefill", nCom, true)
		Expect(ok).To(BeTrue())

		demand, present := demandForRole(sat, "prefill")
		Expect(present).To(BeTrue())
		recovered := math.Ceil(demand / prcCom)
		Expect(recovered).To(Equal(math.Ceil(nCom)))
	})

	// spec test 4: disaggregated, per-role aggregation where another analyzer
	// is higher for ONE role only -- the other role must be unaffected.
	It("aggregates independently per role: a higher contributor for prefill only raises PRC_com there, not for decode (test 4)", func() {
		sat := &domain.AnalyzerResult{
			RoleDemand: map[string]float64{"prefill": 400, "decode": 900},
			VariantCapacities: []domain.VariantCapacity{
				{VariantName: "p1", Role: "prefill", PerReplicaCapacity: 100}, // N=4
				{VariantName: "d1", Role: "decode", PerReplicaCapacity: 300},  // N=3
			},
		}
		other := &domain.AnalyzerResult{
			RoleDemand: map[string]float64{"prefill": 800},
			VariantCapacities: []domain.VariantCapacity{
				{VariantName: "p1", Role: "prefill", PerReplicaCapacity: 100}, // N=8, higher than sat's 4
				// other says nothing about decode/d1 at all.
			},
		}

		nPrefill, ok := AggN([]*domain.AnalyzerResult{sat, other}, "p1")
		Expect(ok).To(BeTrue())
		Expect(nPrefill).To(Equal(8.0), "other's higher N must win for prefill")

		nDecode, ok := AggN([]*domain.AnalyzerResult{sat, other}, "d1")
		Expect(ok).To(BeTrue())
		Expect(nDecode).To(Equal(3.0), "decode must be untouched by other's prefill-only disagreement")

		prcPrefill, ok := PRCCom(sat, "prefill", nPrefill, true)
		Expect(ok).To(BeTrue())
		Expect(prcPrefill).To(Equal(50.0), "400/8 -- lower than sat's own PRC of 100, correctly reflecting more replicas needed")

		prcDecode, ok := PRCCom(sat, "decode", nDecode, true)
		Expect(ok).To(BeTrue())
		Expect(prcDecode).To(Equal(300.0), "decode's PRC_com is unaffected, since nothing disagreed there")
	})

	// spec test 25 (read half; the write-back-in-the-same-layout half belongs
	// to composite construction, step 9): a non-disaggregated result
	// (RoleDemand == nil, demand in TotalDemand) composes correctly through
	// PRCCom exactly as the disaggregated layout does.
	It("composes correctly for the non-disaggregated layout (RoleDemand nil, demand in TotalDemand) (test 25, read half)", func() {
		sat := &domain.AnalyzerResult{
			TotalDemand:       600,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 150}}, // N=4
		}
		nCom, ok := AggN([]*domain.AnalyzerResult{sat}, "v")
		Expect(ok).To(BeTrue())
		Expect(nCom).To(Equal(4.0))

		prcCom, ok := PRCCom(sat, domain.RoleBoth, nCom, true)
		Expect(ok).To(BeTrue())
		Expect(prcCom).To(Equal(150.0))
	})
})

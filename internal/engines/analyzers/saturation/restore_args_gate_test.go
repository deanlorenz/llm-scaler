package saturation

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// The restore gate by how much of the configuration still holds: the whole
// of it, the arguments without the image, or neither.
var _ = Describe("learned state restored by engine arguments", func() {
	params := func(maxNumSeqs int64, image string) *capacity.EngineParams {
		return &capacity.EngineParams{Engine: "vllm", MaxNumSeqs: maxNumSeqs, Image: image}
	}
	states := []domain.VariantReplicaState{
		{VariantName: "variant-a", AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
	}
	newProcess := func(p *capacity.EngineParams) *SaturationAnalyzer {
		store := capacity.NewStore()
		store.Update("test-ns", "test-model", "variant-a", capacity.Record{EngineParams: p})
		return NewSaturationAnalyzer(store)
	}
	capacityOf := func(a *SaturationAnalyzer) float64 {
		result, err := a.Analyze(context.Background(), makeAnalyzerInput(
			[]domain.ReplicaMetrics{makeReplicaMetrics("pod-a", "variant-a", 1000, 16000, 0, 100, 50)}, states))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.VariantCapacities).To(HaveLen(1))
		return result.VariantCapacities[0].PerReplicaCapacity
	}
	// learn saturates variant-a at 4000 tokens against a k1 of 12800 and
	// returns the snapshot as the ConfigMap would hold it, with an ITL baseline
	// for each liveness key so the latency figure's fate can be read.
	learn := func(p *capacity.EngineParams) LearnedState {
		a := newProcess(p)
		_, err := a.Analyze(context.Background(), makeAnalyzerInput(
			[]domain.ReplicaMetrics{makeReplicaMetrics("pod-a", "variant-a", 4000, 16000, 6, 100, 50)}, states))
		Expect(err).NotTo(HaveOccurred())
		Expect(capacityOf(a)).To(Equal(float64(4000)), "precondition: k2 learned")
		s := a.ExportLearnedState()
		Expect(s.VariantSeenAt).NotTo(BeEmpty(), "precondition: liveness exported")
		for k := range s.VariantSeenAt {
			s.ITLBaseline[k] = 0.05
		}
		raw, err := json.Marshal(s)
		Expect(err).NotTo(HaveOccurred())
		var back LearnedState
		Expect(json.Unmarshal(raw, &back)).To(Succeed())
		Expect(back.Args).To(HaveKey("test-ns/variant-a"), "the arguments travel with the configuration")
		return back
	}
	itlBaselines := func(a *SaturationAnalyzer) int {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.itlBaseline)
	}

	It("restores everything under the same arguments and image", func() {
		second := newProcess(params(256, "vllm:v1"))
		second.RestoreLearnedState(learn(params(256, "vllm:v1")))
		r, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())
		Expect(r.Installed).To(BeTrue())
		Expect(r.ImageChanged).To(BeFalse())
		Expect(itlBaselines(second)).To(BeNumerically(">", 0), "the control: the ITL baseline comes back")
		Expect(capacityOf(second)).To(Equal(float64(4000)))
	})

	It("restores capacity across an image change made while the controller was down, without the ITL baseline", func() {
		second := newProcess(params(256, "vllm:v2"))
		second.RestoreLearnedState(learn(params(256, "vllm:v1")))
		r, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())
		Expect(r.Installed).To(BeTrue(), "a routine image bump made the restart start cold")
		Expect(r.ImageChanged).To(BeTrue())
		Expect(itlBaselines(second)).To(BeZero(), "a latency figure crossed an engine release")
		Expect(capacityOf(second)).To(Equal(float64(4000)))
	})

	It("keeps the old k2 only as a lower-only, expiring prior when the arguments changed", func() {
		saved := learn(params(256, "vllm:v1"))
		second := newProcess(params(64, "vllm:v1"))
		clock := time.Now()
		second.now = func() time.Time { return clock }
		second.RestoreLearnedState(saved)
		r, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())
		Expect(r.Installed).To(BeFalse())
		Expect(r.Outcome).To(Equal(RestoredPrior))
		Expect(r.Priors).To(Equal(1))

		second.mu.Lock()
		Expect(second.computeCapacityHistory).To(BeEmpty(), "a k2 under other arguments was installed as the variant's own")
		Expect(second.saturatedThroughput).To(BeEmpty(), "a throughput under other arguments was installed")
		Expect(second.startSeconds).To(BeEmpty(), "a start time under other arguments was installed")
		second.mu.Unlock()

		Expect(capacityOf(second)).To(Equal(float64(4000)), "held at the prior")
		clock = clock.Add(BorrowedK2Hold + BorrowedK2Relax/2)
		Expect(capacityOf(second)).To(Equal(float64(8400)), "relaxing half way to own")
		clock = clock.Add(BorrowedK2Relax / 2)
		Expect(capacityOf(second)).To(Equal(float64(12800)), "the prior outlived its relaxation")

		clock = clock.Add(time.Minute)
		evicted := second.EvictStaleHistory()
		Expect(evicted.K2Priors).To(Equal(1))
		Expect(evicted.K2Borrows).To(BeZero(), "a prior is not a borrow clock")
		second.mu.Lock()
		defer second.mu.Unlock()
		Expect(second.priorK2).To(BeEmpty(), "the sweep kept a prior past its life")
	})

	// The prior's hold and relaxation run on the borrow clock, which starts
	// when the bucket is first priced. Swept on when it was installed, a
	// bucket first priced five hours after the restart lost its prior an hour
	// and a half into the relaxation, and capacity jumped to own at once.
	It("keeps a prior first used hours after the restart until its own relaxation ends", func() {
		saved := learn(params(256, "vllm:v1"))
		second := newProcess(params(64, "vllm:v1"))
		clock := time.Now()
		second.now = func() time.Time { return clock }
		second.RestoreLearnedState(saved)
		_, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())

		clock = clock.Add(5 * time.Hour)
		Expect(second.EvictStaleHistory().K2Priors).To(BeZero(), "an unused prior went before the bucket retention")
		Expect(capacityOf(second)).To(Equal(float64(4000)), "first use, five hours in: the hold starts now")

		clock = clock.Add(BorrowedK2Hold + BorrowedK2Relax/2)
		Expect(second.EvictStaleHistory().K2Priors).To(BeZero(), "the prior went part way through its relaxation")
		Expect(capacityOf(second)).To(Equal(float64(8400)), "relaxing half way to own")

		clock = clock.Add(BorrowedK2Relax/2 + time.Minute)
		Expect(second.EvictStaleHistory().K2Priors).To(Equal(1), "the prior outlived its own relaxation")
	})

	It("lets a prior never used go a bucket retention after it was installed", func() {
		saved := learn(params(256, "vllm:v1"))
		second := newProcess(params(64, "vllm:v1"))
		clock := time.Now()
		second.now = func() time.Time { return clock }
		second.RestoreLearnedState(saved)
		_, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())

		clock = clock.Add(second.horizons.BucketRetention - time.Minute)
		Expect(second.EvictStaleHistory().K2Priors).To(BeZero())
		clock = clock.Add(2 * time.Minute)
		Expect(second.EvictStaleHistory().K2Priors).To(Equal(1))
	})

	// A second restart inside the prior's life: the prior and its clock are
	// exported, so the relaxation resumes where it was instead of the variant
	// dropping to its own figure at once.
	It("keeps a prior and its clock across a second restart", func() {
		saved := learn(params(256, "vllm:v1"))
		second := newProcess(params(64, "vllm:v1"))
		clock := time.Now()
		second.now = func() time.Time { return clock }
		second.RestoreLearnedState(saved)
		_, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())
		Expect(capacityOf(second)).To(Equal(float64(4000)), "precondition: the prior in use")

		clock = clock.Add(BorrowedK2Hold + BorrowedK2Relax/2)
		Expect(capacityOf(second)).To(Equal(float64(8400)), "precondition: half way")
		raw, err := json.Marshal(second.ExportLearnedState())
		Expect(err).NotTo(HaveOccurred())
		var again LearnedState
		Expect(json.Unmarshal(raw, &again)).To(Succeed())
		Expect(again.PriorK2).To(HaveLen(1), "the prior was not exported")

		third := newProcess(params(64, "vllm:v1"))
		third.now = func() time.Time { return clock }
		third.RestoreLearnedState(again)
		r, done := third.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())
		Expect(r.Outcome).To(Equal(RestoredWhole))
		Expect(capacityOf(third)).To(Equal(float64(8400)), "the relaxation did not resume where it was")
	})

	It("says when the arguments changed and no prior could be kept", func() {
		saved := learn(params(256, "vllm:v1"))
		// The variant now runs on two GPUs per replica: its k2 keys were
		// filed under one, and are never looked up again.
		store := capacity.NewStore()
		store.Update("test-ns", "test-model", "variant-a", capacity.Record{
			AcceleratorName: "H100", GpuCount: 2, EngineParams: params(64, "vllm:v1"),
		})
		second := NewSaturationAnalyzer(store)
		second.RestoreLearnedState(saved)
		r, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())
		Expect(r.Outcome).To(Equal(RestoredPrior))
		Expect(r.Priors).To(BeZero(), "a prior was kept under a key the variant can no longer read")
	})

	It("names an incomplete read as the reason nothing was restored", func() {
		saved := learn(params(256, "vllm:v1"))
		unread := params(256, "vllm:v1")
		unread.Unresolved = []string{"max_num_seqs"}
		second := newProcess(unread)
		second.RestoreLearnedState(saved)
		r, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())
		Expect(r.Outcome).To(Equal(RestoredIncomplete))
	})

	// llm-d's wide-EP LeaderWorkerSet computes its data-parallel size at
	// start-up. Read as an incomplete configuration, such a variant's own
	// learned state was neither exported nor restored.
	It("restores a variant whose parallel size is computed at start-up", func() {
		wideEP := func() *capacity.EngineParams {
			p := params(256, "vllm:v1")
			p.SharingUnresolved = []string{"data_parallel_size=$((LWS_GROUP_SIZE * DP_SIZE_LOCAL))"}
			return p
		}
		saved := learn(wideEP())
		second := newProcess(wideEP())
		second.RestoreLearnedState(saved)
		r, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())
		Expect(r.Outcome).To(Equal(RestoredWhole))
		Expect(capacityOf(second)).To(Equal(float64(4000)))
		Expect(second.engineFingerprint("test-ns", "test-model", "variant-a")).To(BeEmpty(),
			"a layout read as text is not a key for sharing a latency line")
	})

	It("never takes a prior above what the variant would get without it", func() {
		saved := learn(params(256, "vllm:v1"))
		second := newProcess(params(64, "vllm:v1"))
		second.RestoreLearnedState(saved)
		_, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())
		second.mu.Lock()
		Expect(second.priorK2).To(HaveLen(1))
		var key string
		for k := range second.priorK2 {
			key = k
		}
		second.mu.Unlock()
		ownParams := params(64, "vllm:v1")

		// The control: a prior below what the variant would get is taken.
		b, ok := second.borrowedK2(key, "test-model", 5000, ownParams, true)
		Expect(ok).To(BeTrue())
		Expect(b.k2).To(Equal(int64(4000)))
		Expect(b.prior).To(BeTrue())

		// Above it, not: 4000 against an own capacity of 3000 would raise it.
		_, ok = second.borrowedK2(key, "test-model", 3000, ownParams, true)
		Expect(ok).To(BeFalse(), "a prior raised capacity")
	})

	It("takes no prior for a configuration it could not read completely", func() {
		saved := learn(params(256, "vllm:v1"))
		unread := params(64, "vllm:v1")
		unread.Unresolved = []string{"max_num_seqs"}
		second := newProcess(unread)
		second.RestoreLearnedState(saved)
		r, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())
		Expect(r.Priors).To(BeZero())
		Expect(capacityOf(second)).To(Equal(float64(12800)))
	})

	It("writes back the arguments of entries still pending", func() {
		saved := learn(params(256, "vllm:v1"))
		second := newProcess(params(256, "vllm:v1"))
		second.RestoreLearnedState(saved)
		again := second.ExportLearnedState()
		Expect(again.Args).To(HaveKeyWithValue("test-ns/variant-a", saved.Args["test-ns/variant-a"]))
	})
})

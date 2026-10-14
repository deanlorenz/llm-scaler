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

// Carrying learned state across a controller restart (issue #137): a restart,
// a changed engine configuration and an aged entry. A leader hand-off is the
// same transfer between two processes, and is pinned at the engine, which
// owns the store.
var _ = Describe("learned state across a restart", func() {
	params := func(maxNumSeqs int64) *capacity.EngineParams {
		return &capacity.EngineParams{Engine: "vllm", MaxNumSeqs: maxNumSeqs}
	}
	states := []domain.VariantReplicaState{
		{VariantName: "variant-a", AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
	}
	// newProcess is one controller process: its own store, loaded from the
	// scale target as the engine does before every cycle, and its analyzer.
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
	// learn saturates variant-a at 4000 tokens, against a k1 of 12800, so a
	// process that remembers it prices the replica at 4000 and one that does
	// not prices it at k1.
	learn := func(a *SaturationAnalyzer) LearnedState {
		_, err := a.Analyze(context.Background(), makeAnalyzerInput(
			[]domain.ReplicaMetrics{makeReplicaMetrics("pod-a", "variant-a", 4000, 16000, 6, 100, 50)}, states))
		Expect(err).NotTo(HaveOccurred())
		Expect(capacityOf(a)).To(Equal(float64(4000)), "precondition: the first process learned k2")
		return a.ExportLearnedState()
	}
	// carry is the store's round trip: the state as JSON, as the ConfigMap
	// holds it.
	carry := func(s LearnedState) LearnedState {
		raw, err := json.Marshal(s)
		Expect(err).NotTo(HaveOccurred())
		var back LearnedState
		Expect(json.Unmarshal(raw, &back)).To(Succeed())
		return back
	}

	It("restores k2 into a new process running the same configuration", func() {
		first := newProcess(params(256))
		saved := carry(learn(first))
		Expect(saved.Config).To(HaveKey("test-ns/variant-a"), "the variant's configuration travels with it")

		second := newProcess(params(256))
		Expect(capacityOf(NewSaturationAnalyzer(capacity.NewStore()))).To(Equal(float64(12800)),
			"the control: a process with nothing restored prices the replica at k1")
		rep := second.RestoreLearnedState(saved)
		Expect(rep.PendingVariants).To(Equal(1))
		Expect(capacityOf(second)).To(Equal(float64(4000)),
			"the restart forgot k2 and priced the replica at k1 -- three times the capacity")
	})

	It("does not restore into a variant whose engine configuration changed", func() {
		first := newProcess(params(256))
		saved := carry(learn(first))

		second := newProcess(params(64))
		second.RestoreLearnedState(saved)
		Expect(capacityOf(second)).To(Equal(float64(12800)),
			"a k2 measured under --max-num-seqs=256 priced an engine running 64")
		second.mu.Lock()
		defer second.mu.Unlock()
		Expect(second.pendingRestore).To(BeEmpty(), "checked once, and dropped")
	})

	It("does not restore an entry past its horizon", func() {
		first := newProcess(params(256))
		saved := carry(learn(first))
		for k, w := range saved.K2 {
			w.LastUsed = time.Now().Add(-2 * capacity.HistoryRetention)
			saved.K2[k] = w
		}

		second := newProcess(params(256))
		second.RestoreLearnedState(saved)
		Expect(capacityOf(second)).To(Equal(float64(12800)),
			"a window unread for longer than the retention came back as if it had been read")
	})

	It("keeps each entry's clocks, so a restore does not make a figure younger", func() {
		first := newProcess(params(256))
		saved := learn(first)
		var used time.Time
		for k, w := range saved.K2 {
			used = time.Now().Add(-time.Hour)
			w.LastUsed = used
			saved.K2[k] = w
		}

		second := newProcess(params(256))
		second.RestoreLearnedState(carry(saved))
		second.mu.Lock()
		key := ""
		for k := range saved.K2 {
			key = k
		}
		Expect(second.computeCapacityHistory).To(BeEmpty(), "precondition: pending until the variant's cycle")
		second.mu.Unlock()
		_, done := second.installRestored("test-ns", "test-model", "variant-a")
		Expect(done).To(BeTrue())
		second.mu.Lock()
		defer second.mu.Unlock()
		Expect(second.computeCapacityHistory).To(HaveKey(key))
		Expect(second.computeCapacityHistory[key].Stale(30*time.Minute)).To(BeTrue(),
			"restored as last read now, not an hour ago")
	})

	It("does not restore a variant whose configuration was not known when it was saved", func() {
		first := newProcess(params(256))
		saved := carry(learn(first))
		saved.Config = nil

		second := newProcess(params(256))
		rep := second.RestoreLearnedState(saved)
		Expect(rep.PendingVariants).To(BeZero())
		Expect(rep.Unattributable).To(BeNumerically(">", 0))
		Expect(capacityOf(second)).To(Equal(float64(12800)))
	})

	It("never overwrites what the new process has already learned", func() {
		first := newProcess(params(256))
		saved := carry(learn(first))

		second := newProcess(params(256))
		_, err := second.Analyze(context.Background(), makeAnalyzerInput(
			[]domain.ReplicaMetrics{makeReplicaMetrics("pod-a", "variant-a", 9000, 16000, 6, 100, 50)}, states))
		Expect(err).NotTo(HaveOccurred())
		second.RestoreLearnedState(saved)
		Expect(capacityOf(second)).To(Equal(float64(9000)), "the restore replaced a reading taken since the restart")
	})

	It("forgets everything, restored or learned, when the store is flushed", func() {
		first := newProcess(params(256))
		saved := carry(learn(first))
		first.RestoreLearnedState(saved)
		first.ForgetLearnedState()
		Expect(capacityOf(first)).To(Equal(float64(12800)))
		first.mu.Lock()
		defer first.mu.Unlock()
		Expect(first.pendingRestore).To(BeEmpty())
	})
})

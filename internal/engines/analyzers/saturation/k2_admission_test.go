package saturation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// k2AdmissionCycle runs one Analyze over a single decode row that is full and
// queued (queue 30 over the threshold of 5, 600 000 of 1 162 240 tokens
// resident), shaped by mutate, and returns the k2-decision priorities logged
// for the variant.
func k2AdmissionCycle(t *testing.T, analyzer *SaturationAnalyzer, totalKv int64, mutate func(*domain.ReplicaMetrics)) []string {
	t.Helper()
	ctx, logs := observedCtx(t)
	rm := makeReplicaMetrics("pod-d", "variant-d", 600_000, totalKv, 30, 1000, 1000)
	mutate(&rm)
	_, err := analyzer.Analyze(ctx, makeAnalyzerInput([]domain.ReplicaMetrics{rm},
		[]domain.VariantReplicaState{
			{VariantName: "variant-d", AcceleratorName: "H200", CurrentReplicas: 1, GPUsPerReplica: 1, Role: domain.RoleDecode},
		}))
	require.NoError(t, err)
	var seen []string
	for _, e := range logs.FilterMessage("k2-decision").All() {
		if f := e.ContextMap(); f["variant"] == "variant-d" {
			seen = append(seen, stringField(t, f, "priority"))
		}
	}
	return seen
}

func k2Windows(a *SaturationAnalyzer) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.computeCapacityHistory)
}

// A saturated reading is the variant's k2 only from a row the throughput
// write would also take: Ready, and the variant's own. The control is the
// same row Ready and own, which records.
func TestK2Admission_RecordsOnlyReadyOwnReplicas(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*domain.ReplicaMetrics)
		recorded bool
	}{
		{"a Ready replica of the variant's own", func(*domain.ReplicaMetrics) {}, true},
		{"a warm-pool bridge lent to the variant", func(rm *domain.ReplicaMetrics) { rm.FromWarmPool = true }, false},
		{"a replica that is not Ready", func(rm *domain.ReplicaMetrics) { rm.Ready = false }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			analyzer := NewSaturationAnalyzer(capacity.NewStore())
			seen := k2AdmissionCycle(t, analyzer, 1_162_240, tc.mutate)
			require.NotEmpty(t, seen)
			if tc.recorded {
				assert.Equal(t, "P1-obs", seen[0])
				assert.Equal(t, 1, k2Windows(analyzer), "the reading opens the variant's k2 window")
				return
			}
			assert.Equal(t, k2ReasonObsUnadmitted, seen[0],
				"the saturated reading is named as declined, then the chain falls through")
			assert.Len(t, seen, 2, "the decline, then the tier it fell through to")
			assert.NotEqual(t, "P1-obs", seen[1])
			assert.Zero(t, k2Windows(analyzer), "nothing is written to the variant's k2 window")
		})
	}
}

// An unadmitted row still READS the variant's window: it is only the write
// that is refused. A not-Ready replica next to a measured history prices from
// it rather than from k1.
func TestK2Admission_UnadmittedRowReadsTheWindow(t *testing.T) {
	analyzer := NewSaturationAnalyzer(capacity.NewStore())
	require.Equal(t, []string{"P1-obs"}, k2AdmissionCycle(t, analyzer, 1_162_240, func(*domain.ReplicaMetrics) {}))
	seen := k2AdmissionCycle(t, analyzer, 1_162_240, func(rm *domain.ReplicaMetrics) {
		rm.Ready = false
		rm.TokensInUse = 300_000 // a frozen, lower reading that would pull the window down
		rm.KvCacheUsage = float64(rm.TokensInUse) / float64(rm.TotalKvCapacityTokens)
	})
	assert.Equal(t, []string{k2ReasonObsUnadmitted, "P2-hist"}, seen)
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	for _, ra := range analyzer.computeCapacityHistory {
		assert.Equal(t, 1, ra.Len(), "the declined reading was not folded in")
		assert.InDelta(t, 600_000, ra.Average(), 0.5)
	}
}

// A bridge's KV cache is the pool engine's. It does not overwrite the
// variant's live capacity record, which a parked variant and its compatible
// siblings are priced from.
func TestK2Admission_BridgeDoesNotOverwriteTheLiveRecord(t *testing.T) {
	store := capacity.NewStore()
	analyzer := NewSaturationAnalyzer(store)
	k2AdmissionCycle(t, analyzer, 1_162_240, func(*domain.ReplicaMetrics) {})
	rec := store.Get("test-ns", "test-model", "variant-d")
	require.NotNil(t, rec)
	require.Equal(t, capacity.LearnedFromLive, rec.LearnedFrom)
	require.EqualValues(t, 1_162_240, rec.TotalKvCapacityTokens)

	// The pool runs at a lower --gpu-memory-utilization: a smaller cache.
	k2AdmissionCycle(t, analyzer, 700_000, func(rm *domain.ReplicaMetrics) { rm.FromWarmPool = true })
	rec = store.Get("test-ns", "test-model", "variant-d")
	require.NotNil(t, rec)
	assert.EqualValues(t, 1_162_240, rec.TotalKvCapacityTokens, "the variant's own cache, not the pool engine's")

	// A not-Ready replica's cache is real -- it is the variant's own engine --
	// so its record is still filed.
	k2AdmissionCycle(t, analyzer, 1_100_000, func(rm *domain.ReplicaMetrics) { rm.Ready = false })
	rec = store.Get("test-ns", "test-model", "variant-d")
	require.NotNil(t, rec)
	assert.EqualValues(t, 1_100_000, rec.TotalKvCapacityTokens)
}

// A bridge-only variant with no record yet gets none from the bridge.
func TestK2Admission_BridgeDoesNotCreateALiveRecord(t *testing.T) {
	store := capacity.NewStore()
	analyzer := NewSaturationAnalyzer(store)
	k2AdmissionCycle(t, analyzer, 700_000, func(rm *domain.ReplicaMetrics) { rm.FromWarmPool = true })
	if rec := store.Get("test-ns", "test-model", "variant-d"); rec != nil {
		assert.NotEqual(t, capacity.LearnedFromLive, rec.LearnedFrom)
	}
}

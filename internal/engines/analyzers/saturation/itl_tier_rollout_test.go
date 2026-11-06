package saturation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// The latch across a rollout, through Analyze (SwitchITLTierResetsAfterRollout):
// the cycle after a rollout it is gone, and through the rollout it stands.
func TestITLTier_TheLatchGoesWhenARolloutEnds(t *testing.T) {
	run := func(switches map[string]bool) bool {
		store := capacity.NewStore()
		file := func(rolling bool) {
			store.Update("test-ns", "test-model", "decode-a", capacity.Record{
				EngineParams: mtParams(256, "vllm:v1"), RollingOut: rolling,
			}, time.Now())
		}
		file(false)
		a := NewSaturationAnalyzer(store)
		require.NoError(t, a.SetSwitchesForReplay(switches))
		now := restartT0
		a.now = func() time.Time { return now }
		cycle := func() {
			rows := make([]domain.ReplicaMetrics, 0, 10)
			for i := range 10 {
				k := 0.20 + 0.04*float64(i)
				rm := makeReplicaMetrics(fmt.Sprintf("pod-%d", i), "decode-a", int64(k*16000), 16000, 0, 100, 50)
				rm.KvUsageInstant = k
				rm.AvgITL, rm.AvgITLKnown = 0.01+0.02*k, true
				rm.Ready, rm.ReadySince = true, restartT0.Add(-time.Hour)
				rows = append(rows, rm)
			}
			_, err := a.Analyze(context.Background(), makeAnalyzerInput(rows, []domain.VariantReplicaState{{
				VariantName: "decode-a", AcceleratorName: "H100", CurrentReplicas: 10, GPUsPerReplica: 1, Role: domain.RoleDecode,
			}}))
			require.NoError(t, err)
			now = now.Add(15 * time.Second)
		}
		key := a.itlWindowKey("test-ns", "test-model", "decode-a", "H100", 1)
		for range 4 {
			cycle()
		}
		require.True(t, a.itlTracker.Readings(key).OLSLatched, "premise: the line stands at the full fit")
		file(true)
		cycle()
		require.True(t, a.itlTracker.Readings(key).OLSLatched, "the tier moved mid-rollout, with no new reading")
		file(false)
		cycle()
		return a.itlTracker.Readings(key).OLSLatched
	}
	assert.False(t, run(nil))
	assert.True(t, run(map[string]bool{SwitchITLTierResetsAfterRollout: false}), "negative control")
}

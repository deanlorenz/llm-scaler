package itl

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// fitPodsRow is a Ready replica of v at fill k reading itlSec.
func fitPodsRow(pod string, k, itlSec float64) domain.ReplicaMetrics {
	return domain.ReplicaMetrics{PodName: pod, VariantName: "v", Ready: true,
		KvUsageInstant: k, KvUsageInstantKnown: true, KvCacheUsage: k, AvgITL: itlSec, AvgITLKnown: true}
}

// The reading names the replicas its line was fitted from: each with how
// many of the fit's readings were its and their mean k and ITL, and the
// count of them all. A pod whose row is no reading (below the band) is not
// among them; nothing about the line moves.
func TestReading_FitPodsAreTheFitsReplicas(t *testing.T) {
	tr := NewTracker()
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	var r Reading
	for c := range 4 {
		rows := []domain.ReplicaMetrics{
			fitPodsRow("a", 0.30, 0.020), fitPodsRow("b", 0.45, 0.026), fitPodsRow("c", 0.60, 0.032+0.001*float64(c%2)),
			fitPodsRow("idle", 0.05, 0.010),
		}
		r = tr.Observe("key", "v", rows, Admission{}, t0.Add(time.Duration(c)*15*time.Second))
	}
	require.Equal(t, TierOLS, r.Tier, "premise: twelve steady readings spread 0.30 fit the full line")
	assert.Equal(t, 12, r.FitRows)
	require.Len(t, r.FitPods, 3, "the idle replica offered no reading")
	assert.Equal(t, FitPod{Pod: "a", Rows: 4, K: 0.30, ITLSec: 0.020}, r.FitPods[0])
	assert.Equal(t, "c", r.FitPods[2].Pod)
	assert.InDelta(t, 0.0325, r.FitPods[2].ITLSec, 1e-12, "the mean of its four readings")

	pods, omitted := r.WritePods(2)
	assert.Equal(t, 1, omitted)
	require.Len(t, pods, 2)
	assert.Equal(t, "a", pods[0]["pod"], "lowest ITL first")
	assert.Equal(t, "b", pods[1]["pod"])
	assert.Equal(t, 4, pods[0]["rows"])
	assert.Equal(t, WriteTimeBase, pods[0]["timeBase"])
}

// A reading with no line fitted this cycle lists no pods.
func TestReading_NoFitListsNoPods(t *testing.T) {
	pods, omitted := Reading{}.WritePods(16)
	assert.Empty(t, pods)
	assert.Zero(t, omitted)
}

// The bound holds, ties go by pod name, and a bound under zero lists none.
func TestReading_WritePodsBoundAndTies(t *testing.T) {
	var r Reading
	for i := range 20 {
		r.FitPods = append(r.FitPods, FitPod{Pod: fmt.Sprintf("p%02d", 19-i), Rows: 1, ITLSec: 0.02})
	}
	pods, omitted := r.WritePods(16)
	assert.Equal(t, 4, omitted)
	require.Len(t, pods, 16)
	assert.Equal(t, "p00", pods[0]["pod"], "equal readings go by pod name")
	assert.Equal(t, "p15", pods[15]["pod"])

	pods, omitted = r.WritePods(-1)
	assert.Empty(t, pods)
	assert.Equal(t, 20, omitted)
}

// The margins are the full fit's gate against its thresholds, and the
// readings the line was fitted from.
func TestReading_AdmissionMargins(t *testing.T) {
	r := Reading{SteadySpread: 0.40, SteadyHeld: 7, FitRows: 9}
	kv := r.AdmissionMargins()
	require.Zero(t, len(kv)%2)
	m := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	assert.InDelta(t, 0.40, m["kSpread"], 1e-12)
	assert.InDelta(t, DefaultMinKSpread, m["kSpreadThreshold"], 1e-12)
	assert.InDelta(t, 0.10, m["kSpreadOverThreshold"], 1e-12)
	assert.Equal(t, DefaultMinSamples, m["steadyHeldThreshold"])
	assert.Equal(t, -3, m["steadyHeldOverThreshold"])
	assert.Equal(t, 9, m["fitRows"])
}

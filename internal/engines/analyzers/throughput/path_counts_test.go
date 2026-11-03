package throughput

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// Each per-path counter counts its own path: full fits on a spread fleet,
// pinned fits on a narrow one, a held slope once the narrow fleet falls
// below the band, and the settling readings of a replica that joins. The
// parent's line counts nothing: the replay's required paths fail on it
// (learnreplay's TestGoldens_TheThroughputFloorCheckFails).
func TestPathCounts_EachPathCountsItsOwn(t *testing.T) {
	run := func(a *ThroughputAnalyzer) map[string]int64 {
		r := newLineRun(t, a)
		early := lineStart.Add(-time.Hour)
		spread := func() []domain.ReplicaMetrics {
			rows := make([]domain.ReplicaMetrics, 0, 4)
			for i, k := range []float64{0.20, 0.40, 0.60, 0.75} {
				rows = append(rows, lineRow("pod-"+string(rune('a'+i)), k, lineAt(k), early))
			}
			return rows
		}
		for range 16 {
			r.step(spread())
		}
		joined := r.now()
		for range 4 {
			r.step(append(spread(), lineRow("pod-join", 0.46, 0.45, joined)))
		}
		idle := steadyFleet()
		for i := range idle {
			idle[i].KvCacheUsage, idle[i].KvUsageInstant, idle[i].AvgITL = 0.05, 0.05, lineAt(0.05)
		}
		for r.now().Before(lineStart.Add(45 * time.Minute)) {
			r.step(idle)
		}
		return a.PathCounts()
	}
	on := run(NewThroughputAnalyzer())
	t.Logf("on the tracker: %v", on)
	for _, p := range PathNames() {
		assert.Positive(t, on[p], "%s never ran", p)
	}
	parent := run(newParentLineAnalyzer())
	t.Logf("the parent's line: %v", parent)
	for _, p := range PathNames() {
		assert.Zero(t, parent[p], "the parent's line counted %s", p)
	}
	require.ElementsMatch(t, PathNames(), []string{PathITLOLS, PathITLPinned, PathITLHeld, PathITLSettling})
}

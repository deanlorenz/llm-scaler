package slo

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/logging"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
)

// Issue #459: the analyzer alone, with no engine tracker, across a gap
// between two of a model's cycles longer than the state timeout. What the
// line reads after it depends only on the model's own last sight, not on
// whether EvictStale ran, or where in the cycle.

// gapEviction is where in each cycle the rig calls EvictStale.
type gapEviction string

const (
	// evictFirst calls it at the top of the cycle, as the engine and the
	// replay do.
	evictFirst gapEviction = "first"
	// evictBetween calls it between the two models' Analyze calls.
	evictBetween gapEviction = "between"
	// evictNever never calls it, as a caller that drives Analyze alone.
	evictNever gapEviction = "never"
)

const (
	gapStateTimeout = time.Hour
	gapLength       = 2 * time.Hour
)

// gapModels are two models with identical fleets, analyzed in this order.
var gapModels = []string{"m1", "m2"}

// runGap drives the analyzer alone, with switches, over gapAt cycles, a
// jump of gapLength, and settleCycles+4 cycles more, calling EvictStale as
// evict says. It returns, by model, the cycles on which a reading was kept
// out as its replica's settle, as Analyze's slo-variant log states it.
func runGap(t *testing.T, switches map[string]bool, evict gapEviction, gapAt int) map[string][]int {
	t.Helper()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	a := newGapAnalyzer(t, switches, &now)
	analyze := func(model string) int64 { return analyzeSettling(t, a, model, settleFleet()) }
	out := map[string][]int{}
	for i := range gapAt + settleCycles + 4 {
		if i == gapAt {
			now = now.Add(gapLength)
		}
		if evict == evictFirst {
			a.EvictStale(now)
		}
		for j, model := range gapModels {
			if j == 1 && evict == evictBetween {
				a.EvictStale(now)
			}
			if analyze(model) > 0 {
				out[model] = append(out[model], i)
			}
		}
		now = now.Add(settleCycle)
	}
	return out
}

// newGapAnalyzer is the analyzer alone, with no engine view, a state
// timeout of gapStateTimeout, switches, and its clock at *now.
func newGapAnalyzer(t *testing.T, switches map[string]bool, now *time.Time) *Analyzer {
	t.Helper()
	a := New(nil, nil).WithStateTimeout(gapStateTimeout)
	a.now = func() time.Time { return *now }
	require.NoError(t, a.SetSwitchesForReplay(switches))
	return a
}

// analyzeSettling is one Analyze of model in namespace ns with rows, and
// how many of agg's readings were kept out as their replica's settle, as
// the slo-variant log line states it.
func analyzeSettling(t *testing.T, a *Analyzer, model string, rows []domain.ReplicaMetrics) int64 {
	t.Helper()
	in := domain.AnalyzerInput{Namespace: "ns", ModelID: model, Config: policy(44*time.Millisecond, 0),
		ArrivalRate: 1, VariantStates: []domain.VariantReplicaState{settleAgg}, ReplicaMetrics: rows}
	core, logs := observer.New(zapcore.Level(-logging.VERBOSE))
	_, err := a.Analyze(logr.NewContext(context.Background(), zapr.NewLogger(zap.New(core))), in)
	require.NoError(t, err)
	lines := logs.FilterMessage("slo-variant").FilterField(zap.String("variant", "agg")).All()
	require.Len(t, lines, 1, "premise: Analyze logs the variant's line")
	settling, ok := lines[0].ContextMap()["settling"].(int64)
	require.True(t, ok, "slo-variant carries no settling")
	return settling
}

// The forget after a long gap reaches only the stale model's line keys. A
// model of the same namespace analyzed every cycle, whose name has the
// stale one's as a prefix (m10 against m), is midway through a replica's
// settle (a new Ready time under its name) when m comes back from its gap:
// the settle runs its full steadyFor, and its replica stays on the own
// tracker as it was. Forgetting its line keys' traces would end the settle
// on the next cycle: a trace made anew takes the settle as begun before the
// key's first sight, which does not wait with no engine view. Kills a
// prefix without the trailing separator ("ns|m" matches "ns|m10|") and one
// of the whole namespace ("ns|").
func TestSettle_AfterALongGapTheForgetSparesARecentModel(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	a := newGapAnalyzer(t, nil, &now)
	analyzeSettling(t, a, "m", settleFleet())
	now = now.Add(gapLength)
	restartAt := settleCycles + 2
	staleAt := restartAt + 2
	restartedAt := now.Add(time.Duration(restartAt)*settleCycle - 5*time.Second)
	servingSince := now.Add(-time.Hour)
	rows := func(i int) []domain.ReplicaMetrics {
		f := settleFleet()
		for j := range f {
			f[j].ReadySince = servingSince
		}
		if i >= restartAt {
			f[2].ReadySince = restartedAt
		}
		return f
	}
	key := settle.Key{Pod: "agg-c", Variant: "agg"}
	var settling []int
	for i := range restartAt + settleCycles + 3 {
		if analyzeSettling(t, a, "m10", rows(i)) > 0 {
			settling = append(settling, i)
		}
		if i == staleAt {
			before, held := a.ownSettle.Replica("ns/m10", key)
			require.True(t, held, "premise: the own tracker holds m10's restarted replica")
			assert.Zero(t, analyzeSettling(t, a, "m", settleFleet()), "premise: m, back from its gap, is first-seen")
			after, held := a.ownSettle.Replica("ns/m10", key)
			require.True(t, held, "m's gap forgot m10's replica on the own tracker")
			assert.Equal(t, before.Since, after.Since, "m's gap moved m10's settle on the own tracker")
		}
		now = now.Add(settleCycle)
	}
	assert.Equal(t, span(restartAt, settleCycles), settling, "m's gap ended m10's settle: its line keys' traces were forgotten")
}

// After a gap past the state timeout every model is first-seen, wherever
// EvictStale ran and whether it ran at all: nothing settles, and the two
// models read alike. Before the fix only a model EvictStale had evicted was
// first-seen; one it had not read the gap as an absence (settle.Rejoined)
// and settled for steadyFor.
func TestSettle_AfterALongGapTheAnalyzerAloneReadsEveryModelAlike(t *testing.T) {
	gapAt := settleCycles + 2
	for _, evict := range []gapEviction{evictFirst, evictBetween, evictNever} {
		t.Run(string(evict), func(t *testing.T) {
			settling := runGap(t, nil, evict, gapAt)
			t.Logf("settling cycles: %v", settling)
			assert.Empty(t, settling["m1"], "the model analyzed first settled after the gap")
			assert.Empty(t, settling["m2"], "the model analyzed second settled after the gap")
		})
	}
}

// With SwitchITLLongGapFirstSeen off, as before: a model EvictStale did not
// evict reads the gap as an absence and settles for steadyFor, so the
// outcome follows where EvictStale ran.
func TestSettle_AfterALongGapWithTheSwitchOffEvictionOrderStillMatters(t *testing.T) {
	gapAt := settleCycles + 2
	off := map[string]bool{SwitchITLLongGapFirstSeen: false}
	settled := span(gapAt, settleCycles)
	first := runGap(t, off, evictFirst, gapAt)
	assert.Empty(t, first["m1"], "evicted first: m1 settled")
	assert.Empty(t, first["m2"], "evicted first: m2 settled")
	between := runGap(t, off, evictBetween, gapAt)
	assert.Equal(t, settled, between["m1"], "evicted between: m1, analyzed before the eviction, did not settle")
	assert.Empty(t, between["m2"], "evicted between: m2, evicted before its Analyze, settled")
	never := runGap(t, off, evictNever, gapAt)
	assert.Equal(t, settled, never["m1"], "never evicted: m1 did not settle")
	assert.Equal(t, settled, never["m2"], "never evicted: m2 did not settle")
}

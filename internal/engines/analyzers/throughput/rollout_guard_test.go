package throughput

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// The line in a rollout (issue #481), each through Analyze with its switch on
// and off: no rows while the variant rolls out, and a forget under new engine
// arguments.

// rollParams is an engine stating a cap of maxNumSeqs.
func rollParams(maxNumSeqs int64) *capacity.EngineParams {
	return &capacity.EngineParams{Engine: inferenceengine.EngineVLLM, MaxNumSeqs: maxNumSeqs, MaxNumSeqsSet: true}
}

// fileRecord files p for v1, rolling out or not.
func fileRecord(store *capacity.Store, p *capacity.EngineParams, rolling bool) {
	store.Update("ns", "m", "v1", capacity.Record{EngineParams: p, RollingOut: rolling}, lineStart)
}

// spreadFleet is three replicas at distinct fills on the line, so the window
// fits in a few cycles.
func spreadFleet(revision string) []domain.ReplicaMetrics {
	early := lineStart.Add(-time.Hour)
	f := []domain.ReplicaMetrics{
		lineRow("pod-a", 0.30, lineAt(0.30), early),
		lineRow("pod-b", 0.45, lineAt(0.45), early),
		lineRow("pod-c", 0.60, lineAt(0.60), early),
	}
	for i := range f {
		f[i].Revision = revision
	}
	return f
}

// warm runs cycles of the fleet, and returns what the window then holds.
func warm(t *testing.T, r *lineRun, cycles int, fleet []domain.ReplicaMetrics) int {
	t.Helper()
	for range cycles {
		r.step(fleet)
	}
	return r.a.lines.WindowLen(variantKey("ns", "m", "v1"))
}

// While the scale target says it rolls out, the line takes nothing from a
// row, and fits from what it holds: the variant goes on being priced. Off,
// the rows are offered as before.
func TestRollout_ThePublishedRolloutOffersNoRows(t *testing.T) {
	run := func(a *ThroughputAnalyzer) (itl.Reading, int, int, float64) {
		store := capacity.NewStore()
		fileRecord(store, rollParams(64), false)
		r := newLineRun(t, a.WithCapacityStore(store))
		before := warm(t, r, 4, spreadFleet("rev-1"))
		fileRecord(store, rollParams(64), true)
		vc, ok := r.step(spreadFleet("rev-1"))
		require.True(t, ok)
		got, _, _ := lineRead(r.a)
		return got, before, r.a.lines.WindowLen(variantKey("ns", "m", "v1")), vc.PerReplicaCapacity
	}
	on, before, after, supply := run(NewThroughputAnalyzer())
	require.Positive(t, before, "premise: the window holds readings")
	require.Zero(t, on.Considered, "a row reached the line mid-rollout")
	require.Equal(t, before, after, "the window took a reading mid-rollout")
	require.NotEmpty(t, on.Tier, "the line goes on fitting from what it holds")
	require.Positive(t, supply, "the variant goes on being priced")

	off, beforeOff, afterOff, _ := run(withSwitchOff(SwitchITLRolloutGuard)())
	require.Equal(t, 3, off.Considered, "negative control: off, the rows are offered")
	require.Greater(t, afterOff, beforeOff, "negative control: off, the window takes them")
}

// Revisions that disagree are a rollout with no store at all: the status may
// not say so (domain.RevisionsMixed).
func TestRollout_MixedRevisionsOfferNoRows(t *testing.T) {
	mixed := func() []domain.ReplicaMetrics {
		f := spreadFleet("rev-1")
		f[1].Revision = "rev-2"
		return f
	}
	run := func(a *ThroughputAnalyzer) itl.Reading {
		r := newLineRun(t, a)
		warm(t, r, 2, spreadFleet("rev-1"))
		r.step(mixed())
		got, _, _ := lineRead(r.a)
		return got
	}
	require.Zero(t, run(NewThroughputAnalyzer()).Considered)
	require.Equal(t, 3, run(withSwitchOff(SwitchITLRolloutGuard)()).Considered, "negative control")
}

// A lent replica runs the pool's template, and one that reports no revision
// says nothing: neither is a rollout.
func TestRollout_ABridgeOrAnUnknownRevisionIsNotOne(t *testing.T) {
	for name, edit := range map[string]func(*domain.ReplicaMetrics){
		"a bridge":          func(m *domain.ReplicaMetrics) { m.Revision, m.FromWarmPool = "pool", true },
		"an unknown one":    func(m *domain.ReplicaMetrics) { m.Revision = "" },
		"the same revision": func(*domain.ReplicaMetrics) {},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLineRun(t, NewThroughputAnalyzer())
			f := spreadFleet("rev-1")
			edit(&f[1])
			r.step(f)
			got, _, _ := lineRead(r.a)
			require.Positive(t, got.Considered)
		})
	}
}

// The readings come back when the rollout ends.
func TestRollout_TheLineReadsAgainWhenItEnds(t *testing.T) {
	store := capacity.NewStore()
	fileRecord(store, rollParams(64), true)
	r := newLineRun(t, NewThroughputAnalyzer().WithCapacityStore(store))
	r.step(spreadFleet("rev-1"))
	got, _, _ := lineRead(r.a)
	require.Zero(t, got.Considered, "premise: rolling out")
	fileRecord(store, rollParams(64), false)
	r.step(spreadFleet("rev-2"))
	got, _, _ = lineRead(r.a)
	require.Equal(t, 3, got.Considered)
}

// The defect the issue names: a rollout that lowers the cap judges an old pod
// against the new one. Its reading above the band is taken as at the new cap,
// which it never held. On, it is not offered.
func TestRollout_AnOldPodIsNotJudgedAgainstTheNewCap(t *testing.T) {
	run := func(a *ThroughputAnalyzer) itl.Reading {
		store := capacity.NewStore()
		fileRecord(store, rollParams(64), true)
		newLineRun(t, a.WithCapacityStore(store)).step([]domain.ReplicaMetrics{capLineRow("pod-a", 0.88)})
		got, _, _ := lineRead(a)
		return got
	}
	require.Zero(t, run(NewThroughputAnalyzer()).Capped)
	require.Equal(t, 1, run(withSwitchOff(SwitchITLRolloutGuard)()).Capped, "negative control: off, the old pod is at the new cap")
}

// A silent line in a rollout says so, ahead of the restart a line that was
// never live would otherwise read as.
func TestRollout_ASilentLineNamesTheRollout(t *testing.T) {
	store := capacity.NewStore()
	fileRecord(store, rollParams(64), true)
	r := newLineRun(t, NewThroughputAnalyzer().WithCapacityStore(store))
	r.step(spreadFleet("rev-1"))
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	state := r.a.variantStates[variantKey("ns", "m", "v1")]
	require.Empty(t, state.reading.Tier, "premise: no line")
	require.Equal(t, silenceRollout, silenceCause(state, state.reading))
}

// A change of arguments forgets the old engine's line: the window and its B,
// and the held slope with them. Off, the window goes on being fitted, and only
// the held slope ends. The rollout guard is off here, so the test reads the
// forget alone: on a live rollout it runs with no rows at all.
func TestRollout_NewArgumentsForgetTheLine(t *testing.T) {
	run := func(names ...string) (int, int) {
		store := capacity.NewStore()
		fileRecord(store, rollParams(64), false)
		r := newLineRun(t, withSwitchesOff(names...)().WithCapacityStore(store))
		before := warm(t, r, 4, spreadFleet("rev-1"))
		fileRecord(store, rollParams(128), false)
		r.step(spreadFleet("rev-1"))
		return before, r.a.lines.WindowLen(variantKey("ns", "m", "v1"))
	}
	before, after := run(SwitchITLRolloutGuard)
	require.Equal(t, 3, after, "the window holds only this cycle's readings, had %d", before)
	before, after = run(SwitchITLRolloutGuard, SwitchITLForgetsOnNewArguments)
	require.Equal(t, before+3, after, "negative control: off, the old engine's readings stay")
}

// Unknown arguments are no change: a store that loses the variant for a cycle
// forgets nothing, and the arguments it comes back with are no new ones.
func TestRollout_UnknownArgumentsForgetNothing(t *testing.T) {
	store := capacity.NewStore()
	fileRecord(store, rollParams(64), false)
	r := newLineRun(t, NewThroughputAnalyzer().WithCapacityStore(store))
	before := warm(t, r, 2, spreadFleet("rev-1"))
	fileRecord(store, nil, false)
	r.step(spreadFleet("rev-1"))
	fileRecord(store, rollParams(64), false)
	r.step(spreadFleet("rev-1"))
	require.Equal(t, before+6, r.a.lines.WindowLen(variantKey("ns", "m", "v1")))
}

// Arguments first read on a line already fitting are no change: there is
// nothing to say the window was another engine's.
func TestRollout_FirstKnownArgumentsForgetNothing(t *testing.T) {
	store := capacity.NewStore()
	r := newLineRun(t, NewThroughputAnalyzer().WithCapacityStore(store))
	before := warm(t, r, 2, spreadFleet("rev-1"))
	require.Positive(t, before, "premise: the window holds readings")
	fileRecord(store, rollParams(64), false)
	r.step(spreadFleet("rev-1"))
	require.Equal(t, before+3, r.a.lines.WindowLen(variantKey("ns", "m", "v1")))
}

// After a forget the arguments read next are the first again, as for a new
// line: nothing is left to forget.
func TestRollout_AForgetTakesTheArguments(t *testing.T) {
	store := capacity.NewStore()
	fileRecord(store, rollParams(64), false)
	r := newLineRun(t, NewThroughputAnalyzer().WithCapacityStore(store))
	warm(t, r, 2, spreadFleet("rev-1"))
	r.a.mu.Lock()
	r.a.forgetLineLocked("ns", "m", "v1")
	require.Empty(t, r.a.variantStates[variantKey("ns", "m", "v1")].lineArgs)
	r.a.mu.Unlock()
}

// The forget keeps the replicas' traces, as the annotation's does: a step of
// load across it is still read as a step
// (TestForget_TheSteadyGateSeesTheFirstCycleAfter).
func TestRollout_NewArgumentsKeepTheTraces(t *testing.T) {
	store := capacity.NewStore()
	fileRecord(store, rollParams(64), false)
	r := newLineRun(t, withSwitchOff(SwitchITLRolloutGuard)().WithCapacityStore(store))
	warm(t, r, 12, spreadFleet("rev-1"))
	stepped := spreadFleet("rev-1")
	for i := range stepped {
		stepped[i].KvCacheUsage, stepped[i].KvUsageInstant = 0.75, 0.75
		stepped[i].AvgITL = lineAt(0.75)
	}
	fileRecord(store, rollParams(128), false)
	r.step(stepped)
	got, _, _ := lineRead(r.a)
	require.Equal(t, 3, got.Unsteady, "the traces went with the line: the step reads as steady")
}

// Without the steady gate the line is the parent's window, which the guard
// and the forget do not touch.
func TestRollout_TheParentsWindowIsLeftAlone(t *testing.T) {
	store := capacity.NewStore()
	fileRecord(store, rollParams(64), true)
	a := newParentLineAnalyzer().WithCapacityStore(store)
	r := newLineRun(t, a)
	r.step(spreadFleet("rev-1"))
	st, _ := a.VariantState("m", "ns", "v1")
	require.Equal(t, 3, st.SampleCount)
}

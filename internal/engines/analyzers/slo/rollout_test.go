package slo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// The line in a rollout (issue #481), through Analyze, with the switch on and
// off.

// rolloutRig runs Analyze over a store holding agg's engine.
type rolloutRig struct {
	t     *testing.T
	a     *Analyzer
	store *capacity.Store
	now   time.Time
}

func newRolloutRig(t *testing.T, switches map[string]bool) *rolloutRig {
	t.Helper()
	r := &rolloutRig{t: t, store: capacity.NewStore(), now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	r.file(false)
	r.a = New(nil, r.store)
	r.a.now = func() time.Time { return r.now }
	require.NoError(t, r.a.SetSwitchesForReplay(switches))
	return r
}

// file files agg's engine, rolling out or not.
func (r *rolloutRig) file(rolling bool) {
	r.store.Update("ns", "m", "agg", capacity.Record{EngineParams: &capacity.EngineParams{MaxNumSeqs: 64, MaxNumSeqsSet: true},
		RollingOut: rolling}, r.now)
}

// cycle analyzes rows, and returns how many readings the line's window holds.
func (r *rolloutRig) cycle(rows []domain.ReplicaMetrics) int {
	r.t.Helper()
	_, err := r.a.Analyze(context.Background(), domain.AnalyzerInput{Namespace: "ns", ModelID: "m",
		Config: policy(44*time.Millisecond, 0), VariantStates: []domain.VariantReplicaState{settleAgg}, ReplicaMetrics: rows})
	require.NoError(r.t, err)
	r.now = r.now.Add(settleCycle)
	return r.a.lines.WindowLen(windowKey("ns", "m", settleAgg))
}

func revised(rows []domain.ReplicaMetrics, revision string) []domain.ReplicaMetrics {
	for i := range rows {
		rows[i].Revision = revision
	}
	return rows
}

// While the scale target says it rolls out, the window takes nothing; when
// the rollout ends it reads again. Off, it takes every row.
func TestRollout_ThePublishedRolloutOffersNoRows(t *testing.T) {
	run := func(switches map[string]bool) (before, during, after int) {
		r := newRolloutRig(t, switches)
		before = r.cycle(revised(settleFleet(), "rev-1"))
		r.file(true)
		during = r.cycle(revised(settleFleet(), "rev-1"))
		r.file(false)
		after = r.cycle(revised(settleFleet(), "rev-2"))
		return before, during, after
	}
	before, during, after := run(nil)
	require.Equal(t, 3, before, "premise: the window takes the rows")
	assert.Equal(t, before, during, "the window took a reading mid-rollout")
	assert.Equal(t, before+3, after, "the readings come back when it ends")

	_, during, _ = run(map[string]bool{SwitchITLRolloutGuard: false})
	assert.Equal(t, 6, during, "negative control: off, the window takes them")
}

// Revisions that disagree are a rollout whatever the status says.
func TestRollout_MixedRevisionsOfferNoRows(t *testing.T) {
	run := func(switches map[string]bool) int {
		r := newRolloutRig(t, switches)
		rows := revised(settleFleet(), "rev-1")
		rows[1].Revision = "rev-2"
		return r.cycle(rows)
	}
	assert.Zero(t, run(nil))
	assert.Equal(t, 3, run(map[string]bool{SwitchITLRolloutGuard: false}), "negative control")
}

// The guard is per variant: a sibling rolling out leaves this line reading,
// and an analyzer with no store still reads the revisions.
func TestRollout_OneVariantsRolloutIsNotAnother(t *testing.T) {
	r := newRolloutRig(t, nil)
	r.store.Update("ns", "m", "other", capacity.Record{RollingOut: true}, r.now)
	assert.Equal(t, 3, r.cycle(revised(settleFleet(), "rev-1")))

	bare := New(nil, nil)
	in := domain.AnalyzerInput{Namespace: "ns", ModelID: "m"}
	assert.Nil(t, bare.lineRows(in, fleetReading{mixed: map[string]bool{"agg": true}}, "agg"))
}

// The line is priced from what it holds through a rollout: a window of the
// old engine's readings under the same arguments is not forgotten.
func TestRollout_TheLineFitsFromWhatItHolds(t *testing.T) {
	r := newRolloutRig(t, nil)
	var got int
	for range settleCycles + 8 {
		got = r.cycle(revised(settleFleet(), "rev-1"))
	}
	r.file(true)
	assert.Equal(t, got, r.cycle(revised(settleFleet(), "rev-1")))
	reading := r.a.lines.Observe(windowKey("ns", "m", settleAgg), "agg", nil, admission(policy(44*time.Millisecond, 0), nil, true), r.now)
	assert.NotEmpty(t, reading.Tier, "no line from a full window")
}

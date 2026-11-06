package saturation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// k2's wait after a rollout (analyzer-learner.md, 4.2 and 4.3;
// SwitchK2WaitsAfterRollout).

// revisedFull is p0 at its queue, Ready since ready, on revision.
func revisedFull(ready time.Time, revision string) []domain.ReplicaMetrics {
	row := fullRow("p0", "v", ready)
	row.Revision = revision
	return []domain.ReplicaMetrics{row}
}

// rolledCycles settles p0 on rev-1, rolls it to rev-2 under its name and Ready
// time, and returns the k2 sources of the cycles after: the wait a new Ready
// time would start, read from the tracker's RolledOut.
func rolledCycles(t *testing.T, switches map[string]bool, from string) []string {
	t.Helper()
	r := newRestartRig(t, restartT0)
	require.NoError(t, r.a.SetSwitchesForReplay(switches))
	ready := restartT0.Add(-time.Hour)
	settled(t, r, func() []domain.ReplicaMetrics { return revisedFull(ready, from) }, decodeVariants("v"))
	var got []string
	for range waitWindow + 1 {
		got = append(got, r.cycle(revisedFull(ready, "rev-2"), decodeVariants("v"))...)
	}
	return got
}

// On, a new revision under the same key waits a window, and is admitted after
// it. Off, it is read at once: this is the reading a k2 is written low from,
// and a low k2 is permanent (3.4).
func TestK2Rollout_ANewRevisionWaitsAWindow(t *testing.T) {
	on := rolledCycles(t, nil, "rev-1")
	for i, source := range on[:waitWindow] {
		assert.Equal(t, k2ReasonObsUnsettled, source, "cycle %d after the rollout", i)
	}
	assert.Equal(t, p1Obs, on[waitWindow], "the wait ends: a window, not for good")

	for i, source := range rolledCycles(t, map[string]bool{SwitchK2WaitsAfterRollout: false}, "rev-1") {
		assert.Equal(t, p1Obs, source, "negative control: off, cycle %d is read at once", i)
	}
}

// A revision read for the first time is no rollout: an unknown turned known
// starts nothing (settle.Tracker.note), and a fleet whose revisions start to
// be read does not wait.
func TestK2Rollout_AFirstRevisionIsNotOne(t *testing.T) {
	for i, source := range rolledCycles(t, nil, "") {
		assert.Equal(t, p1Obs, source, "cycle %d", i)
	}
}

// With the memo of Ready times (SwitchK2ReadsSettle off) the switch reads
// nothing: there is no tracker to read.
func TestK2Rollout_TheMemoDoesNotWait(t *testing.T) {
	for i, source := range rolledCycles(t, map[string]bool{SwitchK2ReadsSettle: false}, "rev-1") {
		assert.Equal(t, p1Obs, source, "cycle %d", i)
	}
}

// A wait begun later than the rollout stands: the rollout does not move it
// back. Past the rollout's window, a row that was not Ready starts a wait of
// its own.
func TestK2Rollout_ALaterWaitIsNotMovedBack(t *testing.T) {
	r := newRestartRig(t, restartT0)
	ready := restartT0.Add(-time.Hour)
	settled(t, r, func() []domain.ReplicaMetrics { return revisedFull(ready, "rev-1") }, decodeVariants("v"))
	for range waitWindow + 2 {
		r.cycle(revisedFull(ready, "rev-2"), decodeVariants("v"))
	}
	notReady := revisedFull(ready, "rev-2")
	notReady[0].Ready = false
	r.cycle(notReady, decodeVariants("v"))
	assert.Equal(t, []string{k2ReasonObsUnsettled}, r.cycle(revisedFull(ready, "rev-2"), decodeVariants("v")))
}

// A new Ready time waits whichever way the switch is: only the revision that
// arrives with the Ready time unchanged depends on it, so the switch adds a
// wait and shortens none.
func TestK2Rollout_ANewReadyTimeWaitsEitherWay(t *testing.T) {
	for _, on := range []bool{true, false} {
		r := newRestartRig(t, restartT0)
		require.NoError(t, r.a.SetSwitchesForReplay(map[string]bool{SwitchK2WaitsAfterRollout: on}))
		ready := restartT0.Add(-time.Hour)
		settled(t, r, func() []domain.ReplicaMetrics { return revisedFull(ready, "rev-1") }, decodeVariants("v"))
		got := r.cycle(revisedFull(r.now, "rev-2"), decodeVariants("v"))
		assert.Equal(t, []string{k2ReasonObsUnsettled}, got, "switch %v", on)
	}
}

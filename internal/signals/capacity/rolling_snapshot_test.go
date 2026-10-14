package capacity

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRollingAverageSnapshotRoundTripsValuesAndBothClocks(t *testing.T) {
	r := NewRollingAverage(4)
	r.Add(4000)
	r.Add(5000)
	used := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	r.TouchAt(used)
	written := r.lastWritten

	got, ok := RestoreRollingAverage(r.Snapshot(), 4)
	require.True(t, ok)
	assert.Equal(t, 4500.0, got.Average())
	assert.Equal(t, used, got.lastUsed, "a restore must not make the window look recently used")
	assert.Equal(t, written, got.lastWritten, "a restore must not make the last reading look recent")
}

func TestSnapshotDoesNotAliasTheLiveWindow(t *testing.T) {
	r := NewRollingAverage(4)
	r.Add(4000)
	s := r.Snapshot()
	r.Add(9000)
	assert.Equal(t, []float64{4000}, s.Values)
}

func TestRestoreKeepsOnlyUsableValues(t *testing.T) {
	now := time.Now()
	got, ok := RestoreRollingAverage(RollingAverageSnapshot{
		Values:   []float64{math.NaN(), -1, 0, math.Inf(1), 4000},
		LastUsed: now, LastWritten: now,
	}, 4)
	require.True(t, ok)
	assert.Equal(t, 1, got.Len(), "a NaN, a negative, a zero and an infinity are not capacities")
	assert.Equal(t, 4000.0, got.Average())
}

func TestRestoreRefusesWhatCannotBeUsedHonestly(t *testing.T) {
	now := time.Now()
	for name, s := range map[string]RollingAverageSnapshot{
		"no usable value":  {Values: []float64{math.NaN()}, LastUsed: now, LastWritten: now},
		"no values":        {LastUsed: now, LastWritten: now},
		"no last-used":     {Values: []float64{4000}, LastWritten: now},
		"no last-written":  {Values: []float64{4000}, LastUsed: now},
	} {
		_, ok := RestoreRollingAverage(s, 4)
		assert.False(t, ok, name)
	}
}

func TestRestoreKeepsTheNewestWhenTheSnapshotIsLonger(t *testing.T) {
	now := time.Now()
	got, ok := RestoreRollingAverage(RollingAverageSnapshot{
		Values: []float64{1, 2, 3, 4, 5}, LastUsed: now, LastWritten: now,
	}, 2)
	require.True(t, ok)
	assert.Equal(t, 4.5, got.Average(), "as Add would have: the oldest go first")
}

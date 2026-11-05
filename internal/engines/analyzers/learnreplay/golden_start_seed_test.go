package learnreplay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startSeedRecording is one start-seed golden's exported recording.
func startSeedRecording(t *testing.T, fixture string) []byte {
	t.Helper()
	gz, err := os.ReadFile(filepath.Join(goldenDir, fixture, RecordingName))
	require.NoError(t, err)
	rec, err := gunzip(gz)
	require.NoError(t, err)
	return rec
}

// The first start-seed golden exercises the seed path: the seeded variant
// carries its 120 s, share pacing reads it until the variant's first Pod is
// Ready, and then reads that Pod's start. The Pods' own timestamps gave 241
// to 242 s; a golden that held none of this would pass every other test.
func TestGoldens_StartSeedHoldsTheSeedAndTheMeasuredStart(t *testing.T) {
	rec := startSeedRecording(t, startSeedFixture)
	assert.Positive(t, bytes.Count(rec, []byte(`"StartSeedSeconds":120`)), "no variant carries the seed")
	assert.Positive(t, bytes.Count(rec, []byte(`"RoleStartSeconds":{"both":120}`)), "share pacing never read the seed")
	assert.Positive(t, bytes.Count(rec, []byte(`"RoleStartSeconds":{"both":241}`)), "share pacing never read the first measured start")
	assert.Zero(t, bytes.Count(rec, []byte(`"Kind":"`+restoreKind+`"`)), "the first controller start restored something")
}

// restoreKind is the event a controller start writes when it restores a
// snapshot.
const restoreKind = "saturation.RestoreLearnedState"

// Each restart golden restores both start keys, at figures the Pods' own
// timestamps agree with: 20 to 22 s for the fast variant, 241 to 242 s for
// the slow one.
func TestGoldens_StartSeedRestartsRestoreBothStartKeys(t *testing.T) {
	for _, fixture := range []string{startSeedRestartFixture, startSeedRestart2Fixture} {
		t.Run(fixture, func(t *testing.T) {
			restored := 0
			sc := bufio.NewScanner(bytes.NewReader(startSeedRecording(t, fixture)))
			sc.Buffer(nil, 1<<26)
			for sc.Scan() {
				var ev struct {
					Kind string
					Data struct {
						State struct{ StartSeconds map[string]float64 }
					}
				}
				require.NoError(t, json.Unmarshal(sc.Bytes(), &ev))
				if ev.Kind != restoreKind {
					continue
				}
				restored++
				figures := slices.Sorted(maps.Values(ev.Data.State.StartSeconds))
				require.Len(t, figures, 2, "both start keys")
				assert.InDelta(t, 21, figures[0], 1, "the fast variant")
				assert.InDelta(t, 241.5, figures[1], 0.5, "the slow variant")
			}
			require.NoError(t, sc.Err())
			assert.Equal(t, 1, restored, "one restore per controller start")
		})
	}
}

package learnreplay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuietTransfers_ByPromptShortestFirst(t *testing.T) {
	gz, err := os.ReadFile(filepath.Join(goldenDir, "ttft-staircase-b-off", RecordingName))
	require.NoError(t, err)
	raw, err := gunzip(gz)
	require.NoError(t, err)
	readings, err := QuietTransfers(raw)
	require.NoError(t, err)
	require.NotEmpty(t, readings)
	for i, r := range readings {
		assert.Positive(t, r.Rows)
		assert.Positive(t, r.Seconds)
		if i > 0 {
			assert.Greater(t, r.PromptTokens, readings[i-1].PromptTokens)
		}
	}
	// The golden's quiet rows: 22 at 8000 tokens. A row with a queue would add to them
	// and raise the figure, as a saturated phase does.
	last := readings[len(readings)-1]
	assert.InDelta(t, 8000, last.PromptTokens, 0)
	assert.Equal(t, 22, last.Rows)
	assert.InDelta(t, 0.03358, last.Seconds, 1e-5)
}

func TestQuietTransfers_NotARecordingIsAnError(t *testing.T) {
	_, err := QuietTransfers([]byte("{not json\n"))
	require.Error(t, err)
}

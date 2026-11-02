package learnreplay

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/saturation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/slo"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learnrec"
)

// The ols-sweep-slo goldens are one cluster run of the ols-sweep recipe with
// the slo analyzer listed beside saturation (a policy tier with TPOT and TTFT
// targets) and a streamed load, so the router measured the latency the slo
// analyzer judges. The leader controller Pod was deleted under load, which
// split the run in two recordings: ols-sweep-slo up to the restart and
// ols-sweep-slo-restart after it (docs/developer-guide/testing.md).
const (
	olsSLOFixture        = "ols-sweep-slo"
	olsSLORestartFixture = "ols-sweep-slo-restart"
)

// olsSLOFloor is what one of the two recordings' own replays must still
// reach. At the commit they were added, each replay took the counts in the
// comment beside its floor, the same as the controller's own log of the run
// (itl-fit tier=ols and tier=pinned-B; slo-variant lineTier=ols and
// pinned-B). The two analyzers fit from the same rows with the same rules,
// each with its own tracker, so their counts agree. A floor sits about a
// fifth under its count: a learner change may move fits (make
// learner-goldens rewrites lines.csv and does not touch these), but must not
// lose the full fits these recordings exist for. A PR that drops a replay
// below a floor says why and lowers it here, keeping the counts as the
// recording-time reference.
type olsSLOFloor struct {
	fixture string
	// satOLS and sloOLS are the fewest full fits of each analyzer's line.
	satOLS, sloOLS int64
	// routerJudged is the fewest slo Analyze calls the router served
	// latency to: TPOT requests and a TTFT histogram both present.
	routerJudged int
	// cut is a time before either analyzer's first fit of any tier, at
	// which the head replay holds none.
	cut time.Time
}

var olsSLOFloors = []olsSLOFloor{
	// Recorded 13:40:07Z to 14:00:43Z: 15 full and 33 pinned fits for each
	// analyzer, the first full one at 13:48:55Z; 79 of 83 slo Analyze calls
	// served router latency.
	{fixture: olsSLOFixture, satOLS: 12, sloOLS: 12, routerJudged: 63,
		cut: time.Date(2026, 11, 2, 13, 48, 0, 0, time.UTC)},
	// Recorded 14:01:02Z to 14:36:35Z, after the restart: 57 full and 66
	// pinned fits for each analyzer, the first full one at 14:06:02Z; all
	// 143 slo Analyze calls served router latency.
	{fixture: olsSLORestartFixture, satOLS: 45, sloOLS: 45, routerJudged: 114,
		cut: time.Date(2026, 11, 2, 14, 5, 0, 0, time.UTC)},
}

// Each ols-sweep-slo recording's own replay takes full fits of both lines,
// the saturation analyzer's and the slo analyzer's, and judges from router
// latency: a cluster golden of the slo analyzer that lost them would pass the
// summed path check and the row comparison, which make learner-goldens
// rewrites to whatever the replay gives. The head before the first fit is the
// negative control: the same recording, cut, reaches none.
func TestGoldens_OLSSweepSLOReplayFitsBothLines(t *testing.T) {
	for _, f := range olsSLOFloors {
		t.Run(f.fixture, func(t *testing.T) {
			rec := readGoldenRecording(t, f.fixture)
			rep, err := Replay(context.Background(), rec, Options{Mode: Normal})
			require.NoError(t, err)
			require.Empty(t, rep.Failed())
			t.Logf("%s: saturation ols %d pinned %d; slo ols %d pinned %d; slo Analyze with router latency %d",
				f.fixture, rep.Paths[saturation.PathITLOLS], rep.Paths[saturation.PathITLPinned],
				rep.Paths[slo.PathITLOLS], rep.Paths[slo.PathITLPinned], sloAnalyzesWithRouterLatency(t, rec))

			assert.GreaterOrEqual(t, rep.Paths[saturation.PathITLOLS], f.satOLS, "saturation's line still comes from full fits")
			assert.GreaterOrEqual(t, rep.Paths[slo.PathITLOLS], f.sloOLS, "the slo analyzer's line still comes from full fits")
			assert.Positive(t, rep.Paths[saturation.PathITLPinned], "pinned saturation fits too: the load holds still between steps")
			assert.Positive(t, rep.Paths[slo.PathITLPinned], "pinned slo fits too")
			assert.GreaterOrEqual(t, sloAnalyzesWithRouterLatency(t, rec), f.routerJudged,
				"the router served TPOT and TTFT to the slo analyzer: the load streamed")

			head := replayHeadBefore(t, rec, f.cut)
			assert.Zero(t, head.Paths[saturation.PathITLOLS], "no saturation full fit before the cut")
			assert.Zero(t, head.Paths[saturation.PathITLPinned], "no saturation pinned fit before the cut")
			assert.Zero(t, head.Paths[slo.PathITLOLS], "no slo full fit before the cut")
			assert.Zero(t, head.Paths[slo.PathITLPinned], "no slo pinned fit before the cut")
			assert.Positive(t, head.Paths[saturation.PathRowsInstantKV], "precondition: the head offers readings to the window")
		})
	}
}

// readGoldenRecording is the named golden's recording, unzipped.
func readGoldenRecording(t *testing.T, fixture string) []byte {
	t.Helper()
	gz, err := os.ReadFile(filepath.Join(goldenDir, fixture, RecordingName))
	require.NoError(t, err)
	rec, err := gunzip(gz)
	require.NoError(t, err)
	return rec
}

// sloAnalyzesWithRouterLatency counts the slo Analyze events of rec that the
// router served latency to: a TPOT request rate and a TTFT histogram.
func sloAnalyzesWithRouterLatency(t *testing.T, rec []byte) int {
	t.Helper()
	read, err := learnrec.Read(rec)
	require.NoError(t, err)
	n := 0
	for _, ev := range read.Events {
		if ev.Kind != slo.KindAnalyze {
			continue
		}
		var a slo.AnalyzeEvent
		require.NoError(t, ev.DecodeData(&a))
		if a.RouterLatency != nil && a.RouterLatency.TPOTCountRate > 0 && len(a.RouterLatency.TTFTBuckets) > 0 {
			n++
		}
	}
	return n
}

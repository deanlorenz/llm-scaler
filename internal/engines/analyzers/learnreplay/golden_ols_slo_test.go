package learnreplay

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
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
// pinned-B; slo-model lines with a finite observed figure). The two analyzers
// fit from the same rows with the same rules, each with its own tracker, so
// their counts agree. A floor sits about a fifth under its count: a learner
// change may move fits and judgements (make learner-goldens rewrites
// lines.csv and slo-published.csv and does not touch these), but must not
// lose the full fits and the judged windows these recordings exist for. A PR
// that drops a replay below a floor says why and lowers it here, keeping the
// counts as the recording-time reference.
type olsSLOFloor struct {
	fixture string
	// satOLS and sloOLS are the fewest full fits of each analyzer's line.
	satOLS, sloOLS int64
	// routerServed is the fewest slo Analyze calls of the recording the
	// router served latency to: TPOT requests and a TTFT histogram both
	// present. An input: only a new recording moves it.
	routerServed int
	// judgedTPOT and judgedTTFT are the fewest objectives of each kind the
	// replay's own slo Analyze calls judged: a finite observed figure, and
	// not excluded. An output: an analyzer change moves it.
	judgedTPOT, judgedTTFT int
	// cut is a time before either analyzer's first fit of any tier. What
	// keeps the head fit-free is the pinned fit declining, not the sample
	// gate: both lines report declined from the time beside each floor until
	// the first full fit, so the head must reach that verdict.
	cut time.Time
}

var olsSLOFloors = []olsSLOFloor{
	// Recorded 13:40:07Z to 14:00:43Z: 15 full and 33 pinned fits for each
	// analyzer, the first full one at 13:48:55Z, declined from 13:43:38Z; 79
	// of 83 slo Analyze calls served router latency, and 55 of them judged
	// TPOT and TTFT (the 24 before 13:47:10Z were excluded as a transient).
	{fixture: olsSLOFixture, satOLS: 12, sloOLS: 12, routerServed: 63, judgedTPOT: 44, judgedTTFT: 44,
		cut: time.Date(2026, 11, 2, 13, 48, 0, 0, time.UTC)},
	// Recorded 14:01:02Z to 14:36:35Z, after the restart: 57 full and 66
	// pinned fits for each analyzer, the first full one at 14:06:02Z,
	// declined from 14:03:17Z; all 143 slo Analyze calls served router
	// latency, and 115 judged TPOT and 113 TTFT.
	{fixture: olsSLORestartFixture, satOLS: 45, sloOLS: 45, routerServed: 114, judgedTPOT: 92, judgedTTFT: 90,
		cut: time.Date(2026, 11, 2, 14, 5, 0, 0, time.UTC)},
}

// Each ols-sweep-slo recording's own replay takes full fits of both lines,
// the saturation analyzer's and the slo analyzer's, and judges TPOT and TTFT
// from router latency: a cluster golden of the slo analyzer that lost them
// would pass the summed path check and the row comparison, which make
// learner-goldens rewrites to whatever the replay gives. The head before the
// first fit is the negative control: the same recording, cut, reaches none.
func TestGoldens_OLSSweepSLOReplayFitsBothLines(t *testing.T) {
	for _, f := range olsSLOFloors {
		t.Run(f.fixture, func(t *testing.T) {
			rec := readGoldenRecording(t, f.fixture)
			rep, err := Replay(context.Background(), rec, Options{Mode: Normal})
			require.NoError(t, err)
			require.Empty(t, rep.Failed())
			judged := sloJudged(t, rep.Recording)
			t.Logf("%s: saturation ols %d pinned %d; slo ols %d pinned %d; slo Analyze with router latency %d; judged tpot %d ttft %d",
				f.fixture, rep.Paths[saturation.PathITLOLS], rep.Paths[saturation.PathITLPinned],
				rep.Paths[slo.PathITLOLS], rep.Paths[slo.PathITLPinned], sloAnalyzesWithRouterLatency(t, rec),
				judged[constants.SLOObjectiveTPOT], judged[constants.SLOObjectiveTTFT])

			assert.GreaterOrEqual(t, rep.Paths[saturation.PathITLOLS], f.satOLS, "saturation's line still comes from full fits")
			assert.GreaterOrEqual(t, rep.Paths[slo.PathITLOLS], f.sloOLS, "the slo analyzer's line still comes from full fits")
			assert.Positive(t, rep.Paths[saturation.PathITLPinned], "pinned saturation fits too: the load holds still between steps")
			assert.Positive(t, rep.Paths[slo.PathITLPinned], "pinned slo fits too")
			assert.GreaterOrEqual(t, sloAnalyzesWithRouterLatency(t, rec), f.routerServed,
				"the router served TPOT and TTFT to the slo analyzer: the load streamed")
			assert.GreaterOrEqual(t, judged[constants.SLOObjectiveTPOT], f.judgedTPOT, "the replay still judges TPOT from the router's latency")
			assert.GreaterOrEqual(t, judged[constants.SLOObjectiveTTFT], f.judgedTTFT, "the replay still judges TTFT from the router's latency")

			head := replayHeadBefore(t, rec, f.cut)
			assert.Zero(t, head.Paths[saturation.PathITLOLS], "no saturation full fit before the cut")
			assert.Zero(t, head.Paths[saturation.PathITLPinned], "no saturation pinned fit before the cut")
			assert.Zero(t, head.Paths[slo.PathITLOLS], "no slo full fit before the cut")
			assert.Zero(t, head.Paths[slo.PathITLPinned], "no slo pinned fit before the cut")
			assert.Positive(t, head.Paths[saturation.PathRowsInstantKV], "precondition: the head offers readings to the window")
			assert.Positive(t, sloDeclined(t, head.Recording),
				"precondition: the head reaches the pinned fit's decline, which is what keeps it fit-free")
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

// sloAnalyzeEvents is every slo Analyze event of rec, decoded.
func sloAnalyzeEvents(t *testing.T, rec []byte) []slo.AnalyzeEvent {
	t.Helper()
	read, err := learnrec.Read(rec)
	require.NoError(t, err)
	var out []slo.AnalyzeEvent
	for _, ev := range read.Events {
		if ev.Kind != slo.KindAnalyze {
			continue
		}
		var a slo.AnalyzeEvent
		require.NoError(t, ev.DecodeData(&a))
		out = append(out, a)
	}
	return out
}

// sloAnalyzesWithRouterLatency counts the slo Analyze events of rec that the
// router served latency to: a TPOT request rate and a TTFT histogram.
func sloAnalyzesWithRouterLatency(t *testing.T, rec []byte) int {
	t.Helper()
	n := 0
	for _, a := range sloAnalyzeEvents(t, rec) {
		if a.RouterLatency != nil && a.RouterLatency.TPOTCountRate > 0 && len(a.RouterLatency.TTFTBuckets) > 0 {
			n++
		}
	}
	return n
}

// sloJudged counts, per objective, what the slo Analyze events of rec
// published as judged: a finite observed figure, not excluded. Read from a
// replay's own recording, it is what the replay judged.
func sloJudged(t *testing.T, rec []byte) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, a := range sloAnalyzeEvents(t, rec) {
		if a.Published == nil {
			continue
		}
		for _, o := range a.Published.Objectives {
			if o.Excluded == "" && !math.IsNaN(o.Observed) && !math.IsInf(o.Observed, 0) {
				out[o.Objective]++
			}
		}
	}
	return out
}

// sloDeclined counts the variants the slo Analyze events of rec priced from
// no line because the pinned fit declined.
func sloDeclined(t *testing.T, rec []byte) int {
	t.Helper()
	n := 0
	for _, a := range sloAnalyzeEvents(t, rec) {
		if a.Published == nil {
			continue
		}
		for _, v := range a.Published.Variants {
			if v.Source == constants.SLOSourceDeclined {
				n++
			}
		}
	}
	return n
}

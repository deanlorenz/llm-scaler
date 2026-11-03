package learnreplay

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/throughput"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learnrec"
)

// The throughput-sweep goldens are one cluster run of the recipe in
// docs/developer-guide/testing.md (learner goldens) with the throughput
// analyzer listed beside saturation: a P/D decode fleet under the closed-loop
// sweep, the leader controller Pod deleted 20 min in, so the run gives two
// recordings, throughput-sweep and throughput-sweep-restart
// (analyzer-learner.md, PR 4b, "Recordings"). They are the only goldens whose
// throughput line sees a real decode engine: warm-up readings from joiners,
// readings above the band, and full fits.
const (
	throughputSweepFixture        = "throughput-sweep"
	throughputSweepRestartFixture = "throughput-sweep-restart"
)

// throughputSweepFloor is which of the throughput line's paths a recording's
// own replay must reach. Each must be above zero (2.5's per-path counters):
// full and pinned fits in both, settling readings in both (joiners, and after
// the restart every replica already serving), and a held slope in the
// restart recording, whose light load follows the restart. A floor is a
// count, not a rate: the fixture's own replay at the commit that adds it
// states the counts beside it, and a PR that lowers one says why.
type throughputSweepFloor struct {
	fixture string
	// paths is the fewest runs of each path, by throughput.PathNames.
	paths map[string]int64
}

var throughputSweepFloors = []throughputSweepFloor{
	{fixture: throughputSweepFixture, paths: map[string]int64{
		throughput.PathITLOLS: 1, throughput.PathITLPinned: 1, throughput.PathITLSettling: 1}},
	{fixture: throughputSweepRestartFixture, paths: map[string]int64{
		throughput.PathITLOLS: 1, throughput.PathITLPinned: 1, throughput.PathITLHeld: 1, throughput.PathITLSettling: 1}},
}

// throughputFloorProblems names each path of floor a replay's counts fall
// short of, sorted by path.
func throughputFloorProblems(f throughputSweepFloor, paths map[string]int64) []string {
	var out []string
	for _, p := range slices.Sorted(maps.Keys(f.paths)) {
		if paths[p] < f.paths[p] {
			out = append(out, fmt.Sprintf("%s: the throughput line's path %s ran %d times, under its floor of %d",
				f.fixture, p, paths[p], f.paths[p]))
		}
	}
	return out
}

// listedFixture reports whether the committed manifest lists a fixture.
func listedFixture(t *testing.T, name string) bool {
	t.Helper()
	m, err := ReadManifest(goldenDir)
	require.NoError(t, err)
	for _, g := range m.Goldens {
		if g.Name == name {
			return true
		}
	}
	return false
}

// Each throughput-sweep recording's own replay reaches every path of its
// floor: a cluster golden of the throughput line that lost its full fits,
// its settling stamp or its held slope would still pass the summed path
// check and the row comparison, which make learner-goldens rewrites. The
// same replay with the line's steady gate off (the parent's line, which
// counts nothing) is the negative control: every floor fails.
//
// Until the recordings land, the test skips: they are taken on a cluster
// with 4b-0 merged (the recipe in testing.md), and land with base goldens
// in their own commit.
func TestGoldens_ThroughputSweepFloors(t *testing.T) {
	for _, f := range throughputSweepFloors {
		t.Run(f.fixture, func(t *testing.T) {
			if !listedFixture(t, f.fixture) {
				t.Skipf("no %s golden yet: the throughput-sweep cluster run (docs/developer-guide/testing.md, learner goldens; "+
					"analyzer-learner.md, PR 4b, Recordings) has not landed. Once %s lists it, this test requires the floors %v",
					f.fixture, ManifestName, f.paths)
			}
			rec := readGoldenRecording(t, f.fixture)
			read, err := learnrec.Read(rec)
			require.NoError(t, err)
			var start learnrec.Start
			require.NoError(t, read.Events[0].DecodeData(&start))
			require.True(t, start.Throughput, "precondition: the run listed the throughput analyzer")

			rep, err := Replay(context.Background(), rec, Options{Mode: Normal})
			require.NoError(t, err)
			require.Empty(t, rep.Failed())
			t.Logf("%s: throughput line paths %v", f.fixture, throughputPaths(rep.Paths))
			assert.Empty(t, throughputFloorProblems(f, rep.Paths))

			off, err := Replay(context.Background(), rec, Options{Mode: Normal, Switches: map[string]map[string]bool{
				PackageThroughput: {throughput.SwitchITLSteadyGate: false}}})
			require.NoError(t, err)
			assert.Len(t, throughputFloorProblems(f, off.Paths), len(f.paths),
				"negative control: the parent's line reaches none of the floor's paths")
		})
	}
}

// throughputPaths is the throughput line's counts out of a replay's paths.
func throughputPaths(paths map[string]int64) map[string]int64 {
	out := map[string]int64{}
	for _, p := range throughput.PathNames() {
		out[p] = paths[p]
	}
	return out
}

// The floor check itself, on a fixture that exists now: mu-paths runs the
// throughput analyzer in-process. With the steady gate on its replay reaches
// full and pinned fits; with it off, neither, and the check names each. Its
// settling count is zero by construction (no joiner warms up past the
// line's admission there), so a floor on it fails on either: the reason the
// required set waits for the recording.
func TestGoldens_TheThroughputFloorCheckFails(t *testing.T) {
	rec := readGoldenRecording(t, "mu-paths")
	floor := throughputSweepFloor{fixture: "mu-paths", paths: map[string]int64{
		throughput.PathITLOLS: 1, throughput.PathITLPinned: 1}}

	on, err := Replay(context.Background(), rec, Options{Mode: Normal})
	require.NoError(t, err)
	t.Logf("mu-paths, line on the tracker: %v", throughputPaths(on.Paths))
	assert.Empty(t, throughputFloorProblems(floor, on.Paths))

	off, err := Replay(context.Background(), rec, Options{Mode: Normal, Switches: map[string]map[string]bool{
		PackageThroughput: {throughput.SwitchITLSteadyGate: false}}})
	require.NoError(t, err)
	t.Logf("mu-paths, the parent's line: %v", throughputPaths(off.Paths))
	got := throughputFloorProblems(floor, off.Paths)
	require.Len(t, got, 2, "%v", got)
	assert.Contains(t, got[0], "mu-paths: the throughput line's path throughput-itl-ols ran 0 times, under its floor of 1")

	settling := throughputSweepFloor{fixture: "mu-paths", paths: map[string]int64{throughput.PathITLSettling: 1}}
	assert.Len(t, throughputFloorProblems(settling, on.Paths), 1, "the in-process goldens stamp no reading settling")
}

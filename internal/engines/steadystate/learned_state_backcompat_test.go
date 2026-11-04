package steadystate

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/saturation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learnrec"
)

// Back-compat fixtures: a learned-state document as v0.10.0 wrote it and one
// as v0.10.1 (and v0.10.2) wrote it, each restored by the current code.
//
// They are HAND-BUILT, not captured from a cluster: no capture of either
// release's ConfigMap exists. Each is built by
// TestLearnedStateFixtures_Generate from the last learned state the mu-paths
// golden recording exported (a real analyzer's state), projected onto the
// frozen v0.10.1 types (learned_state_v0101_test.go) and encoded with
// encoding/json as v0.10.1's encoder does (json.Marshal of learnedStateDoc).
// The v0.10.0 document is the same less itlSlopes, which v0.10.0 did not
// have (v0.10.0, 823ddcd74, differs from v0.10.1 in these types by that field
// alone). Run it with LEARNED_STATE_WRITE_FIXTURES=1 to write them again.
const (
	fixtureDir       = "testdata/learned-state"
	fixtureV0100     = "v0.10.0-v1.json"
	fixtureV0101     = "v0.10.1-v1.json"
	fixtureNamespace = "wva-system"
	fixtureWriter    = "wva-controller-6d4f9c7b8-x2k4p"
	muPathsGolden    = "../analyzers/learnreplay/testdata/goldens/mu-paths/recording.jsonl.gz"
)

// lastRecordedExport is the last ExportLearnedState event in a recording:
// the state and the clock it was exported at.
func lastRecordedExport(t *testing.T, path string) (saturation.LearnedState, time.Time) {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	require.NoError(t, err)
	var last struct {
		Seq  uint64
		Now  time.Time
		Kind string
		Data learnrec.Raw
	}
	found := false
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		if !bytes.Contains(sc.Bytes(), []byte(`"Kind":"saturation.ExportLearnedState"`)) {
			continue
		}
		require.NoError(t, learnrec.Unmarshal(sc.Bytes(), &last))
		found = true
	}
	require.NoError(t, sc.Err())
	require.True(t, found, "no ExportLearnedState event in %s", path)
	var s saturation.LearnedState
	require.NoError(t, last.Data.Decode(&s))
	return s, last.Now
}

// Writes the fixtures, only when asked; otherwise checks that the committed
// ones are what it would write.
func TestLearnedStateFixtures_Generate(t *testing.T) {
	state, at := lastRecordedExport(t, muPathsGolden)
	require.NotEmpty(t, state.ITLSlopes, "precondition: the export has slopes, the field v0.10.0 lacks")
	require.NotEmpty(t, state.K2, "precondition: the export has k2 windows")

	cur, err := json.Marshal(state)
	require.NoError(t, err)
	var old v0101LearnedState
	require.NoError(t, json.Unmarshal(cur, &old))
	doc := v0101LearnedStateDoc{
		Version:   1,
		WrittenAt: at,
		Writer:    v0101LearnedStateWriter{Namespace: fixtureNamespace, Identity: fixtureWriter},
		State:     old,
	}
	v0101, err := json.Marshal(doc)
	require.NoError(t, err)
	doc.State.ITLSlopes = nil
	v0100, err := json.Marshal(doc)
	require.NoError(t, err)

	for name, want := range map[string][]byte{fixtureV0101: v0101, fixtureV0100: v0100} {
		path := filepath.Join(fixtureDir, name)
		if os.Getenv("LEARNED_STATE_WRITE_FIXTURES") == "1" {
			require.NoError(t, os.MkdirAll(fixtureDir, 0o755))
			require.NoError(t, os.WriteFile(path, append(want, '\n'), 0o644))
			t.Logf("wrote %s, %d bytes", path, len(want))
		}
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, string(want), strings.TrimSpace(string(got)), "%s is stale: run with LEARNED_STATE_WRITE_FIXTURES=1", name)
	}
}

// restoreFixture restores the document in file through the engine's own path
// (ConfigMap, decode, analyzer) into a real saturation analyzer whose clock
// reads one minute after the document was written, and returns the document
// as the current code decodes it, the analyzer and the restore's log line.
func restoreFixture(t *testing.T, file string) (learnedStateDoc, *saturation.SaturationAnalyzer, string) {
	t.Helper()
	t.Setenv("POD_NAMESPACE", fixtureNamespace)
	raw, err := os.ReadFile(filepath.Join(fixtureDir, file))
	require.NoError(t, err)
	var doc learnedStateDoc
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Empty(t, doc.rejected(), "the current code rejects the %s document", file)

	now := doc.WrittenAt.Add(time.Minute)
	sat := saturation.NewSaturationAnalyzer(capacity.NewStore()).WithClock(clocktesting.NewFakePassiveClock(now))
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: learnedStateConfigMap, Namespace: config.SystemNamespace()},
		Data:       map[string]string{learnedStateKey(): string(raw)},
	}
	cfg := config.NewTestConfig()
	config.SetLearnedStateSnapshotForTest(cfg, true)
	e := &Engine{Config: cfg, client: fake.NewClientBuilder().WithObjects(cm).Build(), analyzers: testAnalyzers(sat)}

	var lines []string
	ctx := logr.NewContext(context.Background(),
		funcr.New(func(prefix, args string) { lines = append(lines, args) }, funcr.Options{}))
	e.restoreLearnedState(ctx, now)
	var restored string
	for _, l := range lines {
		if strings.Contains(l, "Learned state: snapshot restored") {
			restored = l
		}
	}
	require.NotEmpty(t, restored, "no restore line; logged: %v", lines)
	return doc, sat, restored
}

// assertRestoredWhole checks that a restore took every entry of the
// document: nothing aged, unusable or unattributable, every shared line
// installed, every variant's entries waiting for its first cycle, and the
// analyzer's export holding the document's entries unchanged.
func assertRestoredWhole(t *testing.T, doc learnedStateDoc, sat *saturation.SaturationAnalyzer, line string) {
	t.Helper()
	t.Logf("restore: %s", line)
	for _, want := range []string{`"aged"=0`, `"unusable"=0`, `"unattributable"=0`, `"forgotten"=0`} {
		assert.Contains(t, line, want)
	}
	assert.Contains(t, line, `"itlLines"=`+strconv.Itoa(len(doc.State.ITLLines)))
	assert.Contains(t, line, `"pendingVariants"=`+strconv.Itoa(len(doc.State.Config)))

	exp := sat.ExportLearnedState()
	assert.Equal(t, doc.State.K2, exp.K2, "k2")
	assert.Equal(t, doc.State.Throughput, exp.Throughput, "throughput")
	assert.Equal(t, doc.State.ITLBaseline, exp.ITLBaseline, "itlBaseline")
	assert.Equal(t, doc.State.StartSeconds, exp.StartSeconds, "startSeconds")
	assert.Equal(t, doc.State.VariantSeenAt, exp.VariantSeenAt, "variantSeenAt")
	assert.Equal(t, doc.State.Accelerators, exp.Accelerators, "accelerators")
	assert.Equal(t, doc.State.ITLLines, exp.ITLLines, "itlLines")
	assert.Equal(t, doc.State.Config, exp.Config, "config")
	assert.Equal(t, doc.State.Args, exp.Args, "args")
}

// A v0.10.0 document, which has no itlSlopes, restores whole; the ITL lines
// and baselines come back, and no slope is invented.
func TestLearnedStateFixture_V0100Restores(t *testing.T) {
	doc, sat, line := restoreFixture(t, fixtureV0100)
	require.NotEmpty(t, doc.State.K2, "precondition: the fixture holds windows")
	require.Empty(t, doc.State.ITLSlopes, "precondition: a v0.10.0 document has no slopes")
	assertRestoredWhole(t, doc, sat, line)
	assert.Empty(t, sat.ExportLearnedState().ITLSlopes, "no slope came from a document that had none")
}

// A v0.10.1 document restores whole, its fitted slopes included.
func TestLearnedStateFixture_V0101Restores(t *testing.T) {
	doc, sat, line := restoreFixture(t, fixtureV0101)
	require.NotEmpty(t, doc.State.ITLSlopes, "precondition: a v0.10.1 document has slopes")
	assertRestoredWhole(t, doc, sat, line)
	assert.Equal(t, doc.State.ITLSlopes, sat.ExportLearnedState().ITLSlopes, "itlSlopes")
}

// Each fixture is a document v0.10.1's decoder reads with no field to spare:
// decoded strictly into the frozen types, nothing is unknown. So neither
// fixture carries a field the release it stands for could not have written.
func TestLearnedStateFixtures_AreV0101Shaped(t *testing.T) {
	for _, name := range []string{fixtureV0100, fixtureV0101} {
		raw, err := os.ReadFile(filepath.Join(fixtureDir, name))
		require.NoError(t, err)
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var doc v0101LearnedStateDoc
		require.NoError(t, dec.Decode(&doc), name)
		assert.Equal(t, 1, doc.Version, name)
	}
}

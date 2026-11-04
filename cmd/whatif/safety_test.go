package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/learnreplay"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learnrec"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/whatifsource"
)

// runCLIWarn runs whatif with args and returns its output, its warnings and
// its error.
func runCLIWarn(ctx context.Context, args ...string) (string, string, error) {
	var out, warn bytes.Buffer
	err := run(ctx, args, &out, &warn)
	return out.String(), warn.String(), err
}

// authSeen is an https server that records the Authorization header of each
// request it answers.
type authSeen struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newAuthSeen(t *testing.T, next http.HandlerFunc) *authSeen {
	t.Helper()
	a := &authSeen{}
	a.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.seen = append(a.seen, r.Header.Get("Authorization"))
		a.mu.Unlock()
		next(w, r)
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *authSeen) headers() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seen...)
}

// caFile writes srv's certificate as a PEM file.
func caFile(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))
	return path
}

// The bearer token goes to Prometheus' own host, and not to the host a
// redirect points at.
func TestClient_TheBearerTokenStaysOnItsHost(t *testing.T) {
	other := newAuthSeen(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	})
	prom := newAuthSeen(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	})
	token := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(token, []byte("s3cret\n"), 0o600))
	f := captureFlags{prometheus: prom.URL, tokenFile: token, caFile: caFile(t, prom.Server), timeout: 10 * time.Second}
	api, err := f.client(&bytes.Buffer{})
	require.NoError(t, err)
	_, _, err = api.QueryRange(context.Background(), "up", promv1.Range{Start: cliFrom, End: cliFrom.Add(time.Minute), Step: 15 * time.Second})
	require.NoError(t, err)
	require.NotEmpty(t, prom.headers())
	for _, h := range prom.headers() {
		assert.Equal(t, "Bearer s3cret", h, "Prometheus' own host gets the token")
	}
	require.NotEmpty(t, other.headers(), "the redirect was not followed")
	for _, h := range other.headers() {
		assert.Empty(t, h, "the token went to the host a redirect named")
	}
}

// A bearer token for a plain-http Prometheus is refused, not dropped.
func TestCapture_ATokenForPlainHTTPIsRefused(t *testing.T) {
	srv := fleetServer(t)
	token := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(token, []byte("s3cret"), 0o600))
	_, err := runCLI(captureArgs(srv.URL, filepath.Join(t.TempDir(), "c.gz"), "--bearer-token-file", token)...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https Prometheus only")
	assert.Equal(t, 2, exitCode(err))
	assert.Empty(t, srv.Requests(), "refused before any query")
}

// --insecure-skip-verify says so on stderr.
func TestClient_InsecureSkipVerifyWarns(t *testing.T) {
	var warn bytes.Buffer
	_, err := captureFlags{prometheus: "https://prom.example:9090", insecure: true, timeout: time.Second}.client(&warn)
	require.NoError(t, err)
	assert.Contains(t, warn.String(), "--insecure-skip-verify")
	assert.Contains(t, warn.String(), "prom.example:9090")
	warn.Reset()
	_, err = captureFlags{prometheus: "https://prom.example:9090", timeout: time.Second}.client(&warn)
	require.NoError(t, err)
	assert.Empty(t, warn.String())
}

// Every request has a timeout, which --timeout sets.
func TestClient_ARequestTimesOut(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	api, err := captureFlags{prometheus: slow.URL, timeout: 200 * time.Millisecond}.client(&bytes.Buffer{})
	require.NoError(t, err)
	began := time.Now()
	_, _, err = api.QueryRange(context.Background(), "up", promv1.Range{Start: cliFrom, End: cliFrom, Step: time.Second})
	require.Error(t, err)
	assert.Less(t, time.Since(began), 3*time.Second)
	_, err = runCLI(captureArgs(slow.URL, filepath.Join(t.TempDir(), "c.gz"), "--timeout", "0s")...)
	require.Error(t, err)
	assert.Equal(t, 2, exitCode(err))
}

// --batch-cap beside the engine's own batch flag in --engine-args is warned
// about.
func TestCapture_BatchCapTwiceWarns(t *testing.T) {
	srv := fleetServer(t)
	_, warn, err := runCLIWarn(context.Background(), captureArgs(srv.URL, filepath.Join(t.TempDir(), "c.gz"),
		"--batch-cap", "32", "--engine-args", "--max-num-seqs=64 --block-size=16")...)
	require.NoError(t, err)
	assert.Contains(t, warn, "--batch-cap 32 and --max-num-seqs in --engine-args")
	_, warn, err = runCLIWarn(context.Background(), captureArgs(srv.URL, filepath.Join(t.TempDir(), "c.gz"),
		"--batch-cap", "32", "--engine-args", "--block-size=16")...)
	require.NoError(t, err)
	assert.Empty(t, warn)
}

// --role-of selectors whose name prefixes nest exit 2.
func TestCapture_NestedRolePrefixesExitTwo(t *testing.T) {
	srv := fleetServer(t)
	out := filepath.Join(t.TempDir(), "c.gz")
	_, err := runCLI(captureArgs(srv.URL, out, "--role-of", "prefill=app=m-dec-pre")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlap")
	assert.Equal(t, 2, exitCode(err))
	assert.Empty(t, srv.Requests())
}

// A failed range query exits 1, names the query, and does not sit in the
// collector's backoff.
func TestCapture_AFailedHistogramQueryExitsOneAtOnce(t *testing.T) {
	srv := fleetServer(t)
	reg := source.NewSourceRegistry()
	require.NoError(t, reg.Register("prometheus", source.NewNoOpSource()))
	registration.RegisterSaturationQueries(reg)
	registration.RegisterQueueingModelQueries(reg)
	registration.RegisterSLOQueries(reg)
	ttft, err := reg.Get("prometheus").QueryList().Build(registration.QueryTTFTBuckets, map[string]string{source.ParamNamespace: "llm"})
	require.NoError(t, err)
	srv.Fail(ttft, "query processing would load too many samples into memory in query execution")
	out := filepath.Join(t.TempDir(), "c.gz")
	began := time.Now()
	_, err = runCLI(captureArgs(srv.URL, out)...)
	require.Error(t, err)
	assert.Less(t, time.Since(began), 3*time.Second)
	assert.Contains(t, err.Error(), "too many samples")
	assert.Contains(t, err.Error(), fmt.Sprintf("%q", ttft))
	assert.Equal(t, 1, exitCode(err))
	_, statErr := os.Stat(out)
	assert.True(t, os.IsNotExist(statErr))
}

// The capture's flags survive capture, generate and a Fidelity replay of
// the generated recording: a golden made from it shows them.
func TestCapture_FlagsSurviveGenerateAndReplay(t *testing.T) {
	srv := fleetServer(t)
	dir := t.TempDir()
	capture := filepath.Join(dir, "run.capture.jsonl.gz")
	_, err := runCLI(captureArgs(srv.URL, capture)...)
	require.NoError(t, err)
	raw, err := readMaybeGzipped(capture)
	require.NoError(t, err)
	read, err := learnrec.Read(raw)
	require.NoError(t, err)
	var window learnreplay.CaptureWindow
	require.NoError(t, read.Events[1].DecodeData(&window))
	require.Contains(t, window.Flags, whatifsource.FlagRolesInferred)
	require.Contains(t, window.Flags, whatifsource.FlagNoRollouts)

	recording := filepath.Join(dir, "run.jsonl")
	text, err := runCLI("generate", "-in", capture, "-o", recording)
	require.NoError(t, err)
	assert.Contains(t, text, "caveat: "+whatifsource.FlagNoRollouts)
	b, err := os.ReadFile(recording)
	require.NoError(t, err)
	start, err := startOf(b)
	require.NoError(t, err)
	assert.Equal(t, window.Flags, start.Caveats)

	rep, err := learnreplay.Replay(context.Background(), b, learnreplay.Options{Mode: learnreplay.Fidelity})
	require.NoError(t, err)
	assert.Empty(t, rep.Failed())
	again, err := startOf(rep.Recording)
	require.NoError(t, err)
	assert.Equal(t, window.Flags, again.Caveats)

	exported, _, err := learnreplay.Export(b, learnreplay.ExportOptions{})
	require.NoError(t, err, "the export refuses a caveat's words")
	pseudo, err := startOf(exported)
	require.NoError(t, err)
	assert.Equal(t, window.Flags, pseudo.Caveats)
}

// generate exits 2 for a capture it cannot read, and 1 for a failure that
// is not the capture's.
func TestGenerate_ExitCodes(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage")
	require.NoError(t, os.WriteFile(garbage, []byte("not a capture\n"), 0o600))
	_, err := runCLI("generate", "-in", garbage, "-o", filepath.Join(dir, "out.jsonl"))
	require.Error(t, err)
	assert.Equal(t, 2, exitCode(err))

	srv := fleetServer(t)
	capture := filepath.Join(dir, "c.gz")
	_, err = runCLI(captureArgs(srv.URL, capture)...)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = runCLIWarn(ctx, "generate", "-in", capture, "-o", filepath.Join(dir, "out.jsonl"))
	require.Error(t, err)
	assert.Equal(t, 1, exitCode(err), "%v", err)
}

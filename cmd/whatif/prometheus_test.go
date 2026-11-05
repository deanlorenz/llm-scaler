package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/whatifsource"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/whatifsource/promtest"
)

// The --prometheus mode (the planner's D4) is an adapter: capture, then
// generate, then answer, in memory. These tests hold it to the files: the
// same window answered through capture -o, generate -o and --recording is
// the same answer, byte for byte.

const (
	sweepNS    = "llm"
	sweepSteps = 120
	sweepStep  = 15 * time.Second
	sweepPods  = 3
)

var sweepFrom = time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)

// sweepStepOf is the step an instant falls on, ok false off the grid or
// past the window.
func sweepStepOf(at time.Time) (int, bool) {
	d := at.Sub(sweepFrom)
	if d < 0 || d%sweepStep != 0 {
		return 0, false
	}
	return int(d / sweepStep), d/sweepStep < sweepSteps
}

// sweepLevels is the fill the fleet holds, one level per sweepHold steps,
// so each level settles and the ITL line has a span to fit over.
var sweepLevels = []float64{0.2, 0.35, 0.5, 0.65, 0.5, 0.35}

const sweepHold = 10

// sweepKV is pod j's KV utilization at step i: the level, each pod a little
// apart.
func sweepKV(j, i int) float64 {
	return sweepLevels[(i/sweepHold)%len(sweepLevels)] + 0.02*float64(j)
}

// sweepITL is the ITL at KV fill k: a line, 6 ms at no load and 20 ms more
// at a full cache.
func sweepITL(k float64) float64 { return 0.006 + 0.02*k }

// sweepServer is a Prometheus holding a window of one decode variant of
// model m, three Pods sweeping their fill, with every series the decode
// pricing reads.
func sweepServer(t *testing.T) *promtest.Server {
	t.Helper()
	srv := promtest.New()
	t.Cleanup(srv.Close)
	reg := source.NewSourceRegistry()
	require.NoError(t, reg.Register("prometheus", source.NewNoOpSource()))
	registration.RegisterSaturationQueries(reg)
	registration.RegisterQueueingModelQueries(reg)
	registration.RegisterSLOQueries(reg)
	registration.RegisterFlowControlPoolQueries(reg)
	registration.RegisterAlwaysOnQueries(reg)
	q := func(name string) string {
		params := map[string]string{source.ParamNamespace: sweepNS}
		if qt := reg.Get("prometheus").QueryList().Get(name); qt != nil && len(qt.Params) == 0 {
			params = nil
		}
		s, err := reg.Get("prometheus").QueryList().Build(name, params)
		require.NoError(t, err, name)
		return s
	}
	for j := range sweepPods {
		labels := map[string]string{"model_name": "m", "pod": fmt.Sprintf("m-dec-7c9d-%d", j),
			"instance": fmt.Sprintf("10.0.0.%d:8000", j+1), "namespace": sweepNS}
		with := func(k, v string) map[string]string {
			l := map[string]string{}
			for a, b := range labels {
				l[a] = b
			}
			l[k] = v
			return l
		}
		at := func(f func(i int) float64) func(time.Time) (float64, bool) {
			return func(ts time.Time) (float64, bool) {
				i, ok := sweepStepOf(ts)
				if !ok {
					return 0, false
				}
				return f(i), true
			}
		}
		kv := func(i int) float64 { return sweepKV(j, i) }
		itl := func(i int) float64 { return sweepITL(kv(i)) }
		c := func(x float64) func(int) float64 { return func(int) float64 { return x } }
		add := func(name string, l map[string]string, f func(i int) float64) {
			srv.Add(q(name), promtest.Series{Labels: l, Value: at(f)})
		}
		add(registration.QueryKvCacheUsage, labels, kv)
		add(registration.QueryMaxRankKvCacheUsage, labels, kv)
		add(registration.QueryQueueLength, labels, c(0))
		add(registration.QueryMaxRankQueueLength, labels, c(0))
		add(registration.QueryCapacityQueueLength, labels, c(0))
		add(registration.QueryMaxRankCapacityQueueLength, labels, c(0))
		srv.Add(q(registration.QueryCacheConfigInfo), promtest.Series{Labels: map[string]string{
			"instance": labels["instance"], "pod": labels["pod"], "num_gpu_blocks": "8192", "block_size": "16"},
			Value: at(c(1))})
		add(registration.QueryAvgOutputTokens, labels, c(1000))
		add(registration.QueryAvgOutputTokensRecent, labels, c(1000))
		add(registration.QueryAvgInputTokens, labels, c(1000))
		add(registration.QueryAvgInputTokensRecent, labels, c(1000))
		add(registration.QueryPrefixCacheHitRate, labels, c(0))
		add(registration.QueryAvgITL, labels, itl)
		running := func(i int) float64 { return math.Round(kv(i) * 64) }
		add(registration.QueryRunningRequests, labels, running)
		add(registration.QueryGenerationTokenRate, labels, func(i int) float64 { return running(i) / itl(i) })
		add(registration.QueryRequestRate, labels, func(i int) float64 { return running(i) / itl(i) / 1000 })
		add(registration.QueryAvgTTFT, labels, c(0.2))
		add(registration.QueryAvgServiceTime, labels, func(i int) float64 { return 1000 * itl(i) })
		add(registration.QueryKvUsageInstant, labels, kv)
		add(registration.QueryKvUsageMean, labels, kv)
		add(registration.QueryPreemptionRate, labels, c(0))
		add(registration.QueryTPOTBuckets, with("le", "0.02"), c(0.5))
		add(registration.QueryTPOTBuckets, with("le", "0.05"), c(1))
		add(registration.QueryTPOTBuckets, with("le", "+Inf"), c(1))
	}
	return srv
}

// sweepWindow is the --prometheus flags of the sweep's window.
func sweepWindow(prom string) []string {
	return []string{"--prometheus", prom, "--namespace", sweepNS, "--role-of", "decode=app=m-dec",
		"--from", sweepFrom.Format(time.RFC3339), "--to", sweepFrom.Add((sweepSteps - 1) * sweepStep).Format(time.RFC3339),
		"--batch-cap", "64"}
}

// filesAnswer answers question the long way: whatif capture -o, whatif
// generate -o, then the question with --recording, each a command line of
// its own. window is the --prometheus flags, which capture takes as they
// are.
func filesAnswer(t *testing.T, window, question []string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	capture, recording := filepath.Join(dir, "run.capture.jsonl.gz"), filepath.Join(dir, "run.jsonl")
	_, err := runCLI(slices.Concat([]string{"capture"}, window, []string{"-o", capture})...)
	require.NoError(t, err)
	_, err = runCLI("generate", "-in", capture, "-o", recording)
	require.NoError(t, err)
	return runCmd(t, slices.Concat(question[:1], []string{"--recording", recording, "--namespace", sweepNS}, question[1:])...)
}

// sameAnswer reports how two JSON outputs differ, "" when they do not: every
// key byte for byte, but for where the source says it came from (the
// recording's path, or the Prometheus window), which names the source and
// not the answer.
func sameAnswer(t *testing.T, files, prom string) string {
	t.Helper()
	var a, b map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(files), &a), files)
	require.NoError(t, json.Unmarshal([]byte(prom), &b), prom)
	for _, k := range []string{"source"} {
		var sa, sb map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(a[k], &sa))
		require.NoError(t, json.Unmarshal(b[k], &sb))
		require.NotEmpty(t, sa["recording"], "the files' source names no recording")
		require.NotEmpty(t, sb["window"], "the --prometheus source names no window")
		for _, s := range []map[string]json.RawMessage{sa, sb} {
			delete(s, "recording")
			delete(s, "window")
		}
		ja, _ := json.Marshal(sa)
		jb, _ := json.Marshal(sb)
		a[k], b[k] = ja, jb
	}
	var diffs []string
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range a {
			if !yield(k) {
				return
			}
		}
	}) {
		if !bytes.Equal(a[k], b[k]) {
			diffs = append(diffs, fmt.Sprintf("%s:\nfiles: %s\nprometheus: %s", k, a[k], b[k]))
		}
	}
	if len(a) != len(b) {
		diffs = append(diffs, fmt.Sprintf("%d keys, %d keys", len(a), len(b)))
	}
	return strings.Join(diffs, "\n")
}

// identityCases is question flags the identity is held on: both questions,
// answers and refusals, the recorded shape and an assumed one, an earlier
// cycle and a priced replica count.
var identityCases = []struct {
	name string
	args []string
	code int
}{
	{"replicas-to-slo", []string{"replicas-to-slo"}, 0},
	{"replicas-to-slo at a rate", []string{"replicas-to-slo", "--rate", "5", "--decode", "2"}, 0},
	{"replicas-to-slo at cycle 90", []string{"replicas-to-slo", "--cycle", "90"}, 0},
	{"replicas-to-slo, an assumed ratio", []string{"replicas-to-slo", "--tpot-percentile", "90", "--assume-tpot-ratio", "1.3",
		"--assume-output-tokens", "500"}, 0},
	{"replicas-to-slo refused: no TPOT histogram", []string{"replicas-to-slo", "--tpot-percentile", "90"}, 3},
	{"slo-to-rate", []string{"slo-to-rate", "--tpot", "15ms"}, 0},
	{"slo-to-rate, a headroom", []string{"slo-to-rate", "--tpot", "12ms", "--headroom", "0.6", "--seed", "7"}, 0},
	{"slo-to-rate refused: below the floor", []string{"slo-to-rate", "--tpot", "5ms"}, 3},
}

// The --prometheus answer is the answer capture, generate and --recording
// give on the same window, byte for byte, in JSON and in text: the mode is
// an adapter, and prices nothing itself.
func TestPrometheus_TheAnswerIsTheFilesAnswer(t *testing.T) {
	srv := sweepServer(t)
	window := sweepWindow(srv.URL)
	for _, c := range identityCases {
		t.Run(c.name, func(t *testing.T) {
			for _, output := range []string{outputJSON, outputText} {
				q := slices.Concat(c.args, []string{"-o", output})
				files, fileCode := filesAnswer(t, window, q)
				prom, promCode := runCmd(t, slices.Concat(c.args[:1], window, c.args[1:], []string{"-o", output})...)
				require.Equal(t, c.code, fileCode, "the files' exit code\n%s", files)
				require.Equal(t, fileCode, promCode, "exit codes differ\n%s", prom)
				if output == outputText {
					assert.Equal(t, files, prom, "the text differs")
					continue
				}
				if d := sameAnswer(t, files, prom); d != "" {
					t.Errorf("the --prometheus answer is not the files' answer:\n%s", d)
				}
			}
		})
	}
	// The window answered something: a line was learned and priced.
	out, code := runCmd(t, slices.Concat([]string{"slo-to-rate"}, window, []string{"--tpot", "15ms", "-o", "json"})...)
	require.Equal(t, 0, code, out)
	w := decodeOutput(t, out)
	require.Len(t, w.Roles, 1)
	require.NotNil(t, w.Roles[0].Results.Rate.Answer, "no answer to compare:\n%s", out)
	assert.Equal(t, "ols", w.Roles[0].Inputs.Line.Tier)
	assert.Equal(t, int64(64), w.Roles[0].Inputs.BatchCap, "--batch-cap is the captured engine's cap")
	assert.Equal(t, sweepSteps, w.Source.TotalCycles)
}

// The negative control: the comparison sees a difference when there is one.
// The files' capture is made without the --batch-cap the --prometheus run
// captures with, so the engine prices another cap, and the answers differ.
func TestPrometheus_TheIdentityCheckSeesADifference(t *testing.T) {
	srv := sweepServer(t)
	window := sweepWindow(srv.URL)
	other := slices.Clone(window)
	i := slices.Index(other, "--batch-cap")
	require.GreaterOrEqual(t, i, 0)
	other[i+1] = "32"
	q := []string{"slo-to-rate", "--tpot", "15ms", "-o", "json"}
	files, _ := filesAnswer(t, other, q)
	prom, _ := runCmd(t, slices.Concat(q[:1], window, q[1:])...)
	d := sameAnswer(t, files, prom)
	assert.Contains(t, d, "roles", "a capture at another batch cap answered the same")

	// And a window one step shorter: another recording, another answer.
	shorter := slices.Clone(window)
	j := slices.Index(shorter, "--to")
	shorter[j+1] = sweepFrom.Add((sweepSteps - 2) * sweepStep).Format(time.RFC3339)
	files, _ = filesAnswer(t, shorter, q)
	assert.NotEmpty(t, sameAnswer(t, files, prom), "a shorter window answered the same")
}

// The source says which window the answer was captured from; a password in
// the URL is not printed.
func TestPrometheus_TheSourceNamesTheWindow(t *testing.T) {
	srv := sweepServer(t)
	withUser := strings.Replace(srv.URL, "http://", "http://reader:hunter2@", 1)
	out, code := runCmd(t, slices.Concat([]string{"replicas-to-slo"}, sweepWindow(withUser), []string{"--step", "15s", "-o", "json"})...)
	require.Equal(t, 0, code, out)
	assert.NotContains(t, out, "hunter2")
	w := decodeOutput(t, out)
	require.NotNil(t, w.Source)
	require.NotNil(t, w.Source.Window)
	assert.Empty(t, w.Source.Recording)
	win := w.Source.Window
	assert.Equal(t, strings.Replace(withUser, "hunter2", "xxxxx", 1), win.Prometheus)
	assert.True(t, win.From.Equal(sweepFrom), "from %s", win.From)
	assert.True(t, win.To.Equal(sweepFrom.Add((sweepSteps-1)*sweepStep)), "to %s", win.To)
	assert.Equal(t, 15.0, win.StepSeconds)
	assert.Equal(t, "role-of", win.Roles)
	assert.Equal(t, sweepNS, w.Source.Namespace)

	// A recording's source has no window.
	files, _ := filesAnswer(t, sweepWindow(srv.URL), []string{"replicas-to-slo", "-o", "json"})
	fw := decodeOutput(t, files)
	assert.Nil(t, fw.Source.Window)
	assert.NotEmpty(t, fw.Source.Recording)
}

// Each exit code of the --prometheus mode is capture's or the question's:
// 2 for a usage error or the caller's input (a window too large without
// --force, a role map that attributes nothing), 1 for a failed query, 3
// for a refusal, 0 answered. With -o json each error is one JSON object.
func TestPrometheus_ExitCodes(t *testing.T) {
	srv := sweepServer(t)
	failing := sweepServer(t)
	failing.Fail(queryOf(t, registration.QueryKvCacheUsage), "query processing would load too many samples")
	from, to := sweepFrom.Format(time.RFC3339), sweepFrom.Add(time.Hour).Format(time.RFC3339)
	recording := filepath.Join(t.TempDir(), "r.jsonl")
	require.NoError(t, os.WriteFile(recording, []byte("{}\n"), 0o600))
	q2 := func(window []string, extra ...string) []string {
		return slices.Concat([]string{"replicas-to-slo"}, window, extra)
	}
	for _, c := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"answered", q2(sweepWindow(srv.URL)), 0, ""},
		{"refused", slices.Concat([]string{"slo-to-rate"}, sweepWindow(srv.URL), []string{"--tpot", "5ms"}), 3, "TargetBelowFloor"},
		{"usage: neither source", []string{"replicas-to-slo"}, 2, "--recording or --prometheus is required"},
		{"usage: both sources", q2(sweepWindow(srv.URL), "--recording", recording), 2, "give one"},
		{"usage: a window flag without --prometheus", []string{"replicas-to-slo", "--recording", recording, "--from", from}, 2,
			"--from is for --prometheus"},
		{"usage: --force without --prometheus", []string{"replicas-to-slo", "--recording", recording, "--force"}, 2,
			"--force is for --prometheus"},
		{"usage: no namespace", q2([]string{"--prometheus", srv.URL, "--role-of", "decode=app=m-dec", "--from", from, "--to", to}), 2,
			"replicas-to-slo needs --namespace"},
		{"usage: no window", q2([]string{"--prometheus", srv.URL, "--namespace", sweepNS, "--role-of", "decode=app=m-dec"}), 2,
			"needs --from and --to"},
		{"usage: no role map", q2([]string{"--prometheus", srv.URL, "--namespace", sweepNS, "--from", from, "--to", to}), 2,
			"cannot tell prefill from decode"},
		{"usage: a bad URL", q2(sweepWindow("ftp://prom")), 2, "not an http or https URL"},
		{"usage: a token over http", q2(sweepWindow(srv.URL), "--bearer-token-file", recording), 2, "https Prometheus only"},
		{"usage: a negative --batch-cap", q2(sweepWindow(srv.URL), "--batch-cap", "-1"), 2, "--batch-cap -1"},
		{"usage: TTFT is not modelled", q2(sweepWindow(srv.URL), "--prefill", "1"), 2, "not supported"},
		{"input: nothing attributed", q2([]string{"--prometheus", srv.URL, "--namespace", sweepNS, "--role-of", "decode=app=nothing",
			"--from", from, "--to", to}), 2, "no Pod the role map attributes"},
		{"input: a window too large", q2([]string{"--prometheus", srv.URL, "--namespace", sweepNS, "--role-of", "decode=app=m-dec",
			"--from", from, "--to", sweepFrom.Add(1_000_000 * time.Second).Format(time.RFC3339), "--step", "1s"}), 2, "--force"},
		{"input: a model the window lacks", q2(sweepWindow(srv.URL), "--model", "nope"), 2, "nope"},
		{"input: a namespace the recording lacks", []string{"replicas-to-slo", "--recording", olsSweep, "--namespace", "nope"}, 2, "holds no saturation cycle"},
		{"query failure", q2(sweepWindow(failing.URL)), 1, "too many samples"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(context.Background(), c.args, &out, io.Discard)
			code := 0
			if err != nil {
				code = exitCode(err)
			}
			text := out.String()
			if err != nil {
				text += err.Error()
			}
			assert.Equal(t, c.code, code, "%s", text)
			assert.Contains(t, text, c.want)
			if c.code == 1 {
				assert.Equal(t, 1, strings.Count(err.Error(), "capture:"), "one prefix: %v", err)
				assert.True(t, errors.Is(err, whatifsource.ErrQueryFailed), "%v", err)
			}

			js, jsCode := runCmd(t, append(slices.Clone(c.args), "-o", "json")...)
			assert.Equal(t, c.code, jsCode)
			w := decodeOutput(t, js)
			assert.Equal(t, c.code, w.ExitCode)
			if c.code == 1 || c.code == 2 {
				require.NotNil(t, w.Error, js)
				assert.Contains(t, w.Error.Message, c.want)
				assert.Nil(t, w.Source, "an error object holds no source")
			} else {
				assert.Nil(t, w.Error, js)
			}
		})
	}
	// The window too large was refused before anything was asked.
	for _, r := range srv.Requests() {
		assert.False(t, r.End.Equal(sweepFrom.Add(1_000_000*time.Second)), "the window too large was asked")
	}
}

// --force reaches the capture: the window bound is lifted, as capture's
// --force lifts it.
func TestPrometheus_ForceReachesTheCapture(t *testing.T) {
	for _, force := range []bool{false, true} {
		fs := newFlags("replicas-to-slo", io.Discard)
		f := answerFlags{question: "replicas-to-slo", warn: io.Discard}
		f.register(fs)
		args := sweepWindow("http://prom.example:9090")
		if force {
			args = append(args, "--force")
		}
		ok, err := parse(fs, args)
		require.True(t, ok)
		require.NoError(t, err)
		require.NoError(t, f.check(fs))
		opts, err := f.window.options(f.question, f.warn)
		require.NoError(t, err)
		assert.Equal(t, force, opts.Force)
		assert.Equal(t, 64, opts.BatchCap, "--batch-cap is the capture's")
		assert.Zero(t, f.batchCap, "--batch-cap is not also a cap to price at")
		assert.Equal(t, defaultTimeout, f.window.timeout)
	}
}

// A capture's warnings go to stderr, so that -o json is still one object.
func TestPrometheus_WarningsGoToStderr(t *testing.T) {
	srv := sweepServer(t)
	var out, warn bytes.Buffer
	args := slices.Concat([]string{"replicas-to-slo"}, sweepWindow(srv.URL), []string{"--engine-args", "--max-num-seqs=64", "-o", "json"})
	require.NoError(t, run(context.Background(), args, &out, &warn))
	assert.Contains(t, warn.String(), "both set the batch cap")
	decodeOutput(t, out.String())
}

// Help lists the --prometheus mode's flags beside the question's.
func TestPrometheus_HelpListsTheWindowFlags(t *testing.T) {
	for _, cmd := range []string{"replicas-to-slo", "slo-to-rate"} {
		text, code := runCmd(t, cmd, "-h")
		require.Equal(t, 0, code)
		for _, name := range windowFlags {
			assert.Regexp(t, "(?m)^  -"+name+"( |$)", text, "%s help lists -%s", cmd, name)
		}
		assert.Contains(t, text, "-recording")
	}
}

// queryOf is a registered query's PromQL in the sweep's namespace.
func queryOf(t *testing.T, name string) string {
	t.Helper()
	reg := source.NewSourceRegistry()
	require.NoError(t, reg.Register("prometheus", source.NewNoOpSource()))
	registration.RegisterSaturationQueries(reg)
	s, err := reg.Get("prometheus").QueryList().Build(name, map[string]string{source.ParamNamespace: sweepNS})
	require.NoError(t, err)
	return s
}

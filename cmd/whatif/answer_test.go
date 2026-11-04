package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden outputs in testdata")

// olsSweep is the committed recording the page's worked example is read
// from (docs/proposals/slo-capacity-planner.md, "Worked example: ols-sweep").
const olsSweep = "../../internal/engines/analyzers/learnreplay/testdata/goldens/ols-sweep/recording.jsonl.gz"

// workedShape is the example's stated traffic: IL = OL = 1000. The
// recording's last cycle measured OL near 4000, so the example assumes it.
var workedShape = []string{"--assume-input-tokens", "1000", "--assume-output-tokens", "1000"}

// runCmd runs one command line and returns its output and exit code.
func runCmd(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	err := run(context.Background(), args, &out)
	if err == nil {
		return out.String(), 0
	}
	return out.String(), exitCode(err)
}

// The golden outputs: each command line's output, byte for byte, and its exit
// code. go test ./cmd/whatif -run TestGolden -update rewrites them.
func TestGolden(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"replicas-to-slo-worked", slices.Concat([]string{"replicas-to-slo", "--recording", olsSweep,
			"--cycle", "last", "--model", "model-02", "--decode", "1", "--tpot-percentile", "90",
			"--assume-tpot-ratio", "1.3"}, workedShape), 0},
		{"replicas-to-slo-worked-json", slices.Concat([]string{"replicas-to-slo", "--recording", olsSweep,
			"--model", "model-02", "--tpot-percentile", "90", "--assume-tpot-ratio", "1.3", "-o", "json"}, workedShape), 0},
		{"slo-to-rate-worked", slices.Concat([]string{"slo-to-rate", "--recording", olsSweep,
			"--cycle", "last", "--model", "model-02", "--decode", "1", "--tpot", "4ms"}, workedShape), 0},
		{"slo-to-rate-worked-json", slices.Concat([]string{"slo-to-rate", "--recording", olsSweep,
			"--tpot", "4ms", "-o", "json"}, workedShape), 0},
		{"replicas-to-slo-recorded-shape", []string{"replicas-to-slo", "--recording", olsSweep}, 0},
		{"replicas-to-slo-cycle-100", []string{"replicas-to-slo", "--recording", olsSweep, "--cycle", "100"}, 0},
		{"replicas-to-slo-no-histogram", slices.Concat([]string{"replicas-to-slo", "--recording", olsSweep,
			"--tpot-percentile", "90"}, workedShape), 3},
		{"slo-to-rate-below-floor", slices.Concat([]string{"slo-to-rate", "--recording", olsSweep,
			"--tpot", "2ms"}, workedShape), 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, code := runCmd(t, c.args...)
			if code != c.code {
				t.Errorf("exit %d, want %d; output:\n%s", code, c.code, got)
			}
			path := filepath.Join("testdata", c.name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (go test ./cmd/whatif -run TestGolden -update writes it)", err)
			}
			if w := strings.ReplaceAll(string(want), "\r\n", "\n"); got != w {
				t.Errorf("output differs from %s\ngot:\n%s\nwant:\n%s", path, got, w)
			}
		})
	}
}

// decodeOutput reads a command's JSON output strictly: a field the wire
// types do not name is an error.
func decodeOutput(t *testing.T, s string) wireOutput {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var w wireOutput
	if err := dec.Decode(&w); err != nil {
		t.Fatalf("decode: %v\n%s", err, s)
	}
	return w
}

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if !(math.Abs(got-want) <= tol) {
		t.Errorf("%s = %.6g, want %.6g within %g", what, got, want, tol)
	}
}

// The page's worked example, read from the committed recording through the
// whole command: the replay, the accessors and the library. The figures are
// the page's, at the precision it prints them.
func TestWorkedExampleFromTheRecording(t *testing.T) {
	out, code := runCmd(t, slices.Concat([]string{"replicas-to-slo", "--recording", olsSweep,
		"--tpot-percentile", "90", "--assume-tpot-ratio", "1.3", "-o", "json"}, workedShape)...)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	w := decodeOutput(t, out)
	if len(w.Roles) != 1 || w.Roles[0].Variant != "variant-01" {
		t.Fatalf("roles %+v, want the one decode variant", w.Roles)
	}
	in, r := w.Roles[0].Inputs, w.Roles[0].Results
	near(t, "A", in.Line.A, 0.004438, 0.0000005)
	near(t, "B", in.Line.B, 0.001557, 0.0000005)
	if in.Line.Tier != "ols" || in.CapacityTokens != 131072 || in.BatchCap != 64 || in.Ranks != 1 || in.Replicas != 1 {
		t.Errorf("inputs %+v, want the ols line, C 131072, cap 64 on one rank, one replica", in)
	}
	near(t, "KVreq", in.KVPerRequest, 1500, 0)
	near(t, "knee", r.Knee.Answer.Value, 13.31, 0.005)
	near(t, "batch-cap fill", r.Knee.Answer.Basis.Fill, 0.732, 0.0005)
	near(t, "operating rate", r.OperatingRate.Answer.Value, 11.32, 0.005)
	near(t, "fill at the operating rate", r.OperatingRate.Answer.Basis.Fill, 0.474, 0.0005)
	near(t, "mean TPOT", r.MeanTPOT.Answer.Value, 0.00366, 0.000005)
	near(t, "TPOT floor", r.MeanTPOT.Answer.Basis.Floor, 0.00222, 0.000005)
	near(t, "TPOT p90", r.PercentileTPOT.Answer.Value, 0.00476, 0.000005)
	for name, res := range map[string]*wireResult{"knee": r.Knee, "operating rate": r.OperatingRate,
		"mean TPOT": r.MeanTPOT, "TPOT p90": r.PercentileTPOT} {
		b := res.Answer.Band
		if b == nil || !(b.Lo <= res.Answer.Value && res.Answer.Value <= b.Hi) || !(b.Lo < b.Hi) {
			t.Errorf("%s %g: band %+v, want one around it", name, res.Answer.Value, b)
		}
	}

	out, code = runCmd(t, slices.Concat([]string{"slo-to-rate", "--recording", olsSweep, "--tpot", "4ms", "-o", "json"}, workedShape)...)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	rate := decodeOutput(t, out).Roles[0].Results.Rate.Answer
	near(t, "question 1 rate", rate.Value, 11.32, 0.005)
	near(t, "TPOT alone", rate.Basis.Uncapped, 12.03, 0.005)
	if !slices.Equal(rate.Basis.BoundBy, []string{"operating-point cap"}) {
		t.Errorf("bound by %v, want the operating-point cap", rate.Basis.BoundBy)
	}

	// The page's round trip below the cap: 3 ms inverts to 9.47 r/s.
	out, _ = runCmd(t, slices.Concat([]string{"slo-to-rate", "--recording", olsSweep, "--tpot", "3ms",
		"--headroom", "1", "-o", "json"}, workedShape)...)
	near(t, "3 ms rate", decodeOutput(t, out).Roles[0].Results.Rate.Answer.Value, 9.47, 0.005)
}

// The JSON output's schema is pinned: every key path the wire types can
// write, with its JSON type, is in testdata/schema-v<schemaVersion>.txt. A
// change to the wire types fails here until the schema version is bumped
// and the list rewritten (-update writes the list for the current version).
func TestJSONSchemaIsPinned(t *testing.T) {
	var paths []string
	schemaPaths(reflect.TypeFor[wireOutput](), "", &paths)
	slices.Sort(paths)
	got := strings.Join(paths, "\n") + "\n"
	path := filepath.Join("testdata", "schema-v"+strconv.Itoa(schemaVersion)+".txt")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v: a schema version with no pinned key list", err)
	}
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); got != w {
		t.Errorf("the JSON schema moved without a new schemaVersion (now %d)\ngot:\n%s\nwant:\n%s", schemaVersion, got, w)
	}
	if len(paths) < 40 {
		t.Fatalf("walked only %d key paths: %v", len(paths), paths)
	}
}

// schemaPaths walks a wire type and appends "path type" for every key.
func schemaPaths(t reflect.Type, prefix string, out *[]string) {
	switch t.Kind() {
	case reflect.Pointer:
		schemaPaths(t.Elem(), prefix, out)
	case reflect.Slice, reflect.Array:
		*out = append(*out, prefix+" array")
		schemaPaths(t.Elem(), prefix+"[]", out)
	case reflect.Struct:
		if t == reflect.TypeFor[time.Time]() {
			*out = append(*out, prefix+" string")
			return
		}
		if prefix != "" {
			*out = append(*out, prefix+" object")
		}
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			p := name
			if prefix != "" {
				p = prefix + "." + name
			}
			schemaPaths(f.Type, p, out)
		}
	case reflect.String:
		*out = append(*out, prefix+" string")
	case reflect.Bool:
		*out = append(*out, prefix+" boolean")
	case reflect.Int, reflect.Int64, reflect.Uint64, reflect.Float64:
		*out = append(*out, prefix+" number")
	default:
		*out = append(*out, prefix+" "+t.Kind().String())
	}
}

// Every result on the wire is an answer or a refusal, never both and never
// neither, and an answer's flags and bounds are arrays, never null.
func TestJSONResultsAreOneOrTheOther(t *testing.T) {
	for _, args := range [][]string{
		slices.Concat([]string{"replicas-to-slo", "--recording", olsSweep, "--tpot-percentile", "90", "-o", "json"}, workedShape),
		slices.Concat([]string{"slo-to-rate", "--recording", olsSweep, "--tpot", "2ms", "-o", "json"}, workedShape),
		{"replicas-to-slo", "--recording", olsSweep, "-o", "json"},
	} {
		out, code := runCmd(t, args...)
		w := decodeOutput(t, out)
		if w.SchemaVersion != schemaVersion || w.ExitCode != code {
			t.Errorf("%v: schemaVersion %d exitCode %d, want %d and the process's %d", args, w.SchemaVersion, w.ExitCode, schemaVersion, code)
		}
		if !strings.Contains(out, `"flags": [`) && strings.Contains(out, `"answer"`) {
			t.Errorf("%v: an answer's flags are not an array:\n%s", args, out)
		}
		for _, role := range w.Roles {
			rs := role.Results
			for _, r := range []*wireResult{rs.Knee, rs.OperatingRate, rs.MeanTPOT, rs.PercentileTPOT, rs.Rate} {
				if r != nil && (r.Answer == nil) == (r.Refused == nil) {
					t.Errorf("%v: result %+v is not exactly one of an answer and a refusal", args, r)
				}
			}
		}
	}
}

// Each exit code, from the command line: 0 answered, 3 refused (with the
// output still printed and the reason in it), 2 a usage or input error,
// whether the flags or the library say so, 1 an internal error.
func TestExitCodes(t *testing.T) {
	q2 := func(extra ...string) []string {
		return slices.Concat([]string{"replicas-to-slo", "--recording", olsSweep}, workedShape, extra)
	}
	q1 := func(extra ...string) []string {
		return slices.Concat([]string{"slo-to-rate", "--recording", olsSweep}, workedShape, extra)
	}
	cases := []struct {
		name   string
		args   []string
		code   int
		output string
	}{
		{"answered", q2(), 0, "knee"},
		{"answered, question 1", q1("--tpot", "4ms"), 0, "11.32 r/s"},
		{"help is not an error", []string{"replicas-to-slo", "-h"}, 0, ""},
		{"refused: no TPOT histogram", q2("--tpot-percentile", "90"), 3, "NoTPOTHistogram"},
		{"refused: target below the floor", q1("--tpot", "2ms"), 3, "TargetBelowFloor"},
		{"refused: percentile target with no ratio", q1("--tpot", "4ms@p90"), 3, "NoTPOTHistogram"},
		{"refused: saturated at --rate", q2("--rate", "20"), 3, "Saturated"},
		{"answered: --decode 0 takes the recorded count", q2("--decode", "0"), 0, "knee"},
		{"usage: no command", nil, 2, ""},
		{"usage: unknown command", []string{"capacity"}, 2, ""},
		{"usage: no recording", []string{"replicas-to-slo"}, 2, ""},
		{"usage: unknown flag", q2("--bogus"), 2, ""},
		{"usage: a stray argument", q2("extra"), 2, ""},
		{"usage: a cycle that is no number", q2("--cycle", "first"), 2, ""},
		{"usage: cycle 0", q2("--cycle", "0"), 2, ""},
		{"input: a cycle past the recording", q2("--cycle", "100000"), 2, ""},
		{"input: a model the recording lacks", q2("--model", "nope"), 2, ""},
		{"input: a variant the cycle lacks", q2("--variant", "nope"), 2, ""},
		{"input: no such file", []string{"replicas-to-slo", "--recording", "testdata/none.jsonl"}, 2, ""},
		{"input: not a recording", []string{"replicas-to-slo", "--recording", "answer_test.go"}, 2, ""},
		{"usage: TTFT is not modelled", q1("--tpot", "4ms", "--ttft", "200ms@p95"), 2, ""},
		{"usage: prefill is not modelled", q2("--prefill", "1"), 2, ""},
		{"usage: an output format", q2("-o", "yaml"), 2, ""},
		{"usage: a negative --decode", q2("--decode", "-1"), 2, ""},
		{"usage: a negative --batch-cap", q2("--batch-cap", "-1"), 2, ""},
		{"usage: a zero output length", q2("--assume-output-tokens", "0"), 2, ""},
		{"usage: a --rate of zero", q2("--rate", "0"), 2, ""},
		{"usage: a --rate that is no number", q2("--rate", "NaN"), 2, ""},
		{"usage: an assumed ratio with no percentile", q2("--assume-tpot-ratio", "1.3"), 2, ""},
		{"usage: no --tpot", q1(), 2, ""},
		{"usage: a --tpot that is no duration", q1("--tpot", "4"), 2, ""},
		{"usage: a --tpot percentile without p", q1("--tpot", "4ms@90"), 2, ""},
		{"usage: a --tpot-percentile that is no number", q2("--tpot-percentile", "ninety"), 2, ""},
		// The library's InvalidInput: printed as a refusal, exit 2.
		{"invalid: a percentile outside (0, 100)", q2("--tpot-percentile", "150", "--assume-tpot-ratio", "1.3"), 2, "InvalidInput"},
		{"invalid: an assumed ratio below 1", q2("--tpot-percentile", "90", "--assume-tpot-ratio", "0.5"), 2, "InvalidInput"},
		{"invalid: question 1 headroom above 1", q1("--tpot", "4ms", "--headroom", "1.5"), 2, "InvalidInput"},
		{"invalid: question 2 headroom of 1", q2("--headroom", "1"), 2, "InvalidInput"},
		{"invalid: both --rate and --headroom", q2("--rate", "5", "--headroom", "0.5"), 2, "InvalidInput"},
		{"invalid: a ratio refusal does not hide a bad headroom", q1("--tpot", "4ms@p90", "--headroom", "2"), 2, "InvalidInput"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, code := runCmd(t, c.args...)
			if code != c.code {
				t.Errorf("exit %d, want %d; output:\n%s", code, c.code, out)
			}
			if c.output != "" && !strings.Contains(out, c.output) {
				t.Errorf("output lacks %q:\n%s", c.output, out)
			}
		})
	}
}

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// An output that cannot be written is an internal error, exit 1.
func TestExitCodeInternal(t *testing.T) {
	for _, args := range [][]string{
		slices.Concat([]string{"replicas-to-slo", "--recording", olsSweep}, workedShape),
		slices.Concat([]string{"slo-to-rate", "--recording", olsSweep, "--tpot", "4ms", "-o", "json"}, workedShape),
	} {
		err := run(context.Background(), args, failingWriter{})
		if code := exitCode(err); code != 1 {
			t.Errorf("%v: exit %d (%v), want 1", args, code, err)
		}
	}
}

// The band's seed is the flag's: the same seed gives the same bands, and
// another seed other bands.
func TestSeedSetsTheBands(t *testing.T) {
	band := func(seed string) wireInterval {
		out, _ := runCmd(t, slices.Concat([]string{"replicas-to-slo", "--recording", olsSweep, "-o", "json", "--seed", seed}, workedShape)...)
		return *decodeOutput(t, out).Roles[0].Results.MeanTPOT.Answer.Band
	}
	if a, b := band("1"), band("1"); a != b {
		t.Errorf("seed 1 gave %+v then %+v", a, b)
	}
	if a, b := band("1"), band("2"); a == b {
		t.Errorf("seeds 1 and 2 gave the same band %+v", a)
	}
}

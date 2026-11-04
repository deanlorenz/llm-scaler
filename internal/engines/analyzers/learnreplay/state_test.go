package learnreplay

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// olsSweepRecording is the ols-sweep golden's recording, unzipped.
func olsSweepRecording(t *testing.T) []byte {
	t.Helper()
	gz, err := os.ReadFile(filepath.Join("testdata", "goldens", "ols-sweep", RecordingName))
	if err != nil {
		t.Fatal(err)
	}
	b, err := gunzip(gz)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The what-if planner's replay: stopped at the last cycle of ols-sweep, it
// reads the line the page's worked example is priced on, its tier and
// readings, the capacity, the batch cap and the shape.
func TestStateAt_OLSSweepLastCycle(t *testing.T) {
	st, err := StateAt(context.Background(), olsSweepRecording(t), Options{}, StateQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Namespace != "ns-01" || st.ModelID != "model-02" || st.Cycle != 226 {
		t.Fatalf("stopped at %s/%s cycle %d, want ns-01/model-02 cycle 226", st.Namespace, st.ModelID, st.Cycle)
	}
	if st.Policy == nil || st.FirstAt.IsZero() || !st.At.After(st.FirstAt) {
		t.Errorf("policy %v, first %v, at %v", st.Policy, st.FirstAt, st.At)
	}
	if len(st.Variants) != 2 {
		t.Fatalf("variants %+v, want the decode and the prefill one", st.Variants)
	}
	d, p := st.Variants[0], st.Variants[1]
	if d.Variant != "variant-01" || d.Role != "decode" || p.Role != "prefill" {
		t.Fatalf("variants %s/%s and %s/%s", d.Variant, d.Role, p.Variant, p.Role)
	}
	if math.Abs(d.Line.A-0.004438) > 5e-7 || math.Abs(d.Line.B-0.001557) > 5e-7 || d.Tier != whatif.TierOLS {
		t.Errorf("line %+v tier %q, want the page's ols line A 0.004438 B 0.001557", d.Line, d.Tier)
	}
	if d.PricingKey == "" || len(d.Readings) == 0 || len(d.SteadyReadings) == 0 {
		t.Errorf("key %q, %d readings, %d steady; want the line's window", d.PricingKey, len(d.Readings), len(d.SteadyReadings))
	}
	if d.CapacityTokens != 131072 || d.BatchCap != 64 || d.BatchCapSource != "flags" || d.Ranks != 1 || d.Replicas != 1 {
		t.Errorf("C %g, cap %d (%s), ranks %d, replicas %d; want 131072, 64 from the flags, 1, 1",
			d.CapacityTokens, d.BatchCap, d.BatchCapSource, d.Ranks, d.Replicas)
	}
	if d.InputTokens < 900 || d.OutputTokens < 3900 || d.PrefixHitRate != 0 {
		t.Errorf("shape IL %g OL %g hit %g, want the last cycle's ~1000/~4000 with no hits", d.InputTokens, d.OutputTokens, d.PrefixHitRate)
	}
	if !p.Line.IsZero() || p.Tier != "" {
		t.Errorf("the prefill variant priced on line %+v tier %q", p.Line, p.Tier)
	}
	if len(st.TPOTWindows) != 0 {
		t.Errorf("%d TPOT windows from a recording made before the recorder carried them", len(st.TPOTWindows))
	}
}

// A cycle number stops the replay there: cycle 1 has no line yet, and the
// state of cycle 100 is not the last one's.
func TestStateAt_StopsAtTheCycle(t *testing.T) {
	rec := olsSweepRecording(t)
	first, err := StateAt(context.Background(), rec, Options{}, StateQuery{Namespace: "ns-01", ModelID: "model-02", Cycle: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Cycle != 1 || !first.Variants[0].Line.IsZero() {
		t.Errorf("cycle %d line %+v, want cycle 1 with no line", first.Cycle, first.Variants[0].Line)
	}
	mid, err := StateAt(context.Background(), rec, Options{}, StateQuery{ModelID: "model-02", Cycle: 100})
	if err != nil {
		t.Fatal(err)
	}
	last, err := StateAt(context.Background(), rec, Options{}, StateQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if mid.Cycle != 100 || mid.Variants[0].Line.IsZero() || !mid.At.After(first.At) || !mid.At.Before(last.At) {
		t.Fatalf("cycle %d at %v, line %+v", mid.Cycle, mid.At, mid.Variants[0].Line)
	}
	if mid.Variants[0].Line == last.Variants[0].Line {
		t.Errorf("cycle 100 reads the last cycle's line %+v", mid.Variants[0].Line)
	}
}

func TestStateAt_Refuses(t *testing.T) {
	rec := olsSweepRecording(t)
	for name, c := range map[string]struct {
		q    StateQuery
		want string
	}{
		"a cycle past the last":   {StateQuery{Cycle: 227}, "holds 226"},
		"a model it lacks":        {StateQuery{ModelID: "model-99"}, "no cycle of model"},
		"a namespace it lacks":    {StateQuery{Namespace: "ns-99"}, "no saturation cycle"},
		"a negative cycle number": {StateQuery{Cycle: -1}, "want 1 or more"},
	} {
		_, err := StateAt(context.Background(), rec, Options{}, c.q)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error saying %q", name, err, c.want)
		}
	}
}

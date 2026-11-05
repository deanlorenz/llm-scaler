package learnreplay

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
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
	if d.PricingKey == "" || len(d.Readings.All) == 0 || len(d.Readings.Steady) == 0 {
		t.Errorf("key %q, %d readings, %d steady; want the line's window", d.PricingKey, len(d.Readings.All), len(d.Readings.Steady))
	}
	// The tracker's choice of fit, made again on the key's windows, gives the
	// analyzer's line back to the last bits, from the steady readings.
	if !d.Refitted || len(d.LineReadings) != len(d.Readings.Steady) || d.Baseline != d.Line.B {
		t.Errorf("refitted %v from %d readings (%d steady), baseline %g; want the ols line from the steady ones",
			d.Refitted, len(d.LineReadings), len(d.Readings.Steady), d.Baseline)
	}
	if d.CapacityTokens != 131072 || d.BatchCap != 64 || d.BatchCapSource != "flags" || d.Ranks != 1 || d.Replicas != 1 {
		t.Errorf("C %g, cap %d (%s), ranks %d, replicas %d; want 131072, 64 from the flags, 1, 1",
			d.CapacityTokens, d.BatchCap, d.BatchCapSource, d.Ranks, d.Replicas)
	}
	// The recording ends idle (cycles 224-226 serve nothing): the shape is the
	// last 20 cycles that had traffic, weighted by request rate.
	sh := d.Shape
	if sh.ToCycle != 223 || sh.FromCycle != 204 || sh.Cycles != 20 || sh.PrefixHitRate != 0 ||
		!(sh.InputTokens > 900 && sh.InputTokens < 6100) || !(sh.OutputTokens > 400 && sh.OutputTokens < 4100) {
		t.Errorf("shape %+v, want cycles 204-223 of traffic, lengths inside what they served", sh)
	}
	t.Logf("measured shape %+v", sh)
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
		var q *QueryError
		if !errors.As(err, &q) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want a QueryError saying %q", name, err, c.want)
		}
	}
}

// A recording that does not replay is not the caller's question: StateAt's
// error for it is no QueryError, and a cancelled replay is one too.
func TestStateAt_ReplayFailureIsNoQueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := StateAt(ctx, olsSweepRecording(t), Options{}, StateQuery{})
	var q *QueryError
	if err == nil || errors.As(err, &q) {
		t.Errorf("cancelled: %v, want an error that is no QueryError", err)
	}
}

// A line the band does not give back, and the above-band fallback does, is
// an above-band line, read on the above-band readings: its observed fills
// are those, at or above itl.DefaultKSat.
func TestRefitFindsAnAboveBandLine(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	above := make([]itl.Observation, 0, 12)
	for i := range 12 {
		k := 0.86 + 0.01*float64(i%8)
		above = append(above, itl.Observation{K: k, ITLSec: 0.01 + 0.05*k + 0.0005*float64(i%3), Timestamp: t0.Add(time.Duration(i) * 15 * time.Second)})
	}
	line := itl.RefitAboveBand(above, nil, 0)
	if line.Tier == "" {
		t.Fatal("the fallback fits nothing; the case tests nothing")
	}
	vs := VariantState{Tier: whatif.TierPinnedB, Line: line.Model, Readings: itl.KeyReadings{Above: above}}
	vs.refit()
	if !vs.Refitted || vs.Tier != whatif.TierAboveBand || len(vs.LineReadings) != len(above) {
		t.Fatalf("%+v, want the above-band tier on the %d above-band readings", vs, len(above))
	}
	for _, o := range vs.LineReadings {
		if o.K < itl.DefaultKSat {
			t.Errorf("a reading at %g, under the fallback's floor", o.K)
		}
	}
	// A line neither fit gives back is left as it was, unbanded.
	other := VariantState{Tier: whatif.TierPinnedB, Line: itl.Model{A: 1, B: 0.002}, Readings: itl.KeyReadings{Above: above}}
	other.refit()
	if other.Refitted || other.Tier != whatif.TierPinnedB || other.LineReadings != nil {
		t.Errorf("%+v, want it as it was, with no readings", other)
	}
}

// A pinned line the tracker priced one-sided at the sequence cap (issue
// #467) is given back by the refit through the default B with the
// analyzer's one-sided admission, which the state carries for the bands
// (OneSided), not by the pinned fit at the line's own B. Without the
// admission (the switch off) it is not refitted.
func TestRefitFindsAOneSidedLine(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	all := make([]itl.Observation, 0, 36)
	for c := range 12 {
		for _, k := range []float64{0.96, 0.97, 0.98} {
			all = append(all, itl.Observation{K: k, ITLSec: 0.020 + 0.01*k, Timestamp: t0.Add(time.Duration(c) * 15 * time.Second)})
		}
	}
	adm := itl.Admission{OneSidedAboveKSat: true, OneSidedAt: 0.68, BPrior: itl.DefaultBPrior}
	line := itl.Refit(all, nil, itl.DefaultBaselineSec, 0, adm)
	if !line.PinnedAtBPrior {
		t.Fatal("the one-sided refit priced nothing one-sided; the case tests nothing")
	}
	vs := VariantState{Tier: whatif.TierPinnedB, Line: line.Model, Readings: itl.KeyReadings{All: all}, oneSided: adm}
	vs.refit()
	if !vs.Refitted || vs.Baseline != itl.DefaultBaselineSec || vs.OneSided != adm || len(vs.LineReadings) != len(all) {
		t.Fatalf("%+v, want it refitted one-sided through the default B on all %d readings", vs, len(all))
	}
	off := VariantState{Tier: whatif.TierPinnedB, Line: line.Model, Readings: itl.KeyReadings{All: all}}
	off.refit()
	if off.Refitted || off.OneSided.OneSidedAboveKSat {
		t.Errorf("negative control: without the admission %+v was refitted", off)
	}
	// A line through the default B the rule did not raise this cycle still
	// carries the admission: a resample may raise it, as the controller's
	// next cycle would.
	inBand := make([]itl.Observation, 0, 12)
	for c := range 12 {
		k := 0.30 + 0.01*float64(c%3)
		inBand = append(inBand, itl.Observation{K: k, ITLSec: 0.006 + 0.05*k, Timestamp: t0.Add(time.Duration(c) * 15 * time.Second)})
	}
	plain := itl.Refit(inBand, nil, itl.DefaultBaselineSec, 0, adm)
	if plain.PinnedAtBPrior || plain.Tier != itl.TierPinnedB {
		t.Fatalf("premise: %+v, want a pinned fit the rule did not raise", plain)
	}
	un := VariantState{Tier: whatif.TierPinnedB, Line: plain.Model, Readings: itl.KeyReadings{All: inBand}, oneSided: adm}
	un.refit()
	if !un.Refitted || un.OneSided != adm {
		t.Errorf("%+v, want it refitted carrying the one-sided admission", un)
	}
	// A learned B is not priced one-sided, and its line carries none.
	learned := itl.Refit(inBand, nil, 0.010, 0, itl.Admission{})
	lb := VariantState{Tier: whatif.TierPinnedB, Line: learned.Model, Readings: itl.KeyReadings{All: inBand}, oneSided: adm}
	lb.refit()
	if !lb.Refitted || lb.OneSided.OneSidedAboveKSat {
		t.Errorf("a learned B: %+v, want it refitted with no one-sided admission", lb)
	}
}

// The measured shape weights each cycle with traffic by its request rate,
// and every cycle alike when none read a rate.
func TestShapeOfWeightsByRequestRate(t *testing.T) {
	samples := []shapeSample{
		{cycle: 7, il: 1000, ol: 100, hit: 0.2, req: 1},
		{cycle: 9, il: 2000, ol: 500, hit: 0.6, req: 3},
	}
	got := shapeOf(samples)
	if got.InputTokens != 1750 || got.OutputTokens != 400 || math.Abs(got.PrefixHitRate-0.5) > 1e-12 ||
		got.FromCycle != 7 || got.ToCycle != 9 || got.Cycles != 2 {
		t.Errorf("%+v, want IL 1750, OL 400, hit 0.5 over cycles 7-9", got)
	}
	samples[0].req, samples[1].req = 0, 0
	if got := shapeOf(samples); got.InputTokens != 1500 || got.OutputTokens != 300 {
		t.Errorf("no rate read: %+v, want the plain mean IL 1500, OL 300", got)
	}
	if got := shapeOf(nil); got != (MeasuredShape{}) {
		t.Errorf("no traffic: %+v, want zero", got)
	}
}

// The last cycle is a cycle number like any other: asking for it by number
// gives the same state, and the readings' cycles fall inside the replay.
func TestStateAt_LastIsTheLastCycle(t *testing.T) {
	rec := olsSweepRecording(t)
	last, err := StateAt(context.Background(), rec, Options{}, StateQuery{})
	if err != nil {
		t.Fatal(err)
	}
	byNumber, err := StateAt(context.Background(), rec, Options{}, StateQuery{Namespace: "ns-01", ModelID: "model-02", Cycle: 226})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(last, byNumber) {
		t.Errorf("the last cycle and cycle 226 differ:\n%+v\n%+v", last, byNumber)
	}
	d := last.Variants[0]
	if last.Cycles != 226 || d.ReadingsFrom < 1 || d.ReadingsTo > 226 || d.ReadingsFrom > d.ReadingsTo {
		t.Errorf("%d cycles, readings over cycles %d-%d", last.Cycles, d.ReadingsFrom, d.ReadingsTo)
	}
	t.Logf("line readings %d over cycles %d-%d", len(d.LineReadings), d.ReadingsFrom, d.ReadingsTo)
}

package whatif

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
)

// The vectors in testdata were captured from the slo and saturation analyzers
// on main, before this package's functions were moved out of them (the commit
// that adds testdata/*.jsonl.gz holds the capture). Each holds one call's
// inputs and outputs, floats as the hex of their IEEE-754 bits. Replaying the
// inputs here must give back every output bit for bit: the move changed no
// number the controller acts on.

// vector is one captured call: float inputs F, integer inputs I, and float
// (OF), bool (OB) and string (OS) outputs.
type vector struct {
	Fn string   `json:"fn"`
	F  []string `json:"f,omitempty"`
	I  []int64  `json:"i,omitempty"`
	OF []string `json:"of,omitempty"`
	OB []bool   `json:"ob,omitempty"`
	OS []string `json:"os,omitempty"`
}

// wantVectors is how many vectors of each function the capture wrote. A
// replay over fewer -- a truncated or empty file -- is not evidence.
var wantVectors = map[string]int{
	"PriceTPOT":        1632,
	"ReplicaTokenRate": 18816,
	"ReachableFill":    7056,
	"RealisedFill":     11520,
	"PricingK":         379,
	"CapacityTokens":   150,
	"DeriveMuAtCap":    22680,
}

func TestMovedPricingIsBitIdentical(t *testing.T) {
	got := map[string]int{}
	bad := 0
	for _, file := range []string{"testdata/slo-pricing.jsonl.gz", "testdata/saturation-mu.jsonl.gz"} {
		vs, err := readVectors(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, v := range vs {
			replayed, err := replay(v)
			if err != nil {
				t.Fatalf("%s vector %d: %v", file, i+1, err)
			}
			got[v.Fn]++
			if !reflect.DeepEqual(replayed, v) {
				bad++
				if bad <= 10 {
					t.Errorf("%s vector %d differs:\n captured %+v\n replayed %+v", file, i+1, v, replayed)
				}
			}
		}
	}
	if !reflect.DeepEqual(got, wantVectors) {
		t.Fatalf("replayed %v vectors, the capture wrote %v", got, wantVectors)
	}
	total := 0
	for _, n := range got {
		total += n
	}
	if bad > 0 {
		t.Fatalf("%d of %d vectors differ from main", bad, total)
	}
	t.Logf("%d vectors bit-identical to main: %v", total, got)
}

// replay calls the function v names on v's inputs and returns v with the
// outputs replaced by what the call gave.
func replay(v vector) (vector, error) {
	f := make([]float64, len(v.F))
	for i, h := range v.F {
		b, err := strconv.ParseUint(h, 16, 64)
		if err != nil {
			return v, err
		}
		f[i] = math.Float64frombits(b)
	}
	need := func(nf, ni int) error {
		if len(f) != nf || len(v.I) != ni {
			return fmt.Errorf("%s: %d floats and %d ints, want %d and %d", v.Fn, len(f), len(v.I), nf, ni)
		}
		return nil
	}
	params := func(has, override int64) *capacity.EngineParams {
		if has == 0 {
			return nil
		}
		return &capacity.EngineParams{TotalKvTokensOverride: override}
	}
	out := vector{Fn: v.Fn, F: v.F, I: v.I}
	switch v.Fn {
	case "PriceTPOT":
		if err := need(5, 0); err != nil {
			return v, err
		}
		p := PriceTPOT(FillBand{Lo: f[0], Hi: f[1]}, itl.Model{A: f[2], B: f[3]}, f[4])
		out.OF, out.OB = fbits(p.Fill, p.Target), []bool{p.Feasible, p.OK}
	case "ReplicaTokenRate":
		if err := need(5, 1); err != nil {
			return v, err
		}
		out.OF = fbits(ReplicaTokenRate(itl.Model{A: f[0], B: f[1]}, f[2], f[3], f[4], v.I[0]))
	case "ReachableFill":
		if err := need(5, 1); err != nil {
			return v, err
		}
		out.OF = fbits(ReachableFill(FillBand{Lo: f[0], Hi: f[1]}, f[2], f[3], f[4], v.I[0]))
	case "RealisedFill":
		if err := need(8, 1); err != nil {
			return v, err
		}
		out.OF = fbits(RealisedFill(FillBand{Lo: f[0], Hi: f[1]}, itl.Model{A: f[2], B: f[3]},
			f[4], f[5], f[6], f[7], v.I[0]))
	case "PricingK":
		if err := need(4, 1); err != nil {
			return v, err
		}
		out.OF = fbits(PricingK(f[0], f[1], FillBand{Lo: f[2], Hi: f[3]}))
	case "CapacityTokens":
		if err := need(0, 4); err != nil {
			return v, err
		}
		out.OF = fbits(CapacityTokens(params(v.I[0], v.I[1]), int(v.I[2]), v.I[3]))
	case "DeriveMuAtCap":
		if err := need(5, 5); err != nil {
			return v, err
		}
		d := DeriveMuAtCap(itl.Model{A: f[0], B: f[1]}, params(v.I[0], v.I[1]), v.I[2], int(v.I[3]), v.I[4],
			shape.Shape{KVreq: f[2]}, f[3], f[4])
		out.OF, out.OB, out.OS = fbits(d.Rate, d.Seqs, d.TokenSec), []bool{d.OK, d.CapAtMeasured}, []string{d.Why}
	default:
		return v, fmt.Errorf("unknown function %q", v.Fn)
	}
	return out, nil
}

func fbits(xs ...float64) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = strconv.FormatUint(math.Float64bits(x), 16)
	}
	return out
}

func readVectors(path string) ([]vector, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var vs []vector
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		var v vector
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, len(vs)+1, err)
		}
		vs = append(vs, v)
	}
	return vs, sc.Err()
}

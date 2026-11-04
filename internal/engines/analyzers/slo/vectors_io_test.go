package slo

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
)

// vector is one call of a moved function: its float inputs (F) and integer
// inputs (I), and its float (OF), bool (OB) and string (OS) outputs. Floats are
// stored as the hex of their IEEE-754 bits, so NaN, -0 and the last ulp survive
// the round trip and the comparison is bit for bit.
type vector struct {
	Fn string   `json:"fn"`
	F  []string `json:"f,omitempty"`
	I  []int64  `json:"i,omitempty"`
	OF []string `json:"of,omitempty"`
	OB []bool   `json:"ob,omitempty"`
	OS []string `json:"os,omitempty"`
}

func fbits(xs ...float64) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = strconv.FormatUint(math.Float64bits(x), 16)
	}
	return out
}

func checkVectors(t *testing.T, path string, got []vector) {
	t.Helper()
	if os.Getenv("WHATIF_WRITE_VECTORS") == "1" {
		if err := writeVectors(path, got); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d vectors to %s", len(got), path)
		return
	}
	want, err := readVectors(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 || len(want) != len(got) {
		t.Fatalf("%s holds %d vectors, the grid makes %d", path, len(want), len(got))
	}
	bad := 0
	for i := range want {
		if !reflect.DeepEqual(want[i], got[i]) {
			bad++
			if bad <= 10 {
				t.Errorf("vector %d differs:\n want %+v\n got  %+v", i, want[i], got[i])
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d vectors differ", bad, len(want))
	}
	t.Logf("%d vectors bit-identical", len(want))
}

func writeVectors(path string, vs []vector) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}
	// No timestamp or name in the header, so a rewrite of equal vectors is a
	// byte-identical file.
	enc := json.NewEncoder(zw)
	for _, v := range vs {
		if err := enc.Encode(v); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
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

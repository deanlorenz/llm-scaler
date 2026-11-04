package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/learnreplay"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learnrec"
)

func runGenerate(ctx context.Context, args []string, out, _ io.Writer) error {
	fs := newFlags("generate", out)
	in := fs.String("in", "", "the capture (whatif capture -o), gzipped or not (required)")
	dst := fs.String("o", "", "where to write the recording; a name ending in .gz is gzipped (required)")
	if ok, err := parse(fs, args); !ok {
		return err
	}
	if *in == "" || *dst == "" {
		return usagef("generate needs -in and -o")
	}
	capture, err := readMaybeGzipped(*in)
	if err != nil {
		return usageError{err}
	}
	rep, err := learnreplay.Replay(ctx, capture, learnreplay.Options{Mode: learnreplay.Generate})
	var bad *learnreplay.CaptureError
	switch {
	case errors.As(err, &bad):
		return usageError{err}
	case err != nil:
		return fmt.Errorf("generate: %w", err)
	}
	recording := rep.Recording
	start, err := startOf(recording)
	if err != nil {
		return err
	}
	if len(*dst) > 3 && (*dst)[len(*dst)-3:] == ".gz" {
		if recording, err = learnreplay.Gzip(recording); err != nil {
			return err
		}
	}
	if err := os.WriteFile(*dst, recording, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(out, "generated %d steps into %s: %d bytes, analyzes %v\n", rep.Applied, *dst, len(recording), rep.Analyzes)
	for _, c := range start.Caveats {
		fmt.Fprintf(out, "  caveat: %s\n", c)
	}
	return nil
}

// startOf is a recording's start line.
func startOf(recording []byte) (learnrec.Start, error) {
	var start learnrec.Start
	read, err := learnrec.Read(recording)
	if err != nil {
		return start, err
	}
	if len(read.Events) == 0 || read.Events[0].Kind != learnrec.KindStart {
		return start, errors.New("generate: the generated recording has no start line")
	}
	return start, read.Events[0].DecodeData(&start)
}

// readMaybeGzipped reads a file, gunzipping it when it is gzipped.
func readMaybeGzipped(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b, nil
	}
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

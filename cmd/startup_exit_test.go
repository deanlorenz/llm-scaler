package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
)

// A SIGTERM during a startup step cancels the step. That must exit 0 with an
// info line, not report the step as failed and exit 1.
func TestStartupExitCodeOnShutdown(t *testing.T) {
	var lines []string
	log := funcr.New(func(prefix, args string) { lines = append(lines, args) }, funcr.Options{})
	stepErr := errors.New("context canceled")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := startupExitCode(ctx, log, stepErr, "CRITICAL: Failed to connect to Prometheus"); code != 0 {
		t.Errorf("exit code after shutdown = %d, want 0", code)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "Shutdown requested during startup") {
		t.Errorf("log after shutdown = %q, want one shutdown info line", lines)
	}

	lines = nil
	if code := startupExitCode(context.Background(), log, errors.New("connection refused"), "CRITICAL: Failed to connect to Prometheus"); code != 1 {
		t.Errorf("exit code on a real failure = %d, want 1", code)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "CRITICAL: Failed to connect to Prometheus") {
		t.Errorf("log on a real failure = %q, want the step's error", lines)
	}
}

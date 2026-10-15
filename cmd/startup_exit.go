package main

import (
	"context"

	"github.com/go-logr/logr"
)

// startupExitCode reports a failed startup step and returns the code the
// process should exit with.
//
// Startup steps run under the signal context, so a SIGTERM during one cancels
// it and the step returns an error. That is a requested shutdown, not a fault:
// it is logged as an info line and exits 0. Logged as the step's own failure
// ("CRITICAL: Failed to connect to Prometheus") it reads as an outage, and the
// exit code 1 counts against the pod.
func startupExitCode(ctx context.Context, log logr.Logger, err error, msg string) int {
	if ctx.Err() != nil {
		log.Info("Shutdown requested during startup; exiting", "step", msg, "error", err.Error())
		return 0
	}
	log.Error(err, msg)
	return 1
}

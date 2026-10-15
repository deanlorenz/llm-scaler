package main

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
)

// startupExitCode reports a failed startup step and returns the code the
// process should exit with.
//
// Startup steps run under the signal context, so a SIGTERM during one cancels
// it and the step returns the context's error. That is a requested shutdown,
// not a fault: it is logged as an info line and exits 0. Logged as the step's
// own failure ("CRITICAL: Failed to connect to Prometheus") it reads as an
// outage, and the exit code 1 counts against the pod.
//
// Both must hold: the context is done AND the step's error is that
// cancellation. A step that failed for its own reason -- a Forbidden, a
// refused connection -- just as a SIGTERM arrived is still a failure, and is
// reported as one.
func startupExitCode(ctx context.Context, log logr.Logger, err error, msg string) int {
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		log.Info("Shutdown requested during startup; exiting", "step", msg, "error", err.Error())
		return 0
	}
	log.Error(err, msg)
	return 1
}

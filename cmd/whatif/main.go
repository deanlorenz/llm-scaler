// Command whatif is the what-if capacity planner's command line
// (docs/proposals/slo-capacity-planner.md, "CLI reference"). Today it
// captures a past window from Prometheus and turns a capture into a
// recording; the answer commands come with the planner (D3).
//
//	whatif capture  --prometheus URL --namespace NS (--kube-state-metrics | --role-of ROLE=SELECTOR ...)
//	                --from T0 --to T1 [--step 15s] [--policy FILE] -o run.capture.jsonl.gz
//	whatif generate -in run.capture.jsonl.gz -o run.jsonl
//
// capture writes the window's cycle inputs (internal/whatifsource);
// generate plays them through the analyzers in the engine's order
// (learnreplay, Generate mode) and writes the recording, which
// learnergolden export pseudonymises and learnergolden add accepts at the
// commit that generated it.
//
// Exit codes: 0 done; 2 a usage or input error (a bad flag, an unreadable
// or malformed capture, overlapping --role-of selectors, a window too large
// without --force); 1 anything else (a failed Prometheus query, a Generate
// that fails inside the analyzers).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "whatif:", err)
		os.Exit(exitCode(err))
	}
}

// usageError is a usage or input error: exit 2.
type usageError struct{ error }

func (e usageError) Unwrap() error { return e.error }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

// exitCode is the exit code for err: 2 for a usage or input error, 1 for
// anything else.
func exitCode(err error) int {
	var u usageError
	if errors.As(err, &u) {
		return 2
	}
	return 1
}

const usage = "usage: whatif capture|generate [flags]; -h after a command lists its flags"

// command is one subcommand: it parses its own flags from args and writes
// what it reports to out, and its warnings to warn (stderr).
type command func(ctx context.Context, args []string, out, warn io.Writer) error

// commands is every subcommand, by name. The planner's answer commands
// (slo-to-rate, replicas-to-slo) join them in D3.
var commands = map[string]command{
	"capture":  runCapture,
	"generate": runGenerate,
}

// run runs one command. Asking for help is not an error.
func run(ctx context.Context, args []string, out, warn io.Writer) error {
	if len(args) == 0 {
		return usageError{errors.New(usage)}
	}
	cmd, ok := commands[args[0]]
	if !ok {
		return usagef("unknown command %q; %s", args[0], usage)
	}
	return cmd(ctx, args[1:], out, warn)
}

// newFlags is a flag set for one command that reports its errors as usage
// errors and writes its help to out.
func newFlags(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("whatif "+name, flag.ContinueOnError)
	fs.SetOutput(out)
	return fs
}

// parse parses args; ok is false when the caller should stop: help was
// asked for (err nil) or the flags were wrong (a usage error).
func parse(fs *flag.FlagSet, args []string) (bool, error) {
	err := fs.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return false, nil
	case err != nil:
		return false, usageError{err}
	case fs.NArg() > 0:
		return false, usagef("unexpected argument %q", fs.Arg(0))
	}
	return true, nil
}

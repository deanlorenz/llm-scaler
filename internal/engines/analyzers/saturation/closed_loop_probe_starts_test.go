package saturation

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/internal/learnertest"
)

// The closed-loop probe at the measured range of a replica's start
// (analyzer-learner.md, PR 5 design: "the closed-loop probe runs C for true
// starts of 15 s and 463 s"), as liveness property cases: the load step, the
// controller restart before a step, the restored snapshot through it, and
// the two slow-pod cases, each at 15 s (a snapshot restore), 90 s (the
// simulator's default) and 463 s (a cold JIT cache). Every row reports the
// true start as its measured one, so the start learner prices the truth.
//
// Each must settle within tolerance of need by its horizon, and sit below
// need no longer than underBound: the time the default case allows, with its
// start replaced by the case's. A case that fails is a ledger row
// (testdata/properties/known-failures.yaml), not a wider bound.
//
// The probe draws nothing from its seed, so each case runs on one.

// probeStarts is the measured range of a replica's start (docs/reference/
// start-time.md): 15 s a snapshot restore, 90 s the simulator's default,
// 463 s a cold JIT cache.
var probeStarts = []time.Duration{15 * time.Second, probeStart, 463 * time.Second}

// underBound is how long a fleet whose replicas take start to become Ready
// may sit below need. learnertest.UnderFor is the default start, the joined
// replica's settle, and a cycle or two of ordering either side, so only its
// start moves with the case's: 3m45s at 15 s, 5 min at 90 s, 11m13s at 463 s.
func underBound(start time.Duration) time.Duration {
	return learnertest.UnderFor - probeStart + start
}

// verdictWithin is verdict with the mid-run bound at under in place of the
// default start's.
func (r probeResult) verdictWithin(under time.Duration) string {
	end := r.fleet
	end.UnderFor = 0
	if v := end.Verdict(); v != "" {
		return v
	}
	if r.underFor > under {
		return fmt.Sprintf("under-ordered mid-run: below need for %s, over the %s a start and a settle take", r.underFor, under)
	}
	return ""
}

// traceSummary is a fleet trace as runs: "2 from 0s, 3 from 2m0s, ...".
func traceSummary(trace []int) string {
	var out []byte
	for i, n := range trace {
		if i > 0 && trace[i-1] == n {
			continue
		}
		if len(out) > 0 {
			out = append(out, ", "...)
		}
		out = fmt.Appendf(out, "%d from %s", n, time.Duration(i)*propCycle)
	}
	return string(out)
}

// startProbe is one probe scenario run at each of probeStarts.
type startProbe struct {
	name, learner string
	pc            probeCase
	// sameAsUnrestarted holds the run's time under need to the same case's
	// with no restart (the restored snapshot).
	sameAsUnrestarted bool
}

func startProbes() []startProbe {
	return []startProbe{
		{name: "load-step", learner: "floor", pc: probeCase{
			name: "step", pods: []float64{1, 1}, addedMu: 1, meanMu: 1,
			lambda: stepTo(1.2, 6, 2*time.Minute), horizon: 40 * time.Minute,
		}},
		{name: "restart-before-step", learner: "floor", pc: restartStepCase(15*time.Minute, false, 0)},
		{name: "restored-snapshot", learner: "floor", pc: restartStepCase(15*time.Minute, true, 0), sameAsUnrestarted: true},
		{name: "slow-pod-030", learner: "mu", pc: slowReplicaCase(0.3)},
		{name: "slow-pod-055", learner: "mu", pc: slowReplicaCase(0.55)},
	}
}

// startProbeCases is each startProbe at each start, as a property case.
func startProbeCases() []propCase {
	probes := startProbes()
	out := make([]propCase, 0, len(probes)*len(probeStarts))
	for _, sp := range probes {
		for _, s := range probeStarts {
			out = append(out, propCase{class: classLiveness, learner: sp.learner,
				name:   fmt.Sprintf("closed-loop-%s-start-%ds", sp.name, int(s.Seconds())),
				family: familyDirect, tuning: tunedDefault, seeds: 1, check: checkStartProbe(sp, s)})
		}
	}
	return out
}

// checkStartProbe runs sp with its replicas starting in start.
func checkStartProbe(sp startProbe, start time.Duration) func(uint64) error {
	return func(uint64) error {
		pc := sp.pc
		pc.start = start
		r, err := tryProbe(pc)
		if err != nil {
			return err
		}
		summary := fmt.Sprintf("start %s: need %d, final %d, peak %d, under need for %s (bound %s); fleet %s",
			start, r.need, r.final, r.peak, r.underFor, underBound(start), traceSummary(r.trace))
		if v := r.verdictWithin(underBound(start)); v != "" {
			return violation(kindStale, "%s; %s", v, summary)
		}
		if sp.sameAsUnrestarted {
			control := pc
			control.restartAt, control.snapshot = 0, false
			c, err := tryProbe(control)
			if err != nil {
				return err
			}
			if c.underFor != r.underFor {
				return violation(kindMoved, "the restored snapshot under need for %s, %s without the restart; %s", r.underFor, c.underFor, summary)
			}
		}
		return nil
	}
}

// tryProbe is runProbe outside a test: a require failure inside the probe
// (the rig's clock and the fleet's disagree, an analyze error) comes back as
// an error.
func tryProbe(pc probeCase) (r probeResult, err error) {
	ft := &probeT{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r = runProbe(ft, pc)
	}()
	<-done
	if ft.failed {
		return r, fmt.Errorf("probe %s: %s", pc.name, ft.msg)
	}
	return r, nil
}

// probeT is the testing.TB runProbe needs, recording a failure instead of
// failing a test: FailNow ends the probe's goroutine.
type probeT struct {
	testing.TB
	failed bool
	msg    string
}

func (p *probeT) Helper() {}

func (p *probeT) Errorf(format string, args ...any) {
	p.failed, p.msg = true, fmt.Sprintf(format, args...)
}

func (p *probeT) FailNow() {
	p.failed = true
	runtime.Goexit()
}

package saturation

import (
	"fmt"
	"math"
	"runtime"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/internal/learnertest"
)

// The closed-loop probe at the measured range of a replica's start
// (analyzer-learner.md, PR 5 design: "the closed-loop probe runs C for true
// starts of 15 s and 463 s"), as liveness property cases: the load step, a
// decode step with no restart, the controller restart before that step, the
// restored snapshot through it, and the two slow-pod cases, each at 15 s (a
// snapshot restore), 90 s (the simulator's default) and 463 s (a cold JIT
// cache). Every row reports the true start as its measured one.
//
// Each case must, in this order:
//
//   - publish the true start: the start learner's figure
//     (AnalyzerResult.RoleStartSeconds) within a second of S on the last
//     cycle, else kindBiased. This is what makes the case about its start:
//     without it a run that ignored the start could fail the same way;
//   - never order more than peakCeiling, and end within tolerance of need by
//     its horizon, or by backlogClears when that is later, else
//     kindOverPriced;
//   - end no more than a replica under need, and sit below need no longer
//     than underBound, else kindStale;
//   - for the restored snapshot, sit below need exactly as long as the same
//     step with no restart, else kindMoved.
//
// A case that fails is a ledger row (testdata/properties/known-failures.yaml)
// with the kind it fails with, not a wider bound. The probe draws nothing from
// its seed, so each case runs on one.

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

// peakCeilingFactor bounds the fleet at its largest: half again over need,
// plus a replica of rounding. A step may order over need while it drains what
// queued during the start, but the fleet the 15 s cases reach (at most 22
// for a need of 17, 1.29x) shows a start-aware order needs no more. It is a
// declared ceiling, not derived from the floor's arithmetic.
const peakCeilingFactor = 1.5

// peakCeiling is the most replicas a case may order for need.
func peakCeiling(need int) float64 { return peakCeilingFactor*float64(need) + 1 }

// overBound is learnertest.Result.Verdict's: the most a fleet may end at.
func overBound(need int) float64 { return float64(need)*(1+learnertest.OverTolerance) + 1 }

// backlogClears is the soonest a fleet hit by a load step at step, whose
// replicas take start to become Ready, can be back inside overBound: the
// allowance the verdict makes for the backlog a slow start leaves (#520).
//
// Until the first order lands, start plus the cycle that orders it, the
// fleet's first replicas take the new rate lambda1 over their capacity C (the
// sum of their mu), and each queues lambda1/C - 1 of its mu per second. A
// queued request stays on its replica, and the queue-aware router sends that
// replica nothing while its queue is the longest, so it drains at its mu, in
// as long again times lambda1/C - 1. The fleet may then come down once the
// scale-down stabilization has passed:
//
//	backlogClears = step + (start + cycle) * max(lambda1/C, 1) + stabilization
//
// For the decode step (4 replicas of mu 1, 2.5 -> 12 req/s at 15m15s) at a
// 463 s start: 915 + 478 x 3 + 300 s = 44m9s, past the 40 min horizon; at
// 90 s, 25m30s. For the load step (2 of mu 1, 1.2 -> 6 at 2 min) at 463 s:
// 30m54s. The analyzer is not charged: it is the time a fleet ordering need
// at the step's own cycle would take.
func backlogClears(pc probeCase, step, start time.Duration) time.Duration {
	capacity := 0.0
	for _, mu := range pc.pods {
		capacity += mu
	}
	stabilization := learnertest.DefaultStabilization
	if pc.stabilization > 0 {
		stabilization = pc.stabilization
	}
	ratio := 1.0
	if capacity > 0 {
		ratio = max(pc.lambda(pc.horizon)/capacity, 1)
	}
	return step + time.Duration(ratio*float64(start+propCycle)) + stabilization
}

// verdictHorizon is the case's horizon, or backlogClears rounded up to a
// cycle when that is later.
func verdictHorizon(pc probeCase, step, start time.Duration) time.Duration {
	clears := backlogClears(pc, step, start)
	if rest := clears % propCycle; rest > 0 {
		clears += propCycle - rest
	}
	return max(pc.horizon, clears)
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
	// step is when the case's load steps, zero for none (backlogClears).
	step time.Duration
	// sameAsUnrestarted holds the run's time under need to the same case's
	// with no restart (the restored snapshot).
	sameAsUnrestarted bool
}

func startProbes() []startProbe {
	return []startProbe{
		{name: "load-step", learner: "floor", step: 2 * time.Minute, pc: probeCase{
			name: "step", pods: []float64{1, 1}, addedMu: 1, meanMu: 1,
			lambda: stepTo(1.2, 6, 2*time.Minute), horizon: 40 * time.Minute,
		}},
		{name: "decode-step", learner: "floor", step: restartStepAt, pc: restartStepCase(0, false, 0)},
		{name: "restart-before-step", learner: "floor", step: restartStepAt, pc: restartStepCase(15*time.Minute, false, 0)},
		{name: "restored-snapshot", learner: "floor", step: restartStepAt, pc: restartStepCase(15*time.Minute, true, 0), sameAsUnrestarted: true},
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
		pc.horizon = verdictHorizon(pc, sp.step, start)
		r, err := tryProbe(pc)
		if err != nil {
			return err
		}
		summary := fmt.Sprintf("start %s (published %.1f s): need %d, final %d at %s (bound %.1f), peak %d (ceiling %.1f), under need for %s (bound %s); fleet %s",
			start, r.startSeconds, r.need, r.final, pc.horizon, overBound(r.need), r.peak, peakCeiling(r.need), r.underFor, underBound(start), traceSummary(r.trace))
		if math.Abs(r.startSeconds-start.Seconds()) > 1 {
			return violation(kindBiased, "the start learner publishes %.1f s against a true start of %s; %s", r.startSeconds, start, summary)
		}
		if float64(r.peak) > peakCeiling(r.need) {
			return violation(kindOverPriced, "peak %d over the ceiling %.1f; %s", r.peak, peakCeiling(r.need), summary)
		}
		if float64(r.final) > overBound(r.need) {
			return violation(kindOverPriced, "over-ordered at the horizon: %d against %.1f; %s", r.final, overBound(r.need), summary)
		}
		if r.final < r.need-1 || r.underFor > underBound(start) {
			return violation(kindStale, "under-ordered: final %d, under need for %s; %s", r.final, r.underFor, summary)
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

// probeT is the probeTB a property case runs the probe with: it records a
// failure instead of failing a test, and FailNow ends the probe's goroutine.
type probeT struct {
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

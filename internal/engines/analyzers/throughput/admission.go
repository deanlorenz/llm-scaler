package throughput

import (
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learnrec"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
)

// What the ITL line's admission reads (analyzer-learner.md, PR 4b): the
// policy's band, steady gate and cap preemption rate; each variant's engine
// parameters, for its stated sequence cap and the arguments a held slope was
// fitted under; the engine's settle tracker; and how long a slope may be
// held. The window reads only the band today. The rest is the admission the
// line takes once it moves onto itl.Tracker, as the saturation and slo
// analyzers' lines do.

// batchCapNone is where an admission's sequence cap came from when it has
// none: the value the slo analyzer logs too (itl.MaxNumSeqsSourceNone).
// Nothing above the band is admitted.
const batchCapNone = itl.MaxNumSeqsSourceNone

// itlPolicy is what the ITL line reads of a model's policy.
type itlPolicy struct {
	// lo and hi are the band (analyzerTuning.itlBand).
	lo, hi float64
	// steady is the steady-state gate (analyzerTuning.steadyGate).
	steady itl.Steadiness
	// capPreemption is analyzerTuning.itlBand.capMaxPreemptionRate.
	capPreemption float64
}

// itlPolicyOf reads cfg's ITL keys through itl.PolicyReader:
// internal/config's tests import this package, so importing config here
// would be a cycle. *config.ScalingPolicy implements it, nil included, and
// config asserts that at compile time, so a renamed method fails the build
// rather than reading as the defaults here. A config that does not
// implement it (none in production) reads as the defaults: the default
// band, a zero gate (itl.Steadiness's defaults) and no preemption at the cap.
func itlPolicyOf(cfg domain.AnalyzerConfig) itlPolicy {
	r, ok := cfg.(itl.PolicyReader)
	if !ok {
		return itlPolicy{lo: itl.DefaultMinObservableK, hi: itl.DefaultMaxObservableK}
	}
	p := itlPolicy{steady: r.ITLSteadiness(), capPreemption: r.ITLCapPreemptionRate()}
	p.lo, p.hi = r.ITLBand()
	return p
}

// admission is the model's ITL admission under policy p: the policy's keys,
// the engine's settle tracker with its absence tolerance at the optimize
// interval, and the slope hold. What only the capacity store gives -- the
// variant's stated sequence cap and the arguments a held slope was fitted
// under -- is not read yet: nothing reads it until the line moves onto
// itl.Tracker (PR 4b-1, the arguments; 4b-3, the cap), so the store, though
// wired, costs no lookup a cycle. From then the cap is the one the engine's
// flags state (ExplicitMaxNumSeqs), else the one the llm-d.ai/max-num-seqs
// annotation declares (StatedBatchCap), never a learned or default cap, as
// the slo analyzer reads it; the arguments are ArgsTag, as the saturation
// analyzer tags its windows.
//
// Built whole although the window reads only its band today: it is the one
// value 4b-1 hands itl.Tracker unchanged, and building it on every cycle is
// what lets TestTheWiredAnalyzerReadsTheFleetAsABareOneDoes hold that
// nothing it carries changes what the window admits. It costs no lookup.
func (a *ThroughputAnalyzer) admission(p itlPolicy, namespace, modelID string) itl.Admission {
	adm := itl.Admission{
		MinK: p.lo, MaxK: p.hi,
		MaxNumSeqsSource:     batchCapNone,
		CapMaxPreemptionRate: p.capPreemption,
		Steady:               p.steady,
		SlopeHoldFor:         a.slopeHoldFor,
	}
	if a.settle != nil {
		adm.Settle = &itl.Settle{View: a.settle, Model: utils.GetNamespacedKey(namespace, modelID)}
	}
	if a.settleInterval > 0 {
		adm.AbsenceTolerance = settle.Tolerance(a.settleInterval)
	}
	return adm
}

// WithSettleView sets the settle tracker the ITL line reads each replica's
// settle from: the engine's, which it observes at the top of every cycle.
// Call it before the analyzer is used. Unset, the admission carries no
// settle tracker, and the line, once on itl.Tracker, judges each replica's
// settle from its own trace.
func (a *ThroughputAnalyzer) WithSettleView(v settle.View) *ThroughputAnalyzer {
	a.settle = v
	return a
}

// WithOptimizationInterval sets the controller's optimize interval, which
// sets how long a replica may go without a row and keep its settle
// (settle.Tolerance). Call it before the analyzer is used. Unset, the
// tolerance is the least, settle.Period.
func (a *ThroughputAnalyzer) WithOptimizationInterval(d time.Duration) *ThroughputAnalyzer {
	a.settleInterval = d
	return a
}

// WithSlopeHoldFor sets how long after it was fitted the ITL line's last
// slope may be held when its window cannot fit one: the controller's bucket
// retention, as for the other lines. Call it before the analyzer is used.
// Unset, a held slope has no age limit (itl.Admission.SlopeHoldFor).
func (a *ThroughputAnalyzer) WithSlopeHoldFor(d time.Duration) *ThroughputAnalyzer {
	a.slopeHoldFor = d
	return a
}

// WithCapacityStore sets the capacity store the analyzer reads each
// variant's engine parameters from. It reads them only, and writes nothing.
// Call it before the analyzer is used. Unset, no variant has a stated
// sequence cap or known engine arguments: nothing above the band is
// admitted, and a held slope is not bounded by the arguments.
func (a *ThroughputAnalyzer) WithCapacityStore(s *capacity.Store) *ThroughputAnalyzer {
	a.store = s
	return a
}

// StartConfig is what the builders set, for a recording's first line: a
// replay builds the analyzer from it.
func (a *ThroughputAnalyzer) StartConfig() learnrec.ThroughputStart {
	return learnrec.ThroughputStart{
		SlopeHoldFor:         a.slopeHoldFor,
		OptimizationInterval: a.settleInterval,
		SettleView:           a.settle != nil,
		CapacityStore:        a.store != nil,
	}
}

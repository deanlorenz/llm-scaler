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

// Where an admission's sequence cap came from (itl.Admission.MaxNumSeqsSource),
// the values the slo analyzer logs.
const (
	// batchCapFlags is a cap the engine's flags state.
	batchCapFlags = "flags"
	// batchCapDeclared is a cap the llm-d.ai/max-num-seqs annotation
	// declares, where the flags state none.
	batchCapDeclared = "declared"
	// batchCapNone is no stated cap: nothing above the band is admitted.
	batchCapNone = "none"
)

// itlPolicy is what the ITL line reads of a model's policy.
type itlPolicy struct {
	// lo and hi are the band (analyzerTuning.itlBand).
	lo, hi float64
	// steady is the steady-state gate (analyzerTuning.steadyGate).
	steady itl.Steadiness
	// capPreemption is analyzerTuning.itlBand.capMaxPreemptionRate.
	capPreemption float64
}

// itlPolicyOf reads cfg's ITL keys. Through interfaces: internal/config's
// tests import this package, so importing config here would be a cycle.
// *config.ScalingPolicy implements all three, nil included. A config that
// implements none reads as the defaults: the default band, a zero gate
// (itl.Steadiness's defaults) and no preemption at the cap.
func itlPolicyOf(cfg domain.AnalyzerConfig) itlPolicy {
	p := itlPolicy{lo: itl.DefaultMinObservableK, hi: itl.DefaultMaxObservableK}
	if c, ok := cfg.(interface{ ITLBand() (float64, float64) }); ok {
		p.lo, p.hi = c.ITLBand()
	}
	if c, ok := cfg.(interface{ ITLSteadiness() itl.Steadiness }); ok {
		p.steady = c.ITLSteadiness()
	}
	if c, ok := cfg.(interface{ ITLCapPreemptionRate() float64 }); ok {
		p.capPreemption = c.ITLCapPreemptionRate()
	}
	return p
}

// admission is the variant's ITL admission under policy p. The sequence cap
// is the one the engine's flags state, else the one the
// llm-d.ai/max-num-seqs annotation declares, and never a learned or default
// cap (itl.Admission.MaxNumSeqs): the slo analyzer reads the same two. The
// arguments are the ones the saturation analyzer tags its windows with
// (capacity.EngineParams.ArgsTag). With no store, or no record, there is no
// cap and no arguments.
func (a *ThroughputAnalyzer) admission(p itlPolicy, namespace, modelID, variant string) itl.Admission {
	params := a.engineParams(namespace, modelID, variant)
	batchCap, source := params.ExplicitMaxNumSeqs(), batchCapFlags
	if batchCap == 0 {
		batchCap, source = params.StatedBatchCap(), batchCapDeclared
	}
	if batchCap == 0 {
		source = batchCapNone
	}
	adm := itl.Admission{
		MinK: p.lo, MaxK: p.hi,
		MaxNumSeqs: batchCap, MaxNumSeqsSource: source,
		CapMaxPreemptionRate: p.capPreemption,
		Steady:               p.steady,
		SlopeHoldFor:         a.slopeHoldFor,
		Args:                 params.ArgsTag(),
	}
	if a.settle != nil {
		adm.Settle = &itl.Settle{View: a.settle, Model: utils.GetNamespacedKey(namespace, modelID)}
	}
	if a.settleInterval > 0 {
		adm.AbsenceTolerance = settle.Tolerance(a.settleInterval)
	}
	return adm
}

// engineParams is the variant's engine parameters from the capacity store,
// nil when there is no store or no record.
func (a *ThroughputAnalyzer) engineParams(namespace, modelID, variant string) *capacity.EngineParams {
	if a.store == nil {
		return nil
	}
	if rec := a.store.Get(namespace, modelID, variant); rec != nil {
		return rec.EngineParams
	}
	return nil
}

// WithSettleView sets the settle tracker the ITL line reads each replica's
// settle from: the engine's, which it observes at the top of every cycle.
// Call it before the analyzer is used.
func (a *ThroughputAnalyzer) WithSettleView(v settle.View) *ThroughputAnalyzer {
	a.settle = v
	return a
}

// WithOptimizationInterval sets the controller's optimize interval, which
// sets how long a replica may go without a row and keep its settle
// (settle.Tolerance). Call it before the analyzer is used.
func (a *ThroughputAnalyzer) WithOptimizationInterval(d time.Duration) *ThroughputAnalyzer {
	a.settleInterval = d
	return a
}

// WithSlopeHoldFor sets how long after it was fitted the ITL line's last
// slope may be held when its window cannot fit one: the controller's bucket
// retention, as for the other lines. Call it before the analyzer is used.
func (a *ThroughputAnalyzer) WithSlopeHoldFor(d time.Duration) *ThroughputAnalyzer {
	a.slopeHoldFor = d
	return a
}

// WithCapacityStore sets the capacity store the analyzer reads each
// variant's engine parameters from. It reads them only, and writes nothing.
// Call it before the analyzer is used.
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

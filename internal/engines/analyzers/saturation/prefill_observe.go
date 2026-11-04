package saturation

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
)

// Prefill capacity, observed (docs/proposals/slo-capacity-planner.md, PR P1;
// the model is docs/proposals/slo-capacity-planner-ttft.md). Each prefill
// variant's rows go to internal/signals/prefill, which learns V_P, a and f,
// and the result is logged at perReplicaLogLevel as prefill-capacity-observed.
// Nothing sizes from it.
//
// It is wired here, not in the slo analyzer, because two of its inputs are
// this analyzer's: whether decode is back-pressuring prefill this cycle
// (decodeSaturated, the test computeK2 applies), and the prefill lower-bound
// window (prefill_bound.go), V_P's third source. This analyzer also runs for
// every model, where the slo analyzer runs only when a policy lists it.

// SwitchPrefillCapacityObserve is the replay switch for the observation. On,
// each prefill variant's rows are observed and logged every cycle. Off,
// nothing is observed or logged. It moves no golden output: the observation
// writes no state any output reads.
const SwitchPrefillCapacityObserve = "prefill-capacity-observe"

// SwitchPrefillBusyMinutes is the replay switch for V_P's first source,
// busy minutes. On, each prefill row's queue minimum over the minute
// (domain.ReplicaMetrics.QueueMin, where QueueMinKnown) goes to the
// observation with the pods whose KV is full (prefillKVFull), and a minute
// whose queue never emptied, on a pod whose KV was not full, is a busy
// minute. Off, neither goes, and V_P comes only from the two lower-bound
// sources, as P1 shipped. It moves no golden output: the observation writes no state any
// output reads, and no committed recording carries a queue minimum. The
// collector reads the queue minimum only while a learner recording is on
// (collector SetRecordTTFTInputs); with none on, no row carries one and the
// switch changes nothing.
const SwitchPrefillBusyMinutes = "prefill-busy-minutes"

// prefillKVFull is the pods of rows whose KV cache is full (kvFull at
// kvCacheThreshold), nil when none is. A full prefill pod can hold a queue
// it cannot schedule, so its minute is not a busy one.
func prefillKVFull(rows []domain.ReplicaMetrics, kvCacheThreshold float64) map[string]bool {
	var out map[string]bool
	for _, rm := range rows {
		if !kvFull(rm, kvCacheThreshold) {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[rm.PodName] = true
	}
	return out
}

// prefillObserveKey names a prefill variant's observed state: the variant
// key's fields (itlWindowKey), and the engine arguments it runs (windowArgs)
// behind stashKey's separator when they are known. V_P is a max over the
// samples a key keeps, for prefill.Retention, so a key that outlived a
// redeploy with a lower --max-num-batched-tokens kept the old engine's peak:
// with the arguments in the key, the new engine starts its own state and the
// old one ages out. Only the arguments: like every window the arguments
// bound, an image alone does not start a new key; an operator resets one
// with llm-d.ai/forget-learned-state (forgetVariant).
//
// The accelerator is read through stableAcceleratorFor, which touches
// nothing: observing must not keep the accelerator memo alive.
func (a *SaturationAnalyzer) prefillObserveKey(namespace, modelID, variant, accelerator string, gpuCount int,
	params *capacity.EngineParams) string {
	key := fmt.Sprintf("%s|%s|%s|%s|%d", namespace, modelID, variant,
		a.stableAcceleratorFor(namespace, variant, accelerator), gpuCount)
	if args := windowArgs(params); args != "" {
		key = stashKey(key, args)
	}
	return key
}

// prefillBoundStashKey names a variant's back-pressure bound for the cycle
// in progress (notePrefillBound writes it, observePrefill reads it).
func prefillBoundStashKey(namespace, modelID, variant string) string {
	return namespace + "|" + modelID + "|" + variant
}

// stashedBound is a back-pressure bound stashed for the cycle in progress,
// with when it was stashed (for the eviction sweep).
type stashedBound struct {
	bound prefill.Bound
	at    time.Time
}

// stashPrefillBound keeps the bound notePrefillBound read for the variant,
// in computed tokens/s, for observePrefill. It is written once per replica
// priced from the bound, and the replicas of one variant can read different
// windows (one per input bucket): the highest is kept, as the bound's own
// window keeps its highest reading, each being a floor on the true rate.
// Called with a.mu held.
func (a *SaturationAnalyzer) stashPrefillBound(rm domain.ReplicaMetrics, tokens float64, samples int) {
	if !a.prefillObserve {
		return
	}
	k := prefillBoundStashKey(rm.Namespace, rm.ModelID, rm.VariantName)
	now := a.now()
	if prev, ok := a.prefillBounds[k]; ok && prev.at.Equal(now) && prev.bound.Tokens >= tokens {
		return
	}
	a.prefillBounds[k] = stashedBound{bound: prefill.Bound{Tokens: tokens, Samples: samples}, at: now}
}

// prefillBoundsStaleAfter is how old a stashed bound is when the eviction
// sweep drops it. A bound is stashed and taken within one Analyze; one left
// longer belongs to a cycle that never took it.
const prefillBoundsStaleAfter = time.Minute

// evictPrefillObservationLocked sweeps the observation's keys with no cycle
// for prefill.Retention and the stashed bounds no cycle took, and returns
// how many of each. Callers hold a.mu; the observer's lock is its own.
func (a *SaturationAnalyzer) evictPrefillObservationLocked(now time.Time) (keys, bounds int) {
	keys = a.prefillObserver.Sweep(now)
	for k, sb := range a.prefillBounds {
		if now.Sub(sb.at) > prefillBoundsStaleAfter {
			delete(a.prefillBounds, k)
			bounds++
		}
	}
	return keys, bounds
}

// takePrefillBounds returns and clears the bounds stashed for the cycle's
// model, and whether the observation is on. Every cycle clears them, the
// ones whose pricing failed too (Analyze), so a bound is only ever read by
// the cycle that stashed it; and it clears the model's entries for variants
// the cycle does not have, which no cycle would take.
func (c *cycle) takePrefillBounds() (map[string]prefill.Bound, bool) {
	a := c.a
	a.mu.Lock()
	defer a.mu.Unlock()
	bounds := map[string]prefill.Bound{}
	prefix := prefillBoundStashKey(c.input.Namespace, c.input.ModelID, "")
	for k, sb := range a.prefillBounds {
		variant, ok := strings.CutPrefix(k, prefix)
		if !ok || prefillBoundStashKey(c.input.Namespace, c.input.ModelID, variant) != k {
			continue
		}
		if _, ours := c.rolesByVariant[variant]; ours {
			bounds[variant] = sb.bound
		}
		delete(a.prefillBounds, k)
	}
	return bounds, a.prefillObserve
}

// observePrefill observes each prefill variant's rows of the cycle and logs
// what its key has learned. After priceReplicas, so the bound each variant's
// replicas read this cycle is stashed.
func (c *cycle) observePrefill() {
	a := c.a
	bounds, on := c.takePrefillBounds()
	if !on {
		return
	}
	rows := map[string][]domain.ReplicaMetrics{}
	for _, rm := range c.input.ReplicaMetrics {
		if domain.CanonicalRole(c.rolesByVariant[rm.VariantName]) == domain.RolePrefill {
			rows[rm.VariantName] = append(rows[rm.VariantName], rm)
		}
	}
	view := &prefill.Settle{View: a.settleView(), Model: settleModel(c.input.Namespace, c.input.ModelID)}
	for _, variant := range slices.Sorted(maps.Keys(rows)) {
		var engine inferenceengine.Engine
		params := engineParamsFor(a, c.input.Namespace, c.input.ModelID, variant)
		if params != nil {
			engine = params.Engine
		}
		key := a.prefillObserveKey(c.input.Namespace, c.input.ModelID, variant, c.accelByVariant[variant],
			c.gpusByVariant[variant], params)
		// queueMinPods counts the pods that read a queue minimum whether or
		// not the switch feeds them: with busyMinutesOn, a zero says the
		// collector read none (no learner recording is on).
		queueMin := prefill.QueueMins(rows[variant])
		queueMinPods := len(queueMin)
		var kvFullPods map[string]bool
		if a.prefillBusyMinutes {
			kvFullPods = prefillKVFull(rows[variant], c.cfg.KvCacheThreshold)
		} else {
			queueMin = nil
		}
		est := a.prefillObserver.Observe(prefill.Cycle{
			Key: key, Engine: engine, Disaggregated: true, Rows: rows[variant],
			QueueMin: queueMin, KVFull: kvFullPods, DownstreamSaturated: c.decodeSaturated,
			Bound: bounds[variant], Settle: view, Now: a.now(),
		})
		c.logger.V(perReplicaLogLevel).Info("prefill-capacity-observed",
			"modelID", c.input.ModelID, "namespace", c.input.Namespace, "variant", variant, "key", key,
			"engine", string(engine), "pods", len(rows[variant]),
			"vp", est.VP, "vpSource", string(est.VPSource), "vpLowerBound", est.VPLowerBound,
			"vpLowerBoundReason", string(est.VPLowerBoundReason),
			"vpRefused", string(est.VPRefused), "vpSamples", est.VPSamples,
			"slopeBound", est.SlopeBound, "vpAboveSlopeBound", est.VPAboveSlopeBound,
			"a", est.A, "aIntercept", est.AIntercept, "aOverhead", est.AOverhead,
			"aOverheadKnown", est.AOverheadKnown, "aRefused", string(est.ARefused), "aSamples", est.ASamples,
			"aPromptMin", est.APromptMin, "aPromptMax", est.APromptMax,
			"queuedMinutes", est.VPQueuedMinutes, "longestQueuedRun", est.VPLongestQueuedRun,
			"busyMinutes", est.VPBusyMinutes, "busyMinutesOn", a.prefillBusyMinutes,
			"queueMinPods", queueMinPods, "kvFullPods", len(kvFullPods),
			"f", est.F, "fRefused", string(est.FRefused), "fSamples", est.FSamples,
			"downstreamSaturated", c.decodeSaturated,
			"admitted", est.Admitted, "refused", est.Refused)
	}
}

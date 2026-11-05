package learnreplay

import (
	"maps"
	"slices"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// What a recording says of a TTFT question at a cycle: the what-if planner's
// TTFT half (docs/proposals/slo-capacity-planner.md, "Recording mode, as
// built"). P1's estimate is read from the saturation analyzer's stash, the
// same handle the slo analyzer reads it through, and the rest from the
// cycles' rows by the rules of docs/proposals/slo-tiers.md, "S1 decisions".
// All of it is read, none of it is fed back: the analyzers run as they do
// without it, so it needs no replay switch.

// PrefillState is one prefill variant at the cycle, as a TTFT question reads
// it.
type PrefillState struct {
	Variant string
	// Pods is N_p, the serving pods at the cycle (prefill.Serving), the rule
	// P1 counts its per-count f by.
	Pods int
	// Estimate is P1's estimate of the variant, written at this cycle;
	// EstimateRead is false when none was (the observation was off, or the
	// cycle gave it no rows).
	Estimate     prefill.Estimate
	EstimateRead bool
	// PromptTokens is E[I_eff], computed tokens over requests, over the
	// variant's latest shapeCycles cycles with traffic, so that a recording
	// that ends idle is priced at the traffic it last served; 0 with none.
	// PromptFromCycle, PromptToCycle and PromptCycles say which cycles.
	PromptTokens                                 float64
	PromptFromCycle, PromptToCycle, PromptCycles int
	// RawShare is f_raw, the busiest pod's share of the computed-token rate
	// over the mean share, not de-biased: the largest over the latest
	// rawShareCycles cycles at the cycle's pod count. 0 with no
	// computed-token rate on any row. The slo-tiers rule reads settled rows;
	// here a pod still warming up reads low, which raises the share, the
	// side that rule permits.
	RawShare float64
}

// DecodeTTFTState is what a decode variant's rows say of the decode side of
// a TTFT, read from its serving rows.
type DecodeTTFTState struct {
	// EngineTTFT is the decode engine's TTFT: of the latest
	// decodeTTFTCycles cycles that read one, the largest, each a mean over
	// the rows weighted by their request rate, with the mean effective prompt
	// of the same cycle (the pair is never split). Zero when unread.
	EngineTTFT whatif.DecodeTTFT
	// TransferSeconds is the mean KV transfer time over the rows that read
	// one, over the latest shapeCycles cycles with a reading; 0 with none.
	TransferSeconds float64
}

// The windows the rows are read over.
const (
	// rawShareCycles is slo-tiers' window for f_raw.
	rawShareCycles = 5
	// decodeTTFTCycles is slo-tiers' window for the decode engine's TTFT.
	decodeTTFTCycles = 5
)

// promptSample is one cycle's computed tokens and requests of a prefill
// variant, per second.
type promptSample struct {
	cycle             int
	computed, request float64
}

// shareSample is one cycle's f_raw of a prefill variant, and its pod count.
type shareSample struct {
	pods  int
	share float64
}

// ttftSamples is the windows of the rows a TTFT question reads, by variant.
type ttftSamples struct {
	prompts    map[string][]promptSample
	shares     map[string][]shareSample
	engineTTFT map[string][]whatif.DecodeTTFT
	transfers  map[string][]float64
}

func newTTFTSamples() *ttftSamples {
	return &ttftSamples{prompts: map[string][]promptSample{}, shares: map[string][]shareSample{},
		engineTTFT: map[string][]whatif.DecodeTTFT{}, transfers: map[string][]float64{}}
}

// keepLast is s with v appended, holding the last n.
func keepLast[T any](s []T, v T, n int) []T {
	s = append(s, v)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

// rolesOf is each variant's role, as the cycle states it.
func rolesOf(in domain.AnalyzerInput) map[string]string {
	roles := map[string]string{}
	for _, vs := range in.VariantStates {
		roles[vs.VariantName] = vs.Role
	}
	return roles
}

// decodeRows is one decode variant's readings in one cycle.
type decodeRows struct {
	ttftWeighted, ttftWeight, ttftPlain float64
	ttftRows                            int
	transfer                            float64
	transferRows                        int
}

// note takes one cycle's rows.
func (t *ttftSamples) note(in domain.AnalyzerInput, cycle int) {
	roles := rolesOf(in)
	rates := map[string][]float64{}
	computed, requests := map[string]float64{}, map[string]float64{}
	decode := map[string]*decodeRows{}
	for _, rm := range in.ReplicaMetrics {
		if !prefill.Serving(rm) {
			continue
		}
		v := rm.VariantName
		switch domain.CanonicalRole(roles[v]) {
		case domain.RolePrefill:
			if _, ok := rates[v]; !ok {
				rates[v] = nil
			}
			if !rm.PrefillComputedTokenRateKnown {
				continue
			}
			rates[v] = append(rates[v], rm.PrefillComputedTokenRate)
			if rm.RequestRateKnown && rm.RequestRate > 0 {
				computed[v] += rm.PrefillComputedTokenRate
				requests[v] += rm.RequestRate
			}
		case domain.RoleDecode:
			d := decode[v]
			if d == nil {
				d = &decodeRows{}
				decode[v] = d
			}
			if rm.AvgTTFTKnown {
				d.ttftRows++
				d.ttftPlain += rm.AvgTTFT
				if rm.RequestRateKnown && rm.RequestRate > 0 {
					d.ttftWeighted += rm.AvgTTFT * rm.RequestRate
					d.ttftWeight += rm.RequestRate
				}
			}
			if rm.AvgKVTransferTimeKnown {
				d.transfer += rm.AvgKVTransferTime
				d.transferRows++
			}
		}
	}
	var allComputed, allRequests float64
	for v := range computed {
		if !(computed[v] > 0) {
			continue
		}
		allComputed += computed[v]
		allRequests += requests[v]
		t.prompts[v] = keepLast(t.prompts[v], promptSample{cycle: cycle, computed: computed[v], request: requests[v]}, shapeCycles)
	}
	for v, rs := range rates {
		t.shares[v] = keepLast(t.shares[v], shareSample{pods: len(rs), share: rawShare(rs)}, rawShareCycles)
	}
	for v, d := range decode {
		if d.transferRows > 0 {
			t.transfers[v] = keepLast(t.transfers[v], d.transfer/float64(d.transferRows), shapeCycles)
		}
		if d.ttftRows == 0 || !(allComputed > 0) {
			continue
		}
		mean := d.ttftPlain / float64(d.ttftRows)
		if d.ttftWeight > 0 {
			mean = d.ttftWeighted / d.ttftWeight
		}
		t.engineTTFT[v] = keepLast(t.engineTTFT[v], whatif.DecodeTTFT{Seconds: mean, PromptTokens: allComputed / allRequests}, decodeTTFTCycles)
	}
}

// rawShare is the busiest pod's rate over the mean, 0 when no pod has one.
func rawShare(rates []float64) float64 {
	var sum, top float64
	for _, r := range rates {
		sum += r
		top = max(top, r)
	}
	if !(sum > 0) {
		return 0
	}
	return top * float64(len(rates)) / sum
}

// promptOf is a prefill variant's mean effective prompt over its samples,
// and the cycles they span: each cycle weighs by its requests, which is
// computed tokens over requests across them.
func promptOf(samples []promptSample) (tokens float64, from, to, cycles int) {
	if len(samples) == 0 {
		return 0, 0, 0, 0
	}
	var computed, requests float64
	for _, s := range samples {
		computed += s.computed
		requests += s.request
	}
	return computed / requests, samples[0].cycle, samples[len(samples)-1].cycle, len(samples)
}

// shareAt is the largest f_raw of the samples at pods, read back from the
// last and stopping where the count differs: the window restarts when the
// pod count changes.
func shareAt(samples []shareSample, pods int) float64 {
	top := 0.0
	for _, s := range slices.Backward(samples) {
		if s.pods != pods {
			break
		}
		top = max(top, s.share)
	}
	return top
}

// fillTTFT sets what the state says of a TTFT question, from the samples
// through the cycle in and from P1's estimates.
func (r *replayer) fillTTFT(st *State, in domain.AnalyzerInput, t *ttftSamples) {
	roles := rolesOf(in)
	pods := map[string]int{}
	for _, rm := range in.ReplicaMetrics {
		if prefill.Serving(rm) {
			pods[rm.VariantName]++
		}
	}
	for _, v := range slices.Sorted(maps.Keys(roles)) {
		if domain.CanonicalRole(roles[v]) != domain.RolePrefill {
			continue
		}
		ps := PrefillState{Variant: v, Pods: pods[v], RawShare: shareAt(t.shares[v], pods[v])}
		ps.PromptTokens, ps.PromptFromCycle, ps.PromptToCycle, ps.PromptCycles = promptOf(t.prompts[v])
		ps.Estimate, ps.EstimateRead = estimateAt(r.sat.PrefillEstimate, in.Namespace, in.ModelID, v, st.At)
		st.Prefill = append(st.Prefill, ps)
	}
	for i := range st.Variants {
		vs := &st.Variants[i]
		vs.DecodeTTFT.EngineTTFT = largestTTFT(t.engineTTFT[vs.Variant])
		if xs := t.transfers[vs.Variant]; len(xs) > 0 {
			sum := 0.0
			for _, x := range xs {
				sum += x
			}
			vs.DecodeTTFT.TransferSeconds = sum / float64(len(xs))
		}
	}
	st.FlowControlSeries = flowControlSeries(in)
}

// estimateAt is a variant's estimate read through read, when it was stashed
// at now: one left from an earlier cycle, which did observe the variant, is
// not this cycle's.
func estimateAt(read func(namespace, modelID, variant string) (prefill.Estimate, time.Time, bool),
	namespace, modelID, variant string, now time.Time) (prefill.Estimate, bool) {
	est, at, ok := read(namespace, modelID, variant)
	if !ok || !at.Equal(now) {
		return prefill.Estimate{}, false
	}
	return est, true
}

// largestTTFT is the sample with the largest TTFT, as a pair: S1 takes the
// largest and never the largest of each.
func largestTTFT(samples []whatif.DecodeTTFT) whatif.DecodeTTFT {
	var top whatif.DecodeTTFT
	for _, d := range samples {
		if d.Seconds > top.Seconds {
			top = d
		}
	}
	return top
}

// flowControlSeries reports whether any of the router's flow-control series
// exists for the model at the cycle.
func flowControlSeries(in domain.AnalyzerInput) bool {
	return in.SchedulerQueue != nil || len(in.FlowControlPools) > 0 ||
		(in.RouterLatency != nil && len(in.RouterLatency.FlowControlWaitBuckets) > 0)
}

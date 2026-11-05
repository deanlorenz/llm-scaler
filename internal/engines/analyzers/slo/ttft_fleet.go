package slo

import (
	"math"
	"slices"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// What the TTFT model is given for one prefill variant
// (docs/proposals/slo-tiers.md, S1 decisions 1 to 3). One rule runs through
// it: only err toward more replicas. An unknown input takes the value that
// asks for more, and is flagged.

const (
	// fFloor is the least f assumed on more than one pod. Not measured: it
	// is the TTFT model's own synthetic multi-pod case, near P1's 2:1:1:1
	// example (1.6). The prefill-stress run at three pods read a raw share of
	// at most 1.33, so it held there.
	fFloor = 1.5
	// ttftWindowCycles is how many cycles the raw share and the decode
	// engine's TTFT are held over.
	ttftWindowCycles = 5
	// assumedPromptCV is the prompt spread with no histogram: not measured,
	// the top of the spread taken as plausible.
	assumedPromptCV = 2.0
	// maxMeasuredPromptCV is the most spread passed: above it a larger cv
	// stops pricing a longer p90 at low load.
	maxMeasuredPromptCV = 3.0
	// flowControlQueueThreshold and flowControlKVThreshold are the EPP
	// detector's upstream defaults, which this repository's install leaves
	// unchanged.
	flowControlQueueThreshold = 5.0
	flowControlKVThreshold    = 0.8
)

// The analyzer's own flags, added to the TTFT model's on an answer.
const (
	// AssumedPoissonArrivals is every S1 answer: arrivals are taken as
	// Poisson, which over-promises on bursty traffic.
	AssumedPoissonArrivals whatif.Flag = "AssumedPoissonArrivals"
	// PromptSpreadClamped is a measured prompt spread with mass past the
	// histogram's last bound, or above maxMeasuredPromptCV: priced at that top.
	PromptSpreadClamped whatif.Flag = "PromptSpreadClamped"
	// FlowControlMismatch is a declared slo.flowControl the router's series
	// contradict: priced as declared.
	FlowControlMismatch whatif.Flag = "FlowControlMismatch"
)

// Where f came from, on the log line.
const (
	fSourceLearned = "learned"
	fSourceOnePod  = "one-pod"
	fSourceAssumed = "assumed"
)

// Where the prompt's spread came from, on the log line.
const (
	cvSourceMeasured = "measured"
	cvSourceAssumed  = "assumed"
)

// ttftInputs is one prefill variant's figures of the cycle: everything
// ttftFleet prices from, so it is a pure function of the pod count.
type ttftInputs struct {
	// est is P1's estimate, base what the model takes of it
	// (whatif.PrefillFromEstimate).
	est  prefill.Estimate
	base whatif.Prefill
	// estimateAt is when the estimate was stashed; zero with none this cycle.
	estimateAt time.Time
	// pods is N_p, the variant's Ready replicas (prefill.Serving).
	pods int
	// fRaw is the largest raw share over the window; 0 with none.
	fRaw float64
	// prompt is E[I_eff] and spread its coefficient of variation.
	prompt float64
	spread promptSpread
	// transfer is the KV transfer per prompt token, seconds; 0 unmeasured.
	transfer float64
	// decode is the decode side; nil with no decode line.
	decode *whatif.Decode
	// decodeVariant names the variant behind decode, for the log.
	decodeVariant string
	decodeTTFT    whatif.DecodeTTFT
	flow          whatif.FlowControl
	mismatch      bool
}

// imbalance is f at pods, whether it is assumed, and where it came from.
//
// At the current count: P1's f at that count, from minutes taken at it only;
// with one pod, none (the model prices one pod as 1 exactly); otherwise
// assumed, min(N_p, max(1.5, f_raw)). At another count: min(N', max(1.5, f)),
// so a learned f is never carried unchanged to a new count, and one pod's
// exact 1 never reaches two.
func (in ttftInputs) imbalance(pods int) (f float64, assumed bool, source string) {
	f, assumed, source = in.imbalanceNow()
	if pods == in.pods {
		return f, assumed, source
	}
	f = min(float64(pods), max(fFloor, f))
	return f, pods > 1, fSourceAssumed
}

func (in ttftInputs) imbalanceNow() (float64, bool, string) {
	if f, ok := in.est.FByPods[in.pods]; ok {
		return f, false, fSourceLearned
	}
	if in.pods <= 1 {
		return 0, false, fSourceOnePod
	}
	return min(float64(in.pods), max(fFloor, in.fRaw)), true, fSourceAssumed
}

// ttftFleet is the fleet the TTFT model prices at pods prefill replicas, and
// the analyzer's own flags on it. S1 calls it at the current count; S2's
// search calls it at each other count.
func ttftFleet(in ttftInputs, pods int) (whatif.TTFTFleet, []whatif.Flag) {
	p := in.base
	p.Replicas = pods
	p.Imbalance, p.ImbalanceAssumed, _ = in.imbalance(pods)
	p.PromptTokens = in.prompt
	p.PromptCV, p.PromptCVAssumed = in.spread.cv, in.spread.assumed
	p.TransferSecondsPerToken = in.transfer
	fl := whatif.TTFTFleet{Prefill: p, Decode: in.decode, FlowControl: in.flow}
	if in.decode == nil {
		fl.DecodeTTFT = in.decodeTTFT
	}
	flags := []whatif.Flag{AssumedPoissonArrivals}
	if in.spread.clamped {
		flags = append(flags, PromptSpreadClamped)
	}
	if in.mismatch {
		flags = append(flags, FlowControlMismatch)
	}
	return fl, flags
}

// flowControlState is what the tier declares, or else what the router's series
// say, and whether the two disagree. seriesSeen is whether the router's
// flow-control queue series exist for the model. Declared enabled with no
// series counts as a mismatch, the conservative reading; whether an on gate
// publishes its queue series while empty is not verified.
func flowControlState(p *config.SLOPolicy, seriesSeen bool) (whatif.FlowControl, bool) {
	on := whatif.FlowControl{State: whatif.FlowControlOn, QueueThreshold: flowControlQueueThreshold,
		KVThreshold: flowControlKVThreshold}
	switch {
	case p != nil && p.FlowControl == config.SLOFlowControlEnabled:
		return on, !seriesSeen
	case p != nil && p.FlowControl == config.SLOFlowControlDisabled:
		return whatif.FlowControl{State: whatif.FlowControlOff}, seriesSeen
	case seriesSeen:
		return on, false
	}
	return whatif.FlowControl{State: whatif.FlowControlNoSeries}, false
}

// promptSpread is the coefficient of variation of the effective prompt, and
// how it was had.
type promptSpread struct {
	cv float64
	// assumed is no histogram read; clamped is PromptSpreadClamped.
	assumed, clamped bool
}

// readPromptSpread reads the spread from the computed-token histogram of the
// variant's serving rows, pooled: each bucket's rate is summed over the pods
// and cv computed once. mean is the exact E[I_eff].
//
// The second moment uses each bucket's upper bound, so cv reads high, never
// low: that costs replicas, not safety. Mass past the last finite bound has no
// upper bound, so cv is the top and flagged, as is a cv above it. With no
// histogram it is assumedPromptCV, flagged by the model.
func readPromptSpread(rows []domain.ReplicaMetrics, mean float64) promptSpread {
	assumed := promptSpread{cv: assumedPromptCV, assumed: true}
	pooled := map[float64]float64{}
	for _, rm := range rows {
		if !prefill.Serving(rm) {
			continue
		}
		for _, b := range rm.ComputedTokensBuckets {
			if !math.IsNaN(b.UpperBound) && whatif.Finite(b.Rate) && b.Rate >= 0 {
				pooled[b.UpperBound] += b.Rate
			}
		}
	}
	total, ok := pooled[math.Inf(1)]
	if !ok || !(total > 0) || !(mean > 0) {
		return assumed
	}
	bounds := make([]float64, 0, len(pooled))
	for b := range pooled {
		if !math.IsInf(b, 1) {
			bounds = append(bounds, b)
		}
	}
	slices.Sort(bounds)
	var second, below float64
	for _, u := range bounds {
		// Rates over a window can dip between bounds; a dip is no mass.
		n := max(pooled[u]-below, 0)
		second += n * u * u
		below = max(below, pooled[u])
	}
	if total > below {
		return promptSpread{cv: maxMeasuredPromptCV, clamped: true}
	}
	cv := math.Sqrt(max(second/total/(mean*mean)-1, 0))
	if !whatif.Finite(cv) || cv > maxMeasuredPromptCV {
		return promptSpread{cv: maxMeasuredPromptCV, clamped: true}
	}
	return promptSpread{cv: cv}
}

// rawShare is f_raw's reading of one cycle: the busiest pod's share of the
// variant's computed-token rate over the mean share, over its Ready, settled
// rows. A row with no rate counts as a pod that computed nothing, which reads
// high, the permitted side. False with fewer than two such rows or no rate on
// any.
func rawShare(rows []domain.ReplicaMetrics, view *prefill.Settle, now time.Time) (float64, bool) {
	var sum, busiest float64
	n := 0
	for _, rm := range rows {
		if !prefill.Serving(rm) || !view.Settled(rm, now) {
			continue
		}
		n++
		if r := rm.PrefillComputedTokenRate; rm.PrefillComputedTokenRateKnown && whatif.Finite(r) && r > 0 {
			sum += r
			busiest = max(busiest, r)
		}
	}
	if n < 2 || !(sum > 0) {
		return 0, false
	}
	return busiest * float64(n) / sum, true
}

// imbalanceWindow is a prefill variant's raw shares over the last
// ttftWindowCycles cycles at its current Ready count.
type imbalanceWindow struct {
	pods   int
	shares []float64
	seen   time.Time
}

// noteShare folds the cycle's raw share into the variant's window, reset when
// the Ready count changes, and returns the window's largest; 0 with none.
func (a *Analyzer) noteShare(key string, pods int, share float64, ok bool, now time.Time) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.imbalance[key]
	if w == nil || w.pods != pods {
		w = &imbalanceWindow{pods: pods}
		a.imbalance[key] = w
	}
	w.seen = now
	if ok {
		w.shares = append(w.shares, share)
		if len(w.shares) > ttftWindowCycles {
			w.shares = w.shares[1:]
		}
	}
	var largest float64
	for _, s := range w.shares {
		largest = max(largest, s)
	}
	return largest
}

// decodeTTFTWindow is a decode variant's measured TTFT over the last
// ttftWindowCycles cycles, each with the prompt of its cycle. A cycle with no
// reading holds an empty slot, so a figure ages out when the rows stop
// carrying one.
type decodeTTFTWindow struct {
	cycles []whatif.DecodeTTFT
	seen   time.Time
}

// noteDecodeTTFT folds the cycle's decode-engine TTFT into the variant's
// window and returns the largest, as a pair from one cycle; zero with none.
func (a *Analyzer) noteDecodeTTFT(key string, cycle whatif.DecodeTTFT, now time.Time) whatif.DecodeTTFT {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.decodeTTFT[key]
	if w == nil {
		w = &decodeTTFTWindow{}
		a.decodeTTFT[key] = w
	}
	w.seen = now
	w.cycles = append(w.cycles, cycle)
	if len(w.cycles) > ttftWindowCycles {
		w.cycles = w.cycles[1:]
	}
	var largest whatif.DecodeTTFT
	for _, c := range w.cycles {
		if c.Seconds > largest.Seconds {
			largest = c
		}
	}
	return largest
}

// decodeEngineTTFT is the decode rows' mean AvgTTFT, weighted by request rate
// (equally when no row read one), and whether any row carries it. rows are one
// decode variant's.
func decodeEngineTTFT(rows []domain.ReplicaMetrics) (float64, bool) {
	var weighted, weight, sum float64
	n := 0
	for _, rm := range rows {
		if !prefill.Serving(rm) || !rm.AvgTTFTKnown || !whatif.Finite(rm.AvgTTFT) || !(rm.AvgTTFT > 0) {
			continue
		}
		sum += rm.AvgTTFT
		n++
		if rr := rm.RequestRate; whatif.Finite(rr) && rr > 0 {
			weighted += rr * rm.AvgTTFT
			weight += rr
		}
	}
	switch {
	case weight > 0:
		return weighted / weight, true
	case n > 0:
		return sum / float64(n), true
	}
	return 0, false
}

// transferPerToken is the mean AvgKVTransferTime over the serving rows that
// carry it, per token of the mean prompt; 0 where none does, or with no prompt.
func transferPerToken(rows []domain.ReplicaMetrics, prompt float64) float64 {
	var sum float64
	n := 0
	for _, rm := range rows {
		if prefill.Serving(rm) && rm.AvgKVTransferTimeKnown && whatif.Finite(rm.AvgKVTransferTime) && rm.AvgKVTransferTime > 0 {
			sum += rm.AvgKVTransferTime
			n++
		}
	}
	if n == 0 || !(prompt > 0) {
		return 0
	}
	return sum / float64(n) / prompt
}

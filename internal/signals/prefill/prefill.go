package prefill

import (
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle/settlerow"
)

// SteadyFor is how long a replica must have been settled before its rows are
// admitted: the settle tracker's start, a return of load after an idle run,
// or a Ready row after one that was not, at least this long ago. The one-
// minute rates the rows carry cover a replica's warm-up for about that long,
// as for the ITL line (itl.DefaultSteadyFor).
const SteadyFor = 2 * time.Minute

// MaxPromptDrift is how far, relatively, a replica's effective prompt length
// may have moved within SteadyFor and its row still be steady for the fit of
// a: the steady gate.
const MaxPromptDrift = 0.10

// SampleSpacing is the spacing of the samples kept per replica. The rows are
// one-minute rates read every cycle, so two rows inside a minute share most
// of their window: a row within SampleSpacing of the replica's last sample
// rewrites it instead of adding one, as mu's samples do.
const SampleSpacing = time.Minute

// Retention is how long a sample is kept, and how long a key or a replica
// with no row is remembered.
const Retention = 6 * time.Hour

// maxSamples bounds a replica's samples whatever the cycle interval: six
// hours at one a minute.
const maxSamples = 360

// idleRows is how many idle rows make a return of load a settle, as for the
// ITL line.
const idleRows = 2

// Source is where V_P came from, in the order the TTFT page ranks them.
type Source string

const (
	// SourceBusyMinutes is the median computed-token rate over minutes in
	// which the pod's queue was above zero on every sample and decode was not
	// back-pressuring it.
	SourceBusyMinutes Source = "busy-minutes"
	// SourcePeakUnderQueue is the highest computed-token rate sustained over
	// PeakRun minutes in each of which the pod held a queue on some sample. A
	// lower bound.
	SourcePeakUnderQueue Source = "peak-under-queue"
	// SourceBackPressureBound is the saturation analyzer's prefill lower-bound
	// window (prefill_bound.go). A lower bound.
	SourceBackPressureBound Source = "back-pressure-bound"
)

// Refusal is why a figure was not learned.
type Refusal string

const (
	// NoPrefillCapacity is V_P with no source that cannot over-state it.
	NoPrefillCapacity Refusal = "NoPrefillCapacity"
	// SGLangRefused is V_P on SGLang, which publishes no per-stage computed-
	// token series (sgl-project/sglang issue #14303).
	SGLangRefused Refusal = "SGLangNoComputedTokens"
	// AggregatedRefused is a on an aggregated pod: its service time includes
	// decode, so the service time is not the prefill time.
	AggregatedRefused Refusal = "AggregatedServiceTimeIncludesDecode"
	// TooFewLowLoadMinutes is a with fewer than MinFixedSamples low-load
	// minutes.
	TooFewLowLoadMinutes Refusal = "TooFewLowLoadMinutes"
	// NoPromptSpread is a whose low-load minutes do not span MinPromptSpread
	// in prompt length, so the fixed cost and the per-token cost cannot be
	// told apart.
	NoPromptSpread Refusal = "NoPromptSpread"
	// NonPositiveSlope is a low-load fit whose per-token cost is not above
	// zero.
	NonPositiveSlope Refusal = "NonPositiveSlope"
	// NonPositiveIntercept is a low-load fit whose fixed cost is not above
	// zero.
	NonPositiveIntercept Refusal = "NonPositiveIntercept"
	// OnePod is f on a variant with fewer than two prefill pods: there is no
	// share to compare.
	OnePod Refusal = "OnePod"
	// NoComputedTokens is f with no computed-token rate to share out.
	NoComputedTokens Refusal = "NoComputedTokens"
	// TooFewImbalanceMinutes is f with fewer than MinImbalanceMinutes minutes
	// carrying MinRequestsPerPodMinute requests per pod.
	TooFewImbalanceMinutes Refusal = "TooFewImbalanceMinutes"
)

// Row refusals, counted per cycle in Estimate.Refused.
const (
	rowNotReady = "not-ready"
	rowBridge   = "bridge"
	rowSettling = "settling"
)

// Bound is the saturation analyzer's back-pressure bound for the variant:
// the max computed-token rate of its prefill lower-bound window, and how many
// spaced readings the window holds. Zero when the bound is off or empty.
type Bound struct {
	Tokens  float64
	Samples int
}

// Settle is the settle tracker a replica's settle is read from, and the
// tracker's name for the model (the engine's namespaced key).
type Settle struct {
	View  settle.View
	Model string
}

// Cycle is one cycle of one variant's prefill rows.
type Cycle struct {
	// Key names the variant's state: its rows, from cycle to cycle, are
	// one key's. It must change with anything V_P is a property of, the
	// engine arguments included: V_P is a max over Retention, so a key that
	// outlives a change keeps the old engine's peak (the saturation analyzer
	// names it in prefillObserveKey).
	Key string
	// Engine is the inference engine the variant runs; empty when unknown.
	Engine inferenceengine.Engine
	// Disaggregated says the pods are the prefill role of a P/D split, where
	// the service time is the prefill time. False on an aggregated pod.
	Disaggregated bool
	// Rows is the variant's rows this cycle.
	Rows []domain.ReplicaMetrics
	// QueueMin is, per pod, the least waiting count over the minute
	// (min_over_time of num_requests_waiting), where one was read. No
	// collector query reads it today, so a busy minute is never recognised
	// until one does.
	QueueMin map[string]int
	// DownstreamSaturated says decode is back-pressuring prefill this cycle:
	// the test computeK2 applies (k2ReasonObsDownstream).
	DownstreamSaturated bool
	// Bound is the saturation analyzer's back-pressure bound.
	Bound Bound
	// Settle is the settle tracker; nil admits every Ready replica as
	// settled (a caller with no tracker, a test).
	Settle *Settle
	// Now is the cycle's time.
	Now time.Time
}

// Estimate is what one key has learned, after a cycle.
type Estimate struct {
	// VP is the prefill capacity in computed tokens/s, zero when refused;
	// VPSource where it came from and VPRefused why there is none.
	// VPLowerBound says the source is a lower bound (sources 2 and 3).
	// VPSamples is how many minutes (or bound readings) it rests on.
	VP           float64
	VPSource     Source
	VPLowerBound bool
	VPRefused    Refusal
	VPSamples    int
	// SlopeBound is 1/slope of the low-load fit of service time against
	// prompt length: an upper bound on V_P, a validation signal only. Zero
	// with no fit. VPAboveSlopeBound flags a V_P above it.
	SlopeBound        float64
	VPAboveSlopeBound bool

	// A is the fixed latency in seconds: the low-load fit's intercept plus
	// the least overhead (AvgTTFT - AvgServiceTime) seen at low load. Zero
	// when refused. AOverheadKnown is false when no low-load minute carried a
	// TTFT, and then A is the intercept alone.
	A              float64
	AIntercept     float64
	AOverhead      float64
	AOverheadKnown bool
	ARefused       Refusal
	ASamples       int
	// APromptMin and APromptMax are the effective prompt lengths the fit
	// spans.
	APromptMin float64
	APromptMax float64

	// F is the de-biased imbalance, at least 1; zero when refused.
	F         float64
	FRefused  Refusal
	FSamples  int
	FPodsLast int

	// Admitted is how many rows were admitted this cycle, and Refused how
	// many were not, by reason.
	Admitted int
	Refused  map[string]int
}

// sample is one minute of one replica.
type sample struct {
	at time.Time
	// rate is the computed-token rate, valid when rateOK.
	rate   float64
	rateOK bool
	// queued: the queue was above zero on some sample of the minute.
	queued bool
	// busy: above zero on every sample, with decode not back-pressuring.
	busy bool
	// lowLoad: admitted to the fit of a, at ieff and svc, with the
	// overhead when overheadOK.
	lowLoad    bool
	ieff       float64
	svc        float64
	overhead   float64
	overheadOK bool
}

// promptPoint is one row's effective prompt length, for the steady gate.
type promptPoint struct {
	at   time.Time
	ieff float64
}

// podState is what one key keeps of one replica.
type podState struct {
	samples  []sample
	prompts  []promptPoint
	lastSeen time.Time
}

// fSample is one minute's de-biased imbalance.
type fSample struct {
	at time.Time
	f  float64
}

// keyState is what one key keeps.
type keyState struct {
	pods     map[string]*podState
	f        []fSample
	lastSeen time.Time
}

// Observer is the learned state of every key. Safe for concurrent use.
type Observer struct {
	mu   sync.Mutex
	keys map[string]*keyState
}

// NewObserver returns an Observer with nothing learned.
func NewObserver() *Observer {
	return &Observer{keys: map[string]*keyState{}}
}

// Keys is how many keys the observer holds.
func (o *Observer) Keys() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.keys)
}

// Forget drops every key that begins with prefix, "" dropping them all, and
// returns how many it dropped. The caller ends a key's name with a separator
// so that one variant's prefix does not match another's ("v|" against
// "v2|").
func (o *Observer) Forget(prefix string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for k := range o.keys {
		if strings.HasPrefix(k, prefix) {
			delete(o.keys, k)
			n++
		}
	}
	return n
}

// Observe admits one cycle of a variant's rows and returns what the key has
// learned. Keys and replicas with no row for Retention are forgotten here.
func (o *Observer) Observe(c Cycle) Estimate {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sweep(c.Now)
	ks, ok := o.keys[c.Key]
	if !ok {
		ks = &keyState{pods: map[string]*podState{}}
		o.keys[c.Key] = ks
	}
	ks.lastSeen = c.Now

	est := Estimate{Refused: map[string]int{}}
	sglang := c.Engine == inferenceengine.EngineSGLang
	// The f sample of this cycle: every serving replica's computed-token
	// rate and request rate, and whether each was admitted.
	var shares []share
	allAdmitted, serving := true, 0
	for _, rm := range c.Rows {
		if !rm.Ready {
			est.Refused[rowNotReady]++
			continue
		}
		if rm.FromWarmPool {
			// A bridge serves the variant's load on another engine.
			est.Refused[rowBridge]++
			continue
		}
		serving++
		ps := ks.pods[rm.PodName]
		if ps == nil {
			ps = &podState{}
			ks.pods[rm.PodName] = ps
		}
		ps.lastSeen = c.Now
		ieff, ieffOK := effectivePrompt(rm, sglang)
		steady := ieffOK && ps.steady(ieff, c.Now)
		if ieffOK {
			ps.notePrompt(ieff, c.Now)
		}
		if !c.Settle.settled(rm, c.Now) {
			est.Refused[rowSettling]++
			allAdmitted = false
			continue
		}
		est.Admitted++
		s := sample{at: c.Now}
		s.rate, s.rateOK = computedRate(rm, sglang)
		s.queued = rm.QueueLength > 0
		if qmin, ok := c.QueueMin[rm.PodName]; ok {
			s.busy = qmin > 0 && s.queued && !c.DownstreamSaturated
		}
		svc := rm.AvgServiceTime
		rr, rrOK := requestRate(rm)
		if c.Disaggregated && steady && rm.QueueLength == 0 && rrOK && finitePositive(svc) &&
			rr*svc <= LowLoadOccupancy {
			s.lowLoad, s.ieff, s.svc = true, ieff, svc
			if finitePositive(rm.AvgTTFT) && rm.AvgTTFT >= svc {
				s.overhead, s.overheadOK = rm.AvgTTFT-svc, true
			}
		}
		ps.add(s)
		shares = append(shares, share{rate: s.rate, rateOK: s.rateOK, requests: rr, requestsOK: rrOK})
	}
	ks.prune(c.Now)

	ks.noteImbalance(shares, allAdmitted, c.Now)
	est.capacity(ks, c, sglang)
	est.fixed(ks, c.Disaggregated)
	est.imbalance(ks, shares, serving)
	if est.VP > 0 && est.SlopeBound > 0 && est.VP > est.SlopeBound {
		est.VPAboveSlopeBound = true
	}
	return est
}

// sweep forgets the keys with no cycle for Retention.
func (o *Observer) sweep(now time.Time) {
	for k, ks := range o.keys {
		if now.Sub(ks.lastSeen) > Retention {
			delete(o.keys, k)
		}
	}
}

// settled reports whether the replica of rm has been settled for SteadyFor
// at now. A nil Settle admits every replica; a replica the tracker does not
// know is not settled.
func (s *Settle) settled(rm domain.ReplicaMetrics, now time.Time) bool {
	if s == nil || s.View == nil {
		return true
	}
	key := settlerow.Key(rm)
	r, ok := s.View.Replica(s.Model, key)
	if !ok {
		return false
	}
	start := r.Since
	if t, ok := r.ReturnedAfter(idleRows); ok && t.After(start) {
		start = t
	}
	if back := settlerow.ReadyAgain(s.View.Trace(s.Model, key)); back.After(start) {
		start = back
	}
	return !start.IsZero() && now.Sub(start) >= SteadyFor
}

// computedRate is the row's computed-token rate, and whether it is a
// reading: known, finite and not negative, and never on SGLang.
func computedRate(rm domain.ReplicaMetrics, sglang bool) (float64, bool) {
	r := rm.PrefillComputedTokenRate
	if sglang || !rm.PrefillComputedTokenRateKnown || !(r >= 0) || math.IsInf(r, 1) {
		return 0, false
	}
	return r, true
}

// requestRate is the row's request rate, and whether it is a reading above
// zero.
func requestRate(rm domain.ReplicaMetrics) (float64, bool) {
	if !rm.RequestRateKnown || !finitePositive(rm.RequestRate) {
		return 0, false
	}
	return rm.RequestRate, true
}

// effectivePrompt is the row's effective prompt length over the minute the
// service time is taken over: the computed tokens over the requests, both
// one-minute rates over the same completed requests, so net of the prefix
// cache. Where no computed-token rate is read (SGLang), the one-minute
// prompt length net of the hit rate.
func effectivePrompt(rm domain.ReplicaMetrics, sglang bool) (float64, bool) {
	rr, rrOK := requestRate(rm)
	if rate, ok := computedRate(rm, sglang); ok && rrOK && rate > 0 {
		ieff := rate / rr
		return ieff, finitePositive(ieff)
	}
	hit := rm.PrefixCacheHitRate
	if !(hit >= 0 && hit < 1) {
		hit = 0
	}
	ieff := rm.AvgInputTokensRecent * (1 - hit)
	return ieff, finitePositive(ieff)
}

// finitePositive reports whether x is finite and above zero; NaN is not.
func finitePositive(x float64) bool {
	return x > 0 && !math.IsInf(x, 1)
}

// steady reports whether ieff is within MaxPromptDrift of every prompt length
// the replica read in the SteadyFor before now, and it read at least one: a
// row with no history to compare against is not shown steady, so a replica's
// first row, or its first after a gap of SteadyFor, is not fitted.
func (ps *podState) steady(ieff float64, now time.Time) bool {
	cutoff := now.Add(-SteadyFor)
	seen := false
	for _, p := range ps.prompts {
		if !p.at.Before(now) || p.at.Before(cutoff) {
			continue
		}
		if !(math.Abs(p.ieff-ieff) <= MaxPromptDrift*p.ieff) {
			return false
		}
		seen = true
	}
	return seen
}

// notePrompt records the row's prompt length and forgets what is older than
// SteadyFor.
func (ps *podState) notePrompt(ieff float64, now time.Time) {
	cutoff := now.Add(-SteadyFor)
	ps.prompts = slices.DeleteFunc(ps.prompts, func(p promptPoint) bool { return p.at.Before(cutoff) })
	ps.prompts = append(ps.prompts, promptPoint{at: now, ieff: ieff})
}

// add folds s into the replica's samples: it rewrites the last sample when
// that one is under SampleSpacing old, and is added after it otherwise.
func (ps *podState) add(s sample) {
	if n := len(ps.samples); n > 0 && s.at.Sub(ps.samples[n-1].at) < SampleSpacing {
		s.at = ps.samples[n-1].at
		ps.samples[n-1] = s
		return
	}
	ps.samples = append(ps.samples, s)
	if len(ps.samples) > maxSamples {
		ps.samples = slices.Delete(ps.samples, 0, len(ps.samples)-maxSamples)
	}
}

// prune forgets the samples older than Retention, and the replicas with no
// row for as long.
func (ks *keyState) prune(now time.Time) {
	cutoff := now.Add(-Retention)
	for pod, ps := range ks.pods {
		if ps.lastSeen.Before(cutoff) {
			delete(ks.pods, pod)
			continue
		}
		ps.samples = slices.DeleteFunc(ps.samples, func(s sample) bool { return s.at.Before(cutoff) })
	}
	ks.f = slices.DeleteFunc(ks.f, func(s fSample) bool { return s.at.Before(cutoff) })
}

// podNames is the key's replicas, sorted, so every estimator reads them in
// one order.
func (ks *keyState) podNames() []string {
	names := make([]string, 0, len(ks.pods))
	for n := range ks.pods {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

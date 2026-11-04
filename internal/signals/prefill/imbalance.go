package prefill

import (
	"math"
	"time"
)

// The imbalance f (slo-capacity-planner-ttft.md, "The engine queue").
//
// The router sends each request to one pod, so the worst pod sees f times
// the mean share of prefill work. f is measured as the busiest pod's share of
// the computed-token rate over the mean share, over minutes with at least
// MinRequestsPerPodMinute requests per pod on average, minus what Poisson
// arrivals alone would show: the expected max of N_p Poisson counts over
// their mean, less one. It is never below 1.
//
// A minute counts only when every serving replica of the variant was
// admitted (settled) and read a computed-token rate and a request rate:
// shares over a subset of the pods are not shares of the variant's work.
// f is the median of the minutes, over at least MinImbalanceMinutes.

// MinRequestsPerPodMinute is the fewest requests per pod per minute, on
// average, a minute must carry for its shares to be read.
const MinRequestsPerPodMinute = 100

// MinImbalanceMinutes is the fewest minutes f is taken from.
const MinImbalanceMinutes = 5

// share is one replica's work this cycle.
type share struct {
	rate       float64
	rateOK     bool
	requests   float64
	requestsOK bool
}

// minuteImbalance is the de-biased imbalance of one cycle's shares, and
// whether the cycle gives one.
func minuteImbalance(shares []share) (float64, bool) {
	n := len(shares)
	if n < 2 {
		return 0, false
	}
	var total, busiest, requests float64
	for _, s := range shares {
		if !s.rateOK || !s.requestsOK {
			return 0, false
		}
		total += s.rate
		requests += s.requests
		busiest = math.Max(busiest, s.rate)
	}
	perPod := requests * 60 / float64(n)
	if !(total > 0) || !(perPod >= MinRequestsPerPodMinute) || !isFinite(perPod) {
		return 0, false
	}
	raw := float64(n) * busiest / total
	f := math.Max(1, raw-poissonMaxExcess(n, perPod))
	return f, isFinite(f)
}

// noteImbalance folds the cycle's imbalance into the key's minutes, when
// every serving replica was admitted.
func (ks *keyState) noteImbalance(shares []share, allAdmitted bool, now time.Time) {
	if !allAdmitted {
		return
	}
	f, ok := minuteImbalance(shares)
	if !ok {
		return
	}
	if n := len(ks.f); n > 0 && now.Sub(ks.f[n-1].at) < SampleSpacing {
		ks.f[n-1].f = f
		return
	}
	ks.f = append(ks.f, fSample{at: now, f: f})
	if len(ks.f) > maxSamples {
		ks.f = ks.f[len(ks.f)-maxSamples:]
	}
}

// imbalance sets f on est from the key's minutes, and names the refusal by
// the cycle's own shares when there are too few: serving is how many of
// the variant's replicas served this cycle, admitted or not.
func (est *Estimate) imbalance(ks *keyState, shares []share, serving int) {
	est.FPodsLast = serving
	est.FSamples = len(ks.f)
	if len(ks.f) >= MinImbalanceMinutes {
		fs := make([]float64, len(ks.f))
		for i, s := range ks.f {
			fs[i] = s.f
		}
		est.F = median(fs)
		return
	}
	switch {
	case serving < 2:
		est.FRefused = OnePod
	case !anyRate(shares):
		est.FRefused = NoComputedTokens
	default:
		est.FRefused = TooFewImbalanceMinutes
	}
}

// anyRate reports whether any share read a computed-token rate.
func anyRate(shares []share) bool {
	for _, s := range shares {
		if s.rateOK {
			return true
		}
	}
	return false
}

// poissonMaxExcess is E[max of n Poisson(m) counts]/m - 1: how far above the
// mean the busiest of n equally loaded pods reads by chance alone. Summed
// over k from 12 standard deviations either side of m, outside which the
// terms are below float64's resolution.
func poissonMaxExcess(n int, m float64) float64 {
	if n < 2 || !(m > 0) || !isFinite(m) {
		return 0
	}
	sd := math.Sqrt(m)
	lo := math.Max(0, math.Floor(m-12*sd))
	hi := math.Ceil(m + 12*sd)
	logM := math.Log(m)
	// E[max] = sum over k >= 0 of P(max > k) = sum of 1 - F(k)^n. Below lo,
	// F(k) is 0 to float64 resolution and each term is 1.
	expMax := lo
	cdf := 0.0
	for k := lo; k <= hi; k++ {
		lg, _ := math.Lgamma(k + 1)
		cdf += math.Exp(-m + k*logM - lg)
		if cdf > 1 {
			cdf = 1
		}
		expMax += 1 - math.Pow(cdf, float64(n))
	}
	return expMax/m - 1
}

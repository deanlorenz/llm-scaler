package whatif

import (
	"fmt"
	"math"
)

// Admission is decode admission on one replica: a birth-death chain on the
// requests the replica holds, with the line read at each state's own fill
// (docs/proposals/slo-capacity-planner-ttft.md, "Decode admission").
//
// A replica holding n requests completes them at
//
//	mu_n = m / (OL * ITL(m*KVreq/C)),   m = min(n, c)
//
// with c the replica's slots. Above c the chain is geometric, so a request
// that finds every slot taken is admitted after exactly Exp(mu_c - lambda_rep),
// and the wait probability is the chain's mass at n >= c (arrivals see the
// time average: Poisson arrivals). Residence is treated as exponential.
type Admission struct {
	// WaitProbability is the share of requests that wait for a slot.
	WaitProbability float64
	// Decay is mu_c - lambda_rep, per second: the wait of a request that
	// finds every slot taken is exponential at this rate.
	Decay float64
	// Slots is c: the replica cap, or C/KVreq at a full cache where the KV
	// binds first (flagged KVBoundSlots).
	Slots int
	// Fill is the mean KV fill the chain spends its time at.
	Fill  float64
	Flags []Flag
}

func (Admission) result() {}

// MeanWait is the mean admission wait over all requests, in seconds.
func (a Admission) MeanWait() float64 {
	if a.WaitProbability == 0 {
		return 0
	}
	return a.WaitProbability / a.Decay
}

// WaitExceeds is P(W > t): the share of requests that wait longer than t
// seconds.
func (a Admission) WaitExceeds(t float64) float64 {
	if t < 0 {
		return 1
	}
	return a.WaitProbability * math.Exp(-a.Decay*t)
}

// WaitQuantile is the q-th percentile of the admission wait, in seconds: 0
// when fewer than 1-q of requests wait at all. NaN for a q outside (0, 1).
func (a Admission) WaitQuantile(q float64) float64 {
	if !(q > 0 && q < 1) {
		return math.NaN()
	}
	if a.WaitProbability <= 1-q {
		return 0
	}
	return math.Log(a.WaitProbability/(1-q)) / a.Decay
}

// DecodeAdmission prices decode admission at rate requests per second over the
// fleet, shared evenly across the replicas. Saturated when the per-replica
// rate reaches mu_c, where the chain has no stationary state.
//
// The slots are the replica cap; where the KV cache fills before the cap,
// they are C/KVreq at a full cache instead, an approximation the answer flags
// (KVBoundSlots): KV-bound admission is not a fixed number of slots.
func DecodeAdmission(d Decode, rate float64) Result {
	if u := d.check(); u != nil {
		return *u
	}
	if !Finite(rate) || rate < 0 {
		return refuse(InvalidInput, fmt.Sprintf("rate %g", rate))
	}
	slots := d.ReplicaCap
	var flags []Flag
	// At least 1: check refuses a capacity under one request's footprint.
	if kv := math.Floor(d.CapacityTokens / d.KVPerRequest); kv < float64(slots) {
		slots, flags = int64(kv), []Flag{KVBoundSlots}
	}
	c := int(slots)
	mu := func(m int) float64 {
		k := float64(m) * d.KVPerRequest / d.CapacityTokens
		return float64(m) / (d.OutputTokens * d.Line.ITLAt(k))
	}
	lam := rate / float64(d.Replicas)
	ch, ok := birthDeath(lam, mu, c)
	if !ok {
		return refuse(Saturated, fmt.Sprintf("%g r/s per replica at or above the full-slot completion rate %g r/s", lam, mu(c)))
	}
	ch.Slots, ch.Flags = c, flags
	ch.Fill *= d.KVPerRequest / d.CapacityTokens
	return ch
}

// birthDeath solves the chain with arrival rate lam, completion rate mu(m)
// with m requests in service for m in 1..c, and mu(c) above c. ok is false when
// lam >= mu(c) or a rate is unusable. Fill is returned in sequences (the mean
// of min(n, c)); the caller scales it to KV.
//
// The stationary weights are products of lam/mu(m), which overflow long before
// c reaches a large engine's batch cap, so they are summed in logs.
func birthDeath(lam float64, mu func(m int) float64, c int) (Admission, bool) {
	if c < 1 || !Finite(lam) || lam < 0 {
		return Admission{}, false
	}
	muC := mu(c)
	if !Finite(muC) || !(muC > lam) {
		return Admission{}, false
	}
	if lam == 0 {
		return Admission{Decay: muC}, true
	}
	logW := make([]float64, c+1)
	for m := 1; m <= c; m++ {
		mm := mu(m)
		if !Finite(mm) || !(mm > 0) {
			return Admission{}, false
		}
		logW[m] = logW[m-1] + math.Log(lam/mm)
	}
	rho := lam / muC
	// The tail n >= c is geometric: w_c * rho^j, j >= 0, summing to
	// w_c / (1 - rho); its mean of min(n, c) is c.
	logTail := logW[c] - math.Log1p(-rho)
	hi := logTail
	for _, w := range logW[:c] {
		hi = max(hi, w)
	}
	var z, seqs float64
	for m, w := range logW[:c] {
		e := math.Exp(w - hi)
		z += e
		seqs += float64(m) * e
	}
	tail := math.Exp(logTail - hi)
	z += tail
	seqs += float64(c) * tail
	return Admission{WaitProbability: tail / z, Decay: muC - lam, Fill: seqs / z}, true
}

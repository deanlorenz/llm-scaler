package saturation

import (
	"time"
)

// Which saturated readings may become a measured service rate (mu), and how
// long a measured mu outranks a derived one that disagrees with it.
//
// A measured mu is kept and preferred over the derived figure (useDerived),
// and under EPP flow control vLLM's own queue rarely builds, so P1-obs -- the
// only writer -- rarely fires again: one bad sample stands for the rest of a
// run. Measured on E1 (2026-10-23, two namespaces, Qwen3-32B-FP8, one H200 per
// replica): a replica that had just joined read the router's queue (84) with
// no completion yet, and its 49 generated tokens a second over the 496-token
// output -- 0.0995 req/s -- became the bucket's mu. Against a derived 3.1-5.7
// and ~6.2 req/s actually served, the floor priced 2 req/s as 12-20 replicas
// (up to ~190), and the share never swung back. The other namespace's window
// took 1.75, 0.11 and 3.24 from replicas filling the same way.
//
// So a reading is a sample only from a replica that:
//
//   - has served a whole rate window since it was first seen Ready
//     (JoinedReplicaWindow). Its one-minute gauges and rates still hold the
//     minute before it was routed to, and its batch is still filling. This
//     gates the k2 reading too (k2Unsettled), and so the mu written beside it.
//     A replica already Ready on the analyzer's first cycle for the model has
//     an age the analyzer cannot know and is taken as established: refusing
//     it would hold every reading back for a window after each restart, and
//     the next two gates still apply to it. (ReplicaMetrics carries no Ready
//     time on this branch; a collector-reported one would close that gap.)
//   - completed requests over the window (RequestRate > 0). A replica that
//     has completed nothing has every sequence still growing: its token rate
//     is the rate of a batch being admitted, not of one being served.
//   - is not implausibly far under the derived figure for the same line:
//     under MinMeasuredOfDerived of it. A saturated replica at its limit
//     completes about what its ITL line prices; one completing a fiftieth of
//     it is still filling, or stalled. A filling replica reads that way for
//     a minute or so, though; one that reads it on every saturated cycle for
//     MeasuredLowConfirmFor is a real low rate -- the line is the wrong
//     figure -- and its readings are recorded from then on
//     (noteSaturatedReading). Without that, a real low measurement could
//     never be refreshed while the line stood, went stale by construction,
//     and gave way to the too-high derived figure however many saturated
//     readings re-confirmed it.
//
// Each refusal only withholds or delays a sample; none lowers or raises a
// figure. A too-low mu over-orders (money), a too-high one under-orders (SLO).
//
// And a measured mu does not outrank, for ever, a derived figure it
// disagrees with by more than MeasuredDerivedMaxRatio (measuredYieldsToDerived).
// It is fresh while its bucket keeps reading saturated at a rate that agrees
// with it -- accepted as a sample or refused -- within MeasuredMuFreshFor, and
// a fresh measurement wins either way. A stale one that disagrees by more than
// the ratio gives way to the LOWER of the two figures, which over-orders: a
// measurement far under derived keeps pricing, one far over it yields to
// derived. In review the two gates together always picked the higher figure
// under continuous saturation, a replica queued every minute at 1.2 req/s
// priced from a derived 3.6. Within the ratio the measurement keeps winning
// however old it is -- run R's own 1.27 against a derived 0.67 is the case the
// measured-first rule exists for, and it is inside 2x.
//
// The yield is latched: once a stale measurement has given way it stays given
// way until the two figures come back within MeasuredDerivedReturnRatio, or
// the bucket reads saturated again. Derived mu is recomputed every cycle from
// the fleet's output length and carries its noise; with a hard edge at 2x, in
// review, a measurement near it flipped the priced mu between the two figures
// 11 times in 20 cycles under 4 % noise on the output length.
const (
	// JoinedReplicaWindow is how long a replica must have been seen Ready
	// before its saturated reading counts: the collector's rate window
	// (ThroughputSampleSpacing, registration.RequestRateWindow).
	JoinedReplicaWindow = ThroughputSampleSpacing
	// MinMeasuredOfDerived is the smallest fraction of the derived mu a
	// saturated reading may be and still be recorded at once.
	MinMeasuredOfDerived = 0.5
	// MeasuredLowConfirmFor is how long one replica must read saturated under
	// MinMeasuredOfDerived of the derived mu, on every saturated cycle with
	// no gap over ThroughputSampleSpacing, before those readings are
	// recorded: two rate windows, longer than a joining replica fills for.
	MeasuredLowConfirmFor = 2 * ThroughputSampleSpacing
	// MeasuredDerivedMaxRatio is how far a stale measured mu may sit above
	// the derived one and still outrank it.
	MeasuredDerivedMaxRatio = 2.0
	// MeasuredDerivedReturnRatio is how close a stale measured mu that has
	// given way to the derived one must come back before it outranks it
	// again: the hysteresis band under MeasuredDerivedMaxRatio.
	MeasuredDerivedReturnRatio = 1.6
	// MeasuredMuFreshFor is how long after its bucket last read saturated in
	// agreement with it a measured mu outranks a derived one it disagrees
	// with by more than MeasuredDerivedMaxRatio.
	MeasuredMuFreshFor = 5 * time.Minute
)

// k2UnsettledJoined is the settle test's reason for a saturated reading from a
// replica that joined under JoinedReplicaWindow ago.
const k2UnsettledJoined = "the replica joined under a rate window ago: its batch is still filling and its one-minute readings still hold the minute before it was routed to; not recorded"

// noteReady records whether pod is Ready this cycle and reports whether it
// joined the fleet under JoinedReplicaWindow ago. Callers hold a.mu.
func (m *settleMemo) noteReady(pod string, ready bool, now time.Time) bool {
	if m.readySince == nil {
		m.readySince = make(map[string]time.Time)
	}
	if !ready {
		delete(m.readySince, pod)
		return false
	}
	since, seen := m.readySince[pod]
	if !seen {
		since = now
		if !m.pastFirstCycle {
			since = time.Time{} // already serving when the memo began
		}
		m.readySince[pod] = since
	}
	return !since.IsZero() && now.Sub(since) < JoinedReplicaWindow
}

// The reasons throughputRefusal gives.
const (
	refusedNoCompletion = "no request completed over the rate window: every sequence is still growing, so the token rate is a batch being admitted, not served"
	refusedUnderDerived = "under half the mu derived from the variant's ITL line: a replica still filling or stalled, not one at its limit"
)

// throughputRefusal says why a saturated replica's completion rate mu is not
// a sample of what one replica sustains, or "" when it may be recorded. The
// replica has already passed the k2 admission, the joined test included.
func throughputRefusal(requestRate, mu float64, derived derivedMu) string {
	if !(requestRate > 0) {
		return refusedNoCompletion
	}
	if derived.ok && mu < MinMeasuredOfDerived*derived.rate {
		return refusedUnderDerived
	}
	return ""
}

// muChoice is one throughput key's memory for choosing between its measured
// mu and the derived one (SaturationAnalyzer.throughputMuChoice).
type muChoice struct {
	// saturatedAt is when the bucket last read saturated at a rate within
	// MeasuredDerivedMaxRatio of its window's median, the reading accepted
	// or refused.
	saturatedAt time.Time
	// yielded is set while a stale measurement has given way to the derived
	// figure, and latches it (measuredYieldsToDerived).
	yielded bool
	// low is, per replica, its current run of saturated readings refused as
	// under MinMeasuredOfDerived of the derived figure.
	low map[string]lowRun
}

// lowRun is one replica's run of saturated readings refused as low.
type lowRun struct {
	since, last time.Time
}

// noteSaturatedReading records one replica's saturated reading mu of the
// throughput key, before it is recorded or refused, and reports whether a
// reading refused as under the derived figure (refusedLow) has repeated for
// MeasuredLowConfirmFor and is to be recorded after all.
//
// A reading that agrees with the window's median within
// MeasuredDerivedMaxRatio keeps the measurement fresh, accepted or not: the
// bucket is still reading what it measured. One that disagrees does not, and
// a filling replica's reading -- a fraction of the rate on record -- is one.
func (a *SaturationAnalyzer) noteSaturatedReading(key, pod string, mu float64, refusedLow bool, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.throughputMuChoice[key]
	if !ok {
		c = &muChoice{}
		a.throughputMuChoice[key] = c
	}
	if ra, ok := a.saturatedThroughput[key]; ok && ra.Len() > 0 {
		if m := ra.Median(); m > 0 && mu <= m*MeasuredDerivedMaxRatio && mu >= m/MeasuredDerivedMaxRatio {
			c.saturatedAt = now
			c.yielded = false
		}
	}
	for p, r := range c.low {
		if now.Sub(r.last) > ThroughputSampleSpacing {
			delete(c.low, p)
		}
	}
	if !refusedLow {
		delete(c.low, pod)
		return false
	}
	if c.low == nil {
		c.low = make(map[string]lowRun)
	}
	r, had := c.low[pod]
	if !had {
		r.since = now
	}
	r.last = now
	c.low[pod] = r
	return now.Sub(r.since) >= MeasuredLowConfirmFor
}

// measuredYieldsToDerived reports whether the measured mu of the throughput
// key, which useDerived would keep, gives way to the derived one this cycle:
// the measurement is stale (its bucket has not read saturated in agreement
// with it, nor taken a sample, for MeasuredMuFreshFor), and it sits more than
// MeasuredDerivedMaxRatio above the derived figure -- or, once it has given
// way, still MeasuredDerivedReturnRatio above it. A measurement under the
// derived figure never gives way: the lower of the two prices.
func (a *SaturationAnalyzer) measuredYieldsToDerived(key string, reading throughputReading, derived derivedMu, now time.Time) bool {
	if !derived.ok || !(derived.rate > 0) || !(reading.rate > 0) {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.throughputMuChoice[key]
	// The last reading that agreed with the measurement; the newest sample
	// only where none is on record (a window restored, or its first
	// sample). A sample is not freshness by itself: a confirmed low rate
	// is written under a median it contradicts.
	fresh := reading.sampledAt
	if c != nil && !c.saturatedAt.IsZero() {
		fresh = c.saturatedAt
	}
	if !fresh.IsZero() && now.Sub(fresh) < MeasuredMuFreshFor {
		if c != nil {
			c.yielded = false
		}
		return false
	}
	ratio := reading.rate / derived.rate
	yielded := ratio > MeasuredDerivedMaxRatio || (c != nil && c.yielded && ratio > MeasuredDerivedReturnRatio)
	if c == nil {
		if !yielded {
			return false
		}
		c = &muChoice{}
		a.throughputMuChoice[key] = c
	}
	c.yielded = yielded
	return yielded
}

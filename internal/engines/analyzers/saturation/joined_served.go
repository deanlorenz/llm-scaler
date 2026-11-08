package saturation

import (
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// A replica's joined clock starts when it is first routed to (#392).
//
// The joined test (k2Unsettled) ages a replica from when its Pod became Ready,
// or from when the controller first saw it. A replica Ready for longer than a
// rate window, but routed to only partway through a minute -- an idle fleet
// hit by a burst, or a release of the scheduler's flow-control queue --
// passed it, and its first saturated rate[1m] covers only the part of the
// minute it served. That reading went into mu's window as a service rate:
// measured on the property case first-sample/mu/joiner-rate-timebase
// through Analyze, a replica routed to 10 s before a cycle recorded 0.0377
// req/s against a truth of 0.2055 (seed 1), and three minutes on its mu still
// read 0.1918, 7 % low. On 100 of 100 seeds the first mu on record was
// outside the band of the figure three minutes later. A low mu over-orders:
// money, not SLO.
//
// So the clock also starts at the first cycle the replica served anything
// (a request completed or a token generated, over the rate window), when a
// row of the same Ready sighting showed it known to have served nothing
// before. Until a rate window has passed from that cycle, a saturated reading
// is the joined test's, refused as for a replica that has just become Ready
// (P1-obs-unsettled): its k2 is not written, and mu, written only beside a k2
// write, is not either. Refusing both keeps them together, as the joined test
// does: refusing mu alone left k2's settle rate test with no mu on record,
// and the readings it admitted then wrote ramp readings as mu (f32a32ed7).
//
// The routing began at most one cycle before the first served row, so a
// window from that row holds no idle second. A replica already serving when
// the analyzer first saw it is unchanged: nothing says when its load began,
// and the controller-restart wait (SwitchK2WaitsAfterRestart) covers that
// first sight. A row whose rates were not read is no evidence either way. The
// sighting is per Ready time: a new one (an engine restart) starts again, and
// once the replica has served under it, a later idle run and return does not
// restart the clock.
//
// The cost: a replica routed to after standing idle records its first k2 and
// mu a rate window after its first served cycle (4 cycles at 15 s), and k2's
// first reading of a fresh window waits one more (k2_confirm.go).

// k2UnsettledRoutedLate is the settle test's reason for a saturated reading
// from a replica Ready for longer than a rate window but first routed to
// under one ago (SwitchJoinedFromFirstServed).
const k2UnsettledRoutedLate = "the replica was first routed to under a rate window ago, though Ready for longer: its one-minute readings still hold the idle part of the minute before it was routed to; not recorded"

// SwitchJoinedFromFirstServed is the replay switch for the clock (#392): on
// from the constructor; a replay turns it off (SetSwitchesForReplay), and the
// joined test ages a replica from its Ready time and first sight alone, as
// before.
const SwitchJoinedFromFirstServed = "joined-clock-starts-at-first-served"

// servedSighting is what the joined test knows of when one replica, under one
// Pod Ready time, was first routed to.
type servedSighting struct {
	// readyAt is the Pod Ready time the sighting is for (zero when unknown).
	readyAt time.Time
	// idleSeen is set once a row showed the replica known to have served
	// nothing, before any row showed it serving.
	idleSeen bool
	// decided is set at the first row that showed it serving; from is that
	// row's cycle when idleSeen was set by then, zero otherwise.
	decided bool
	from    time.Time
}

// rowServed reports whether rm served anything over the rate window, and
// whether it is known to have served nothing: no request completed and no
// token generated, with the request rate read.
func rowServed(rm domain.ReplicaMetrics) (served, idle bool) {
	served = rm.RequestRate > 0 || rm.GenerationTokenRate > 0
	return served, !served && rm.RequestRateKnown
}

// noteServed records this cycle's row of pod in the memo's served sightings
// and returns when the replica was first routed to after a row that showed
// it idle, zero when no such row was seen. A row that is not Ready ends the
// sighting. Callers hold a.mu.
func (m *settleMemo) noteServed(pod string, ready bool, readyAt time.Time, served, idle bool, now time.Time) time.Time {
	if m.served == nil {
		m.served = make(map[string]servedSighting)
	}
	if !ready {
		delete(m.served, pod)
		return time.Time{}
	}
	s, ok := m.served[pod]
	if !ok || !s.readyAt.Equal(readyAt) {
		s = servedSighting{readyAt: readyAt}
	}
	switch {
	case s.decided:
	case served:
		s.decided = true
		if s.idleSeen {
			s.from = now
		}
	case idle:
		s.idleSeen = true
	}
	m.served[pod] = s
	return s.from
}

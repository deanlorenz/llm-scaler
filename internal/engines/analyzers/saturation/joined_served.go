package saturation

import (
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle/settlerow"
)

// A replica's joined clock also starts when its load first returns (#392).
//
// The joined test (k2Unsettled) ages a replica from when its Pod became Ready,
// or from when the controller first saw it. A replica Ready for longer than a
// rate window, but routed to only partway through a minute -- an idle fleet
// hit by a burst, or a release of the scheduler's flow-control queue --
// passed it, and its first saturated rate[1m] covers only the part of the
// minute it served. That reading went into mu's window as a service rate.
// Measured on the property case first-sample/mu/joiner-rate-timebase through
// Analyze, a replica routed to 10 s before a cycle recorded 0.0377 req/s
// against a truth of 0.2055 (seed 1), and three minutes on its mu still read
// 0.1918, 7 % low. On 100 of 100 seeds the first mu on record was outside the
// band of the figure three minutes later. With an ITL line, so that
// muChoice.minMeasuredOfDerived refuses a reading under half the derived mu,
// the readings that served 30-50 s of the minute still pass
// (first-sample/mu/joiner-rate-timebase-line). A low mu over-orders: money,
// not SLO.
//
// What the replica did is read from the settle tracker, by role
// (settle.Row.Activity). An idle row is positive evidence of no work: no KV
// held at the minute's peak, and no token generated (a prefill replica: no
// request completed). A replica that holds KV is never idle, whatever its
// rates read: a full, queued replica with no completion yet is unknown, not
// idle. The first row of load after an idle run is a return
// (settle.Replica.Returns), and the tracker keeps the returns of the current
// settle only: one that began at the Pod's Ready time, a restart, a rejoin, a
// rollout or a lend.
//
// Until a rate window has passed from the FIRST return of the current settle,
// a saturated reading is the joined test's, refused as for a replica that has
// just become Ready (P1-obs-unsettled, k2UnsettledRoutedLate). The first
// return is the replica first routed to after it became Ready (or after the
// controller first saw it idle): the case of the issue. A later return, after
// the replica has served under the same settle, is not held. The reverted
// first attempt (f32a32ed7) held every return, and on the pd-restart golden,
// whose decode replicas idle and return as the load steps, that refused
// enough readings to leave decode's mu 27 % low and its k2 at 42 868 against
// 55 212. A replica already serving when the tracker first saw it is held at
// its first return after an idle run: its rate window holds idle seconds
// then, too.
//
// k2 is refused with mu, not only mu. Mu is written only beside a k2 write,
// and k2's settle rate test reads the mu on record: refusing mu alone left
// that test with no mu, and the k2 readings it admitted then wrote ramp
// readings as mu (f32a32ed7). The cost is valid k2 readings. A
// max_over_time[1m] KV peak is not lowered by the idle part of the minute, so
// the k2 reading of a full replica is right while its rate is not, and it is
// lost. On the e1 golden, model-02's variant-01 saturates only in the first
// minute after its first return (14:02:16 and 14:02:31, holding 363 871
// tokens), so it never records an observed k2 and prices from the derived
// figure; its capacity does not move.
//
// The routing began at most one cycle before the return's row, so a window
// from that row holds no idle second. The cost: a replica routed to after
// standing idle records its first k2 and mu a rate window after its first
// served cycle (4 cycles at 15 s), and k2's first reading of a fresh window
// waits one more (k2_confirm.go).

// SwitchJoinedFromFirstServed is the replay switch for the clock (#392): on
// from the constructor; a replay turns it off (SetSwitchesForReplay), and the
// joined test ages a replica from its Ready time and first sight alone, as
// before.
const SwitchJoinedFromFirstServed = "joined-clock-starts-at-first-served"

// k2UnsettledRoutedLate is the settle test's reason for a saturated reading
// from a replica whose load first returned, since its settle began, under a
// rate window ago (SwitchJoinedFromFirstServed).
const k2UnsettledRoutedLate = "the replica's load first returned after an idle run under a rate window ago: its one-minute rates still hold the idle part of the minute before it was routed to; not recorded"

// firstReturnUnderWindow reports whether the replica in is Ready and its
// first return of load after an idle run, in its current settle, is under
// JoinedReplicaWindow before now, as the settle tracker reads it.
//
// The first return, not the latest (settle.Replica.ReturnedAfter): see the
// header. The tracker keeps the last eight returns; past eight, the first
// kept is still minutes old, as each return needs an idle row before it.
func (a *SaturationAnalyzer) firstReturnUnderWindow(in k2SettleInput, now time.Time) bool {
	if !a.joinedFromFirstServed || !in.ready {
		return false
	}
	key := settlerow.Key(domain.ReplicaMetrics{PodName: in.pod, VariantName: in.variant, FromWarmPool: in.bridge})
	r, ok := a.settleView().Replica(settleModel(in.namespace, in.modelID), key)
	if !ok {
		return false
	}
	at, ok := firstReturn(r)
	return ok && now.Sub(at) < JoinedReplicaWindow
}

// firstReturn is the first return of load after an idle run in r's current
// settle, and whether there is one.
func firstReturn(r settle.Replica) (time.Time, bool) {
	if len(r.Returns) == 0 {
		return time.Time{}, false
	}
	return r.Returns[0].At, true
}

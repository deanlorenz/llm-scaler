package scalefromzero

import (
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
)

// Issue #215: a wake decided in the instant before the steady-state engine
// had read the persisted in-flight ledger back did not see the holds it
// carries, and took the node an Unschedulable replica ordered before the
// restart was held for. Until the ledger is read back, a wake fitted to nodes
// waits; once it is, the wake sees the restored hold.
//
// The node store, the usage store and the in-flight ledger are process-wide:
// no t.Parallel.
func TestAWakeWaitsForTheRestoredInFlightHolds(t *testing.T) {
	publishUsage(t)
	publishNodes(t, 8)
	allocation.DefaultInFlight.ExpectRestore()
	e := physicalEngine(8)
	e.beginTick()
	if got := wakeOnce(t, e, "a", "cold", cand("cold-decode", "decode", wakeHBM3, 8, 1)); got != OutcomeAwaitingInFlight {
		t.Fatalf("a wake before the in-flight ledger was read back was judged (%s): it took the node a restored hold keeps", got)
	}

	// The steady-state engine reads the ledger back: an Unschedulable
	// replica's hold on the one free node.
	before := allocation.NewInFlightLedger()
	now := time.Now()
	before.Admit("b/warm-v", allocation.ReplicaShape{Variant: "b/warm-v", Accelerator: wakeHBM3, GPUs: 8}, 2, 1, nil, now)
	raw, err := before.MarshalInFlight(now)
	if err != nil {
		t.Fatal(err)
	}
	p, err := allocation.UnmarshalInFlight(raw)
	if err != nil {
		t.Fatal(err)
	}
	allocation.DefaultInFlight.RestoreInFlight(p, now, allocation.InFlightTTL)
	allocation.DefaultInFlight.MarkRestored()

	e.beginTick()
	if got := wakeOnce(t, e, "a", "cold", cand("cold-decode", "decode", wakeHBM3, 8, 1)); got != OutcomeNoCapacity {
		t.Fatalf("a wake after the read-back took the node a restored hold keeps: %s, want %s", got, OutcomeNoCapacity)
	}
}

// A ledger that is never read back holds wakes for inFlightRestoreWait, not
// for good: a parked model still wakes.
func TestAWakeStopsWaitingForAnUnreadLedger(t *testing.T) {
	publishUsage(t)
	publishNodes(t, 8)
	allocation.DefaultInFlight.ExpectRestore()
	e := physicalEngine(8)
	e.beginTick()
	if got := wakeOnce(t, e, "a", "cold", cand("cold-decode", "decode", wakeHBM3, 8, 1)); got != OutcomeAwaitingInFlight {
		t.Fatalf("setup: the wake did not wait (%s)", got)
	}
	e.inFlightWait = time.Now().Add(-inFlightRestoreWait)
	e.beginTick()
	if got := wakeOnce(t, e, "a", "cold", cand("cold-decode", "decode", wakeHBM3, 8, 1)); got == OutcomeAwaitingInFlight || got == OutcomeNoCapacity {
		t.Fatalf("a wake still waited, or was refused, past inFlightRestoreWait: %s", got)
	}
}

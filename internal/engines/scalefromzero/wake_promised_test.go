package scalefromzero

import (
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
)

// Review round 2 (whole-node m1 / Go 3): a node the utilization share freed
// and holds for a receiver whose pod is still Pending looked free to a wake.
// 16 free as 8+4+4 with 8 promised: the pool still holds an 8-GPU replica,
// but the 8-GPU node is the receiver's. Process-wide stores: no t.Parallel.
func TestAWakeDoesNotTakeAPromisedNode(t *testing.T) {
	publishUsage(t)
	publishNodes(t, 8, 4, 4)
	t.Cleanup(func() { decision.PublishSharePromised(nil, time.Time{}) })
	wake := func() SelectionOutcome {
		e := physicalEngine(16)
		e.beginTick()
		return wakeOnce(t, e, "a", "m", cand("m-decode", "decode", wakeHBM3, 8, 1))
	}
	decision.PublishSharePromised(nil, time.Now())
	if got := wake(); got == OutcomeNoCapacity {
		t.Fatalf("setup: nothing promised, the wake was refused (%s)", got)
	}
	decision.PublishSharePromised(map[string]map[string]int{"": {wakeHBM3: 8}}, time.Now())
	if got := wake(); got != OutcomeNoCapacity {
		t.Fatalf("woke into the node promised to a receiver: %s, want %s", got, OutcomeNoCapacity)
	}
}

package allocation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
)

// Review M3 and the round-3 safety net: a replica ordered without a node hole
// -- growth into nominal its queue lent out, Kueue's to admit by preempting a
// borrower -- may never place: Kueue preempts by priority, not by node, and
// the borrower it takes may be a pod that was itself Pending. Namespace b is
// within its guaranteed figure (24 of 32 used), so the borrowed placement
// timeout never fired for it, and x's third 8-GPU replica waited Pending for
// good. The governor keeps it under the placement timeout like a borrowed
// one: released past the timeout (borrow-unplaced), the model backed off. A
// replica the record does not mark NoHole is left alone, as before.
func TestNoHoleReplicaReleasedAfterPlacementTimeout(t *testing.T) {
	t0 := time.Unix(50_000, 0)
	settings := DefaultBorrowSettings()
	for _, tc := range []struct {
		name   string
		noHole bool
		want   int
	}{
		{"ordered without a hole, Pending past the timeout: released", true, 2},
		{"control: a replica not ordered that way is left alone", false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewBorrowGovernor(settings)
			world := govWorld{g: 32, used: 24}
			x := unplacedVariant(govModel("x", 3, 8), "x-v", t0)
			c := BorrowCycle{Now: t0, Constraints: world.constraints(), Requests: []ModelScalingRequest{x}}
			if tc.noHole {
				// The record holds the reclaim in flight, then lagging, until
				// a read shows it; the pod is Pending all along.
				c.NoHole = NoHoleOf([]BorrowDraw{{Namespace: "b", Variant: "x-v", GPUs: 8, Replicas: 1, Reclaim: true, NoHole: true}})
			}
			g.Observe(c)
			if rel := g.Releases(c, decisionsOf(c.Requests)); len(rel) != 0 {
				t.Fatalf("released %+v inside the placement timeout", rel)
			}
			// Past the timeout; the read now shows the replica, so the
			// record no longer holds it.
			c = BorrowCycle{Now: t0.Add(settings.PlacementTimeout + time.Minute), Constraints: world.constraints(),
				Requests: []ModelScalingRequest{x}}
			g.Observe(c)
			ds := decisionsOf(c.Requests)
			g.Releases(c, ds)
			if got := ds[0].TargetReplicas; got != tc.want {
				t.Fatalf("x's target is %d, want %d", got, tc.want)
			}
			if !tc.noHole {
				return
			}
			if !strings.Contains(ds[0].Reason(), "borrow-unplaced") {
				t.Fatalf("reason %q, want borrow-unplaced", ds[0].Reason())
			}
			if !g.CoolingDown("b", "x", c.Now) {
				t.Fatal("the model is not backed off after the release")
			}
		})
	}
}

// The placer marks a reclaimed replica NoHole only when no node held it.
func TestReclaimDrawMarksNoHole(t *testing.T) {
	for _, tc := range []struct {
		name string
		used int
		want bool
	}{
		{"the node is full: ordered without a hole", 8, true},
		{"the node has a hole: placed on it", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewGreedyByScoreOptimizer()
			o.Nodes = NewNodeView(map[string]decision.NodeGPU{"n1": h100Node(tc.used, nil)})
			o.Optimize(context.Background(), []ModelScalingRequest{borrowModel("a", "m", 8, 0, 1, nil)},
				[]*ResourceConstraints{physicalH100(64), borrowQuota(0, nsBorrow{ns: "a", guaranteed: 8, cap: 0, wn: 8, lentOut: 8})})
			rs := o.ReclaimDraws()
			if len(rs) != 1 || rs[0].NoHole != tc.want {
				t.Fatalf("reclaims %+v, want one with NoHole %v", rs, tc.want)
			}
		})
	}
}

// Backed off (ReclaimBlocked), a model's growth into lent-out nominal needs a
// real hole: its next replica is refused by node fit on full nodes, where it
// was placed without one before.
func TestReclaimBlockedNeedsAHole(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blocked bool
		want    int
	}{
		{"not backed off: placed without a hole", false, 8},
		{"backed off: refused on full nodes", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			physical := physicalH100(8)
			physical.Pools["H100"] = ResourcePool{Limit: 8, Used: 8}
			o := NewGreedyByScoreOptimizer()
			o.Nodes = NewNodeView(map[string]decision.NodeGPU{"n1": h100Node(8, nil)})
			if tc.blocked {
				o.ReclaimBlocked = func(ns, model string) bool { return ns == "a" && model == "m" }
			}
			ds := o.Optimize(context.Background(), []ModelScalingRequest{borrowModel("a", "m", 8, 0, 1, nil)},
				[]*ResourceConstraints{physical, borrowQuota(0, nsBorrow{ns: "a", guaranteed: 8, cap: 0, wn: 8, lentOut: 8})})
			if got := addedGPUs(ds)["m"]; got != tc.want {
				t.Fatalf("m grew %d GPUs, want %d", got, tc.want)
			}
		})
	}
}

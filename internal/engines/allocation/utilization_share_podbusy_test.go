package allocation

import (
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// A role keeps its spare while one of its pods holds a queue above the return
// load of the saturation queue, however calm the fleet reads: on a cluster run
// one pod held 235 queued requests beside seven idle siblings, and a return
// started during it. Q holds 7 against a target of 2 and would return 5.
// Controls: Q's busiest pod at the return load returns them; a busy pod on L
// does not hold Q's back.
func TestShareReturnWaitsOnTheBusiestPod(t *testing.T) {
	returns := func(busy map[string]float64) (int, int) {
		in := spareRun(2, 0.4, 7)
		in.BusiestQueueLoad = busy
		n, withheld := 0, 0
		for _, p := range planEach(NewShareLedger(), in, 3) {
			n += p.Returns
			withheld += p.Withheld[ShareWithheldReturnPodBusy]
		}
		return n, withheld
	}
	if n, w := returns(map[string]float64{"Q": 0.8}); n != 0 || w == 0 {
		t.Errorf("Q returned %d with a pod at 0.8 of its saturation queue (withheld %d times), want 0 and a return-pod-busy", n, w)
	}
	if n, w := returns(map[string]float64{"Q": 0.6}); n != 5 || w != 0 {
		t.Errorf("control: Q returned %d with its busiest pod at the return load (withheld %d), want 5", n, w)
	}
	if n, _ := returns(map[string]float64{"L": 5}); n != 5 {
		t.Errorf("control: Q returned %d with a busy pod on L, want 5", n)
	}
}

// The group carries each role's busiest queue from its analyzer result; a
// role with no queue reading, or none above zero, is absent.
func TestShareGroupCarriesTheBusiestQueue(t *testing.T) {
	reqs := make([]ModelScalingRequest, 0, 2)
	for m, q := range map[string]float64{"A": 2.5, "B": 0} {
		r := shareReq(m, "ns", 1000, 1)
		r.CompositeSignal.Result.RoleBusiestQueueLoad = map[string]float64{domain.RoleBoth: q}
		reqs = append(reqs, r)
	}
	groups := BuildShareGroups(reqs, []*ResourceConstraints{{Pools: map[string]ResourcePool{"A100": {Limit: 10, Used: 3}}}},
		ShareGroupOptions{ClusterIsQuota: true})
	if len(groups) != 1 || len(groups[0].Roles) != 2 {
		t.Fatalf("setup: groups %+v, want one group of two roles", groups)
	}
	g := groups[0]
	if len(g.BusiestQueueLoad) != 1 {
		t.Fatalf("BusiestQueueLoad %v, want A's only", g.BusiestQueueLoad)
	}
	for k, v := range g.BusiestQueueLoad {
		if v != 2.5 {
			t.Fatalf("%s carries %v, want 2.5", k, v)
		}
	}
}

package allocation

import (
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// TestShareGroupsHonourCompositeHolds: the share optimizer sizes from demand,
// not from the zeroed RequiredCapacity/SpareCapacity, so it reads the holds
// itself. A role nothing listed can size freezes the model; a role that may
// not shrink keeps a floor at what it has and gives nothing away.
func TestShareGroupsHonourCompositeHolds(t *testing.T) {
	full := []*ResourceConstraints{{Pools: map[string]ResourcePool{"A100": {Limit: 16, Used: 16}}}}
	build := func(hold *RoleHold) ShareGroup {
		t.Helper()
		req := shareReq("A", "ns", 800, 5) // a need of 1 GPU, holding 5
		if hold != nil {
			req.CompositeSignal.Holds = map[string]RoleHold{domain.RoleBoth: *hold}
		}
		groups := BuildShareGroups([]ModelScalingRequest{req}, full, ShareGroupOptions{ClusterIsQuota: true})
		if len(groups) != 1 {
			t.Fatalf("got %d groups, want 1", len(groups))
		}
		return groups[0]
	}

	free := build(nil)
	if len(free.Roles) != 1 || free.Roles[0].Floor != 1 {
		t.Fatalf("setup: unheld role %+v, want one role with floor 1", free.Roles)
	}
	if _, ok := free.Give["ns/A/both"]; !ok {
		t.Fatal("setup: an unheld role with spare can give")
	}

	down := build(&RoleHold{NoScaleDown: true})
	if len(down.Roles) != 1 || down.Roles[0].Floor != 5 {
		t.Fatalf("no scale-down: roles %+v, want one role with floor 5 (what it holds)", down.Roles)
	}
	if _, ok := down.Give["ns/A/both"]; ok {
		t.Fatal("no scale-down: the role must give nothing")
	}

	both := build(&RoleHold{NoScaleUp: true, NoScaleDown: true})
	if len(both.Roles) != 0 || len(both.Frozen) != 1 {
		t.Fatalf("held both ways: roles %+v frozen %v, want the model frozen", both.Roles, both.Frozen)
	}
}

package allocation

import (
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// The placement timeout in a Kueue-backed namespace that counts no
// borrowing (gatedRelease), in 8-GPU units: serving holds 16 against a Kueue
// figure of 8, one of its two replicas at Kueue's admission gate since t0.
// Only a replica Kueue has not admitted is the queue's to give back, and only
// past the figure.
func TestGatedReleaseOutsideBorrowing(t *testing.T) {
	t0 := time.Unix(50_000, 0)
	timeout := DefaultBorrowSettings().PlacementTimeout
	two := 2
	for _, tc := range []struct {
		name        string
		figure      int
		kueueBacked bool
		gated       bool
		minReplicas *int
		want        int
	}{
		{"gated past the figure: released", 8, true, true, nil, 1},
		{"Unschedulable, admitted by Kueue: not the queue's to give back", 8, true, false, nil, 2},
		{"gated within the figure: Kueue admits it", 16, true, true, nil, 2},
		{"a static quota, no Kueue: nothing gates on it", 8, false, true, nil, 2},
		{"at minReplicas: kept", 8, true, true, &two, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &ResourceConstraints{ProviderName: "kq", NamespaceScoped: true, KueueBacked: tc.kueueBacked,
				NamespacePools: map[string]map[string]ResourcePool{"serving": {"H100": {Limit: tc.figure, Used: 16}}}}
			st := domain.VariantReplicaState{VariantName: "v", CurrentReplicas: 2, GPUsPerReplica: 8, AcceleratorName: "H100",
				MinReplicas: tc.minReplicas, UnplacedReplicas: 1, UnplacedSince: t0, UnplacedPods: []string{"v-1"},
				UnplacedAt: map[string]time.Time{"v-1": t0}}
			if tc.gated {
				st.GatedAt = map[string]time.Time{"v-1": t0}
			}
			reqs := []ModelScalingRequest{{ModelID: "m", Namespace: "serving", VariantStates: []domain.VariantReplicaState{st}}}
			decisions := func() []domain.VariantDecision {
				return []domain.VariantDecision{{Namespace: "serving", ModelID: "m", VariantName: "v", AcceleratorName: "H100",
					CurrentReplicas: 2, TargetReplicas: 2, GPUsPerReplica: 8}}
			}
			g := NewBorrowGovernor(DefaultBorrowSettings())
			cycle := func(at time.Time) []domain.VariantDecision {
				c := BorrowCycle{Now: at, Constraints: []*ResourceConstraints{c}, Requests: reqs}
				g.Observe(c)
				ds := decisions()
				rel := g.Releases(c, ds)
				for _, r := range rel {
					if !r.AtGate || r.Cause != ReleasePlacementTimeout {
						t.Errorf("release %+v, want AtGate with cause %s", r, ReleasePlacementTimeout)
					}
				}
				return ds
			}
			if ds := cycle(t0); ds[0].TargetReplicas != 2 {
				t.Fatalf("released at once to %d, before the placement timeout", ds[0].TargetReplicas)
			}
			if ds := cycle(t0.Add(timeout)); ds[0].TargetReplicas != tc.want {
				t.Fatalf("target %d after the placement timeout, want %d", ds[0].TargetReplicas, tc.want)
			}
		})
	}
}

// A namespace counted for borrowing is not also governed as an uncounted one:
// its figure is NamespaceBorrow's guaranteed one, and the counted rules
// (borrowedRelease) alone apply to it.
func TestUncountedHeldPastSkipsCountedNamespaces(t *testing.T) {
	counted := &ResourceConstraints{ProviderName: "kq", KueueBacked: true,
		NamespacePools:  map[string]map[string]ResourcePool{"b": {"H100": {Limit: 0, Used: 16}}, "u": {"H100": {Limit: 8, Used: 16}}},
		NamespaceBorrow: map[string]map[string]NamespaceBorrow{"b": {"H100": {Cap: -1}}}}
	got := uncountedHeldPast([]*ResourceConstraints{counted})
	if _, ok := got["b"]; ok || got["u"]["H100"] != 8 {
		t.Fatalf("uncountedHeldPast = %v, want only u's 8", got)
	}
}

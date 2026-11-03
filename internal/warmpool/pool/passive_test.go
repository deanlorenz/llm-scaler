package pool

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

// countingPool records every call that reaches it.
type countingPool struct{ calls []string }

func (p *countingPool) ListWarm(context.Context) ([]Membership, error) {
	p.calls = append(p.calls, "list")
	return nil, nil
}
func (p *countingPool) Warm(context.Context, types.NamespacedName, ModelRef, Tier) error {
	p.calls = append(p.calls, "warm")
	return nil
}
func (p *countingPool) Activate(context.Context, types.NamespacedName, ModelRef) (Endpoint, error) {
	p.calls = append(p.calls, "activate")
	return Endpoint{}, nil
}
func (p *countingPool) Deactivate(context.Context, types.NamespacedName, ModelRef) error {
	p.calls = append(p.calls, "deactivate")
	return nil
}
func (p *countingPool) Evict(context.Context, types.NamespacedName, ModelRef) error {
	p.calls = append(p.calls, "evict")
	return nil
}

// TestPassiveGuardSkipsPassiveModels: the pool writes nothing for a passive
// model -- no supervisor create or delete, no pod patch -- and passes an
// active model's calls through.
func TestPassiveGuardSkipsPassiveModels(t *testing.T) {
	inner := &countingPool{}
	g := &PassiveGuard{Pool: inner, Passive: func(_, variant string) bool { return variant == "observed" }}
	ctx, pod := context.Background(), types.NamespacedName{Namespace: "pool", Name: "p0"}
	run := func(variant string) []error {
		m := ModelRef{Namespace: "ns", Variant: variant}
		_, aerr := g.Activate(ctx, pod, m)
		return []error{g.Warm(ctx, pod, m, Ram), aerr, g.Deactivate(ctx, pod, m), g.Evict(ctx, pod, m)}
	}
	for i, err := range run("observed") {
		if !errors.Is(err, ErrPassive) {
			t.Errorf("call %d for a passive model: err = %v, want ErrPassive", i, err)
		}
	}
	if len(inner.calls) != 0 {
		t.Fatalf("a passive model's calls reached the pool: %v", inner.calls)
	}
	if _, err := g.ListWarm(ctx); err != nil {
		t.Fatal(err)
	}
	for i, err := range run("scaled") {
		if err != nil {
			t.Errorf("call %d for an active model: %v", i, err)
		}
	}
	want := []string{"list", "activate", "warm", "deactivate", "evict"}
	if len(inner.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", inner.calls, want)
	}
	for i := range want {
		if inner.calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", inner.calls, want)
		}
	}
}

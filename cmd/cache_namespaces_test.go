package main

import (
	"maps"
	"slices"
	"testing"
)

// A namespace-scoped manager caches each namespace it manages and its own,
// where its scaling policy lives: a policy edit there must reach the ConfigMap
// reconciler without a restart.
func TestCacheNamespaces(t *testing.T) {
	cases := []struct {
		name    string
		watched []string
		own     string
		want    []string
	}{
		{"self-managed", []string{"team-a"}, "team-a", []string{"team-a"}},
		{"out of reach", []string{"team-a"}, "wva-system", []string{"team-a", "wva-system"}},
		{"several", []string{"team-a", "team-b"}, "wva-system", []string{"team-a", "team-b", "wva-system"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := slices.Sorted(maps.Keys(cacheNamespaces(c.watched, c.own)))
			if !slices.Equal(got, c.want) {
				t.Errorf("cacheNamespaces(%q, %q) = %q, want %q", c.watched, c.own, got, c.want)
			}
		})
	}
}

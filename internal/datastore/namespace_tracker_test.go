package datastore

import (
	"sync"
	"testing"
)

// A Track that lands while an Untrack of the namespace's last resource is
// emptying it must leave the namespace tracked. Untrack used to check the inner
// map for emptiness and then delete the namespace in separate steps, so a Track
// in between stored into a map that was then detached, and the namespace read as
// untracked although a live resource was registered in it.
func TestNamespaceTrackConcurrentWithUntrackKeepsNamespaceTracked(t *testing.T) {
	const rounds = 20000
	ds := NewDatastore(nil)

	lost := 0
	for i := range rounds {
		ds.NamespaceTrack("VariantAutoscaling", "leaving", "ns")

		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			ds.NamespaceUntrack("VariantAutoscaling", "leaving", "ns")
		}()
		go func() {
			defer wg.Done()
			<-start
			ds.NamespaceTrack("InferencePool", "arriving", "ns")
		}()
		close(start)
		wg.Wait()

		if !ds.IsNamespaceTracked("ns") {
			lost++
		}
		ds.NamespaceUntrack("InferencePool", "arriving", "ns")
		if ds.IsNamespaceTracked("ns") {
			t.Fatalf("round %d: namespace still tracked after its last resource was untracked", i)
		}
	}
	if lost > 0 {
		t.Fatalf("namespace with a live resource read as untracked in %d of %d rounds", lost, rounds)
	}
}

func TestNamespaceTrackerBookkeeping(t *testing.T) {
	ds := NewDatastore(nil)

	ds.NamespaceTrack("VariantAutoscaling", "a", "ns1")
	ds.NamespaceTrack("VariantAutoscaling", "a", "ns1") // idempotent
	ds.NamespaceTrack("InferencePool", "p", "ns1")
	ds.NamespaceTrack("VariantAutoscaling", "b", "ns2")
	ds.NamespaceTrack("", "x", "ns3") // ignored
	ds.NamespaceTrack("VariantAutoscaling", "", "ns3")

	if got := len(ds.ListTrackedNamespaces()); got != 2 {
		t.Fatalf("tracked namespaces = %d, want 2", got)
	}

	ds.NamespaceUntrack("VariantAutoscaling", "a", "ns1")
	if !ds.IsNamespaceTracked("ns1") {
		t.Fatal("ns1 untracked while an InferencePool is still registered")
	}
	ds.NamespaceUntrack("InferencePool", "p", "ns1")
	if ds.IsNamespaceTracked("ns1") {
		t.Fatal("ns1 still tracked after its last resource left")
	}
	ds.NamespaceUntrack("VariantAutoscaling", "missing", "ns2") // unknown resource: no-op
	if !ds.IsNamespaceTracked("ns2") {
		t.Fatal("untracking an unknown resource dropped ns2")
	}
	if ds.IsNamespaceTracked("") {
		t.Fatal("empty namespace reported tracked")
	}
}

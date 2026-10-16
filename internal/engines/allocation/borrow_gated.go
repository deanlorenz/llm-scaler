package allocation

import (
	"maps"
	"slices"
)

// Replicas held at Kueue's admission gate in a namespace that counts no
// borrowing: countBorrowable false, a queue in no cohort, or any other reason
// the entry withholds it. Kueue gates a pod its queue has no room for, and
// with nothing borrowed nothing preempts for it either: it waits for good.
// The engine lists those pods in every Kueue-backed namespace, so the quota
// charge does not count them as reserved (KU1), and the governor releases one
// held there past the placement timeout (gatedRelease), as it does a borrowed
// one.

// KueueNamespaces is every namespace a Kueue-backed constraint (KueueBacked)
// carries figures for, sorted: the namespaces whose pods at Kueue's admission
// gate the engine lists, borrowing counted or not.
func KueueNamespaces(constraints []*ResourceConstraints) []string {
	set := map[string]bool{}
	for _, c := range constraints {
		if c == nil || !c.KueueBacked {
			continue
		}
		for ns := range c.NamespacePools {
			set[ns] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// uncountedHeldPast is, per Kueue-backed namespace no constraint counts
// borrowing for, and per type, the GPUs its variants hold past its Kueue
// figure (the pool's Limit, charged with what Kueue reserved for others),
// gated replicas included: Kueue admits nothing past that figure, and nothing
// reclaims for it. Within the figure Kueue admits the replica, or reclaims
// lent-out nominal for it. Two entries over one namespace count the larger.
// The counted namespaces are namespaceHeldPast's.
func uncountedHeldPast(constraints []*ResourceConstraints) map[string]map[string]int {
	counted := map[string]bool{}
	for _, c := range constraints {
		if c == nil {
			continue
		}
		for ns := range c.NamespaceBorrow {
			counted[ns] = true
		}
	}
	out := map[string]map[string]int{}
	for _, c := range constraints {
		if c == nil || !c.KueueBacked {
			continue
		}
		for ns, pools := range c.NamespacePools {
			if counted[ns] {
				continue
			}
			for t, p := range pools {
				if p.Limit < 0 || p.Used <= p.Limit {
					continue
				}
				if out[ns] == nil {
					out[ns] = map[string]int{}
				}
				out[ns][t] = max(out[ns][t], p.Used-p.Limit)
			}
		}
	}
	return out
}

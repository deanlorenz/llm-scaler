package allocation

import (
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/accelerator"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
)

// FitsGPUBudget reports whether allocating demand — GPUs keyed by accelerator
// type — is permitted in namespace under the given constraints.
//
// It answers the question the scale-from-zero engine has to ask before waking a
// variant: would this actually get GPUs, or would its pods sit Pending while the
// request that asked for it times out anyway. Waking something that cannot be
// placed is worse than not waking it, because it looks like progress.
//
// The budget semantics are the optimizer's, reused rather than restated so the
// two cannot drift:
//
//   - Per-type budgets are the minimum available across providers, with the
//     Limit < 0 sentinel meaning unlimited (see mergeConstraints).
//   - A namespace that appears in any provider's NamespacePools is a CLOSED
//     allowlist: an accelerator type it does not list is denied outright, and a
//     listed type is capped at the tighter of its cluster and namespace budgets
//     (see mergeNamespaceConstraints).
//   - Any other namespace is judged only by the providers that bound every
//     namespace (see openBudgets). A namespace quota's pools are the other
//     tenants' free quota, and a namespace it excludes is not bound by them.
//     With a namespace quota and no such provider, nothing bounds it, so it
//     fits.
//   - An accelerator type no provider knows about is denied, matching the
//     optimizer's treatment of absent constraints as zero capacity rather than
//     unlimited.
//
// No constraints at all means "unknown", which returns true. That mirrors the
// engine's own fallback when no provider can supply constraints: proceed rather
// than block scaling silently. Denying instead would keep a model down for the
// first cycle after every restart — exactly when a queued request is waiting on
// it.
func FitsGPUBudget(constraints []*ResourceConstraints, namespace string, demand map[string]int) bool {
	if len(constraints) == 0 {
		return true
	}

	perType := mergeConstraints(constraints)
	nsBudgets, nsScoped := mergeNamespaceConstraints(constraints)[namespace]
	if !nsScoped {
		open, bounded := openBudgets(constraints)
		if !bounded {
			return true
		}
		perType = open
	}

	for rawType, need := range demand {
		if need <= 0 {
			continue
		}

		// A workload that constrains no accelerator can be placed on any of them,
		// so it fits if any pool does. Resolving it to a named pool would be a
		// guess the scheduler is free to contradict; see anyPoolFits.
		if rawType == constants.DefaultAcceleratorName {
			if nsScoped {
				if !anyPoolFits(nsBudgets, need) {
					return false
				}
				continue
			}
			if !anyPoolFits(perType, need) {
				return false
			}
			continue
		}

		var budget int
		if nsScoped {
			// A namespace present in any provider's NamespacePools is a CLOSED
			// allowlist, and it — not the cluster aggregate — decides what this
			// namespace may use. Resolve against it FIRST: a type the namespace
			// grants as unlimited is legitimately absent from the cluster Pools
			// (aggregateNamespacePools omits the -1 sentinel), so resolving
			// against the cluster first would deny a wake the optimizer allows.
			nsKey, listed := resolveAcceleratorKey(nsBudgets, rawType)
			if !listed {
				return false
			}
			budget = nsBudgets[nsKey]
			// Still bounded by the cluster pool where one is known: a namespace
			// may not be granted more than physically exists.
			if clusterKey, known := resolveAcceleratorKey(perType, rawType); known {
				budget = tighterBudget(budget, perType[clusterKey])
			}
		} else {
			accType, known := resolveAcceleratorKey(perType, rawType)
			if !known {
				return false
			}
			budget = perType[accType]
		}

		// A negative budget survives tighterBudget only when every contributing
		// budget was unlimited.
		if budget < 0 {
			continue
		}
		if need > budget {
			return false
		}
	}

	return true
}

// GPUBudgets reports the per-accelerator-type budgets an allocation in namespace
// is judged against, and whether that namespace is a closed allowlist (in which
// case a type absent from the map is denied outright rather than unconstrained).
// A negative budget is the unlimited sentinel. Nil constraints yield nil, which
// is "unknown" — the permissive case. A namespace that nothing bounds (see
// openBudgets) gets every type the providers name as unlimited.
//
// It exists so the budgets can be REPORTED with the same resolution FitsGPUBudget
// applies. Nothing else exposes what the check actually compared against: the
// pools carry Limit and Used, the merge takes the minimum across providers, and a
// namespace allowlist overrides the cluster aggregate — so a log line that summed
// the pools itself would be free to disagree with the verdict it purports to
// explain, which is worse than no line at all.
func GPUBudgets(constraints []*ResourceConstraints, namespace string) (map[string]int, bool) {
	if len(constraints) == 0 {
		return nil, false
	}

	nsBudgets, nsScoped := mergeNamespaceConstraints(constraints)[namespace]
	if !nsScoped {
		open, bounded := openBudgets(constraints)
		if bounded {
			return open, false
		}
		// Nothing bounds this namespace: every type the providers name is
		// reported unlimited, so the line does not read as "nothing free".
		unbounded := map[string]int{}
		for _, c := range constraints {
			if c == nil {
				continue
			}
			for accType := range c.Pools {
				unbounded[accType] = -1
			}
			for _, perType := range c.NamespacePools {
				for accType := range perType {
					unbounded[accType] = -1
				}
			}
		}
		return unbounded, false
	}
	perType := mergeConstraints(constraints)

	// Mirrors the namespace branch of FitsGPUBudget: the allowlist decides what the
	// namespace may use, still bounded by the cluster pool where one is known.
	budgets := make(map[string]int, len(nsBudgets))
	for accType, budget := range nsBudgets {
		if clusterKey, known := resolveAcceleratorKey(perType, accType); known {
			budget = tighterBudget(budget, perType[clusterKey])
		}
		budgets[accType] = budget
	}
	return budgets, true
}

// clusterProviders returns the providers whose pools bound every namespace --
// a cluster-scoped quota, or the physical inventory -- and whether any
// provider is a namespace-scoped quota. A provider that carries NamespacePools
// is namespace-scoped whether or not it says so.
func clusterProviders(constraints []*ResourceConstraints) ([]*ResourceConstraints, bool) {
	var cluster []*ResourceConstraints
	namespaced := false
	for _, c := range constraints {
		switch {
		case c == nil:
		case c.NamespaceScoped || len(c.NamespacePools) > 0:
			namespaced = true
		default:
			cluster = append(cluster, c)
		}
	}
	return cluster, namespaced
}

// openBudgets returns the per-type budget of a namespace that no
// namespace-scoped quota caps: the merged budget of the providers that bound
// every namespace. A namespace-scoped quota does not count. Its pools are the
// sum of the free quota of the namespaces it caps, and a namespace it excludes
// is not bound by them. bounded is false when a namespace-scoped quota exists
// and no provider bounds every namespace: then nothing bounds such a
// namespace, for any accelerator type. Without a namespace-scoped quota the
// budget is mergeConstraints of every provider, as before.
func openBudgets(constraints []*ResourceConstraints) (budgets map[string]int, bounded bool) {
	cluster, namespaced := clusterProviders(constraints)
	if namespaced && len(cluster) == 0 {
		return nil, false
	}
	return mergeConstraints(cluster), true
}

// resolveAcceleratorKey maps an accelerator name as a WORKLOAD declares it onto
// the key an accelerator-keyed map actually uses, reporting whether the map
// covers it.
//
// The two sides are written in different vocabularies and neither can be changed
// unilaterally. Physical pool keys come from node product labels normalized to
// short names ("A100"); quota keys are whatever the operator typed; and a
// workload's nodeSelector may carry either the full product name
// ("NVIDIA-A100-PCIE-80GB") or the short one.
//
// The matching order (declared name, then its short name, then either ignoring
// case) lives in accelerator.FindKey, with the reasons. The case fold is what
// lets a GKE node's "nvidia-l4" land on an operator's "L4".
//
// Shared by every accelerator-keyed lookup — physical limits, quotas, and the
// demand check — so they cannot drift in how they reconcile a name.
func resolveAcceleratorKey(known map[string]int, declared string) (string, bool) {
	return accelerator.FindKey(known, declared)
}

// anyPoolFits reports whether some pool in budgets can take need GPUs.
//
// This answers demand from a workload that constrains nothing. Its pod spec names
// no accelerator, so the scheduler may place it on any GPU node — "any accelerator
// can serve this" is the literal meaning of the configuration, not a guess about
// it. The placement question is therefore whether ANY pool has room, and asking it
// that way is both more correct and more permissive than the alternatives: naming
// one pool would assert something the scheduler can contradict, and denying
// outright would refuse a workload that several accelerators could have served.
//
// It subsumes the single-type case. With one pool, "any pool has room" and "the
// sole pool has room" are the same statement, which is why there is no separate
// homogeneous branch here.
//
// Attribution is a different question and is NOT answered here: which pool such a
// workload should be CHARGED to is genuinely unknown until its pods are scheduled.
// TypeInventory.SetUsed deduces it on a single-type cluster and leaves it
// unattributed otherwise, reported as wva_unattributed_gpus.
func anyPoolFits(budgets map[string]int, need int) bool {
	for _, budget := range budgets {
		if budget < 0 || need <= budget {
			return true
		}
	}
	return false
}

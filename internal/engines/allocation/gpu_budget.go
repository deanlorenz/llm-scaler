package allocation

import (
	"maps"
	"math"

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
//   - An uncapped namespace — one a namespace quota excludes and no namespace
//     quota caps — is judged only by the providers that bound every namespace
//     (see uncappedBudget). A namespace quota's pools are the other tenants'
//     free quota, and they do not bound it. A type none of those providers
//     lists is unbounded for it, so it fits.
//   - For any other namespace, an accelerator type no provider knows about is
//     denied, matching the optimizer's treatment of absent constraints as zero
//     capacity rather than unlimited.
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
	uncapped := newUncappedBudget(constraints).covers(namespace)
	if uncapped {
		perType = uncappedReport(constraints)
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
			if !anyPoolFits(perType, need) && !(uncapped && len(perType) == 0) {
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
				if uncapped {
					continue // no provider bounds this type for it
				}
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
// is "unknown" — the permissive case. For an uncapped namespace (see
// uncappedBudget) it reports the budgets of the providers that bound every
// namespace, and every other type a provider names as unlimited; a type no
// provider names is unbounded for it too.
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

	if newUncappedBudget(constraints).covers(namespace) {
		return uncappedReport(constraints), false
	}
	perType := mergeConstraints(constraints)
	nsBudgets, nsScoped := mergeNamespaceConstraints(constraints)[namespace]
	if !nsScoped {
		return perType, false
	}

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

// capsNamespaces reports whether c is a namespace-scoped provider: its pools
// are per-namespace caps, not a budget over every namespace. A provider that
// carries NamespacePools is namespace-scoped whether or not it says so.
func (c *ResourceConstraints) capsNamespaces() bool {
	return c.NamespaceScoped || len(c.NamespacePools) > 0
}

// clusterProviders returns the providers whose pools bound every namespace —
// a cluster-scoped quota, or the physical inventory — and whether any
// provider is a namespace-scoped quota.
func clusterProviders(constraints []*ResourceConstraints) (cluster []*ResourceConstraints, namespaced bool) {
	for _, c := range constraints {
		switch {
		case c == nil:
		case c.capsNamespaces():
			namespaced = true
		default:
			cluster = append(cluster, c)
		}
	}
	return cluster, namespaced
}

// uncappedBudget is the budget of the uncapped namespaces: the ones a
// namespace quota lists in `exclude` and no namespace quota caps. A namespace
// quota's pools are the sum of its tenants' free quota, so they do not bound
// an uncapped namespace; only the providers over every namespace (a cluster
// quota) do. A type none of those providers lists is unbounded for it.
//
// Capped namespaces spend this budget too, beside their own, so the two kinds
// share a cluster quota. A nil *uncappedBudget means no namespace is uncapped
// this cycle, and every namespace is planned exactly as without it.
type uncappedBudget struct {
	namespaces map[string]bool
	// budgets is the free GPUs per type of the providers over every
	// namespace. A type absent is unbounded; math.MaxInt is unlimited.
	budgets map[string]int
}

// newUncappedBudget returns the uncapped namespaces of constraints and their
// budget, or nil when there are none. A namespace one namespace quota
// excludes and another caps is capped: the cap binds it.
func newUncappedBudget(constraints []*ResourceConstraints) *uncappedBudget {
	capped := mergeNamespaceConstraints(constraints)
	var namespaces map[string]bool
	for _, c := range constraints {
		if c == nil {
			continue
		}
		for ns := range c.ExcludedNamespaces {
			if _, ok := capped[ns]; ok {
				continue
			}
			if namespaces == nil {
				namespaces = map[string]bool{}
			}
			namespaces[ns] = true
		}
	}
	if namespaces == nil {
		return nil
	}
	cluster, _ := clusterProviders(constraints)
	return &uncappedBudget{namespaces: namespaces, budgets: mergeConstraints(cluster)}
}

// covers reports whether namespace is uncapped.
func (u *uncappedBudget) covers(namespace string) bool {
	return u != nil && u.namespaces[namespace]
}

// free returns the GPUs of accType left for an uncapped namespace:
// math.MaxInt when nothing bounds the type.
func (u *uncappedBudget) free(accType string) int {
	if b, ok := u.budgets[accType]; ok {
		return b
	}
	return math.MaxInt
}

// clamp bounds n by the GPUs of accType left in the budget. A capped
// namespace spends the budget too, so its own budget is clamped by it.
func (u *uncappedBudget) clamp(accType string, n int) int {
	if u == nil {
		return n
	}
	return min(n, u.free(accType))
}

// spend takes n GPUs of accType from the budget. An unbounded or unlimited
// type has nothing to draw down.
func (u *uncappedBudget) spend(accType string, n int) {
	if u == nil {
		return
	}
	if b, ok := u.budgets[accType]; ok && b != math.MaxInt {
		u.budgets[accType] = b - n
	}
}

// uncappedReport returns the per-type budget an uncapped namespace is judged
// against: the merged budget of the providers over every namespace, and the
// unlimited sentinel (-1) for every other type a provider names.
func uncappedReport(constraints []*ResourceConstraints) map[string]int {
	out := map[string]int{}
	for _, c := range constraints {
		if c != nil {
			addTypes(out, c)
		}
	}
	cluster, _ := clusterProviders(constraints)
	maps.Copy(out, mergeConstraints(cluster))
	return out
}

// addTypes records every accelerator type c names, in its pools or in any
// namespace's, as unlimited in out.
func addTypes(out map[string]int, c *ResourceConstraints) {
	for accType := range c.Pools {
		out[accType] = -1
	}
	for _, perType := range c.NamespacePools {
		for accType := range perType {
			out[accType] = -1
		}
	}
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

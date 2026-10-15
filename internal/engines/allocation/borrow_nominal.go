package allocation

// A queue's nominal figures beside its borrowing (the Kueue borrowing
// proposal, PR #161, B3, B7 and B10), for every namespace a Kueue-reading
// entry reads, counted or not: what of its nominal a peer holds that Kueue
// reclaims for it (growth into it needs no node hole: Kueue preempts the
// borrowers whose pods fill those nodes), and what it can reach without
// borrowing (a queue that cannot reclaim reaches only what it kept plus the
// cohort's free pool).

import (
	"maps"
	"slices"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/accelerator"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/kueue"
)

// NominalFigures is one namespace's nominal figures for one accelerator type,
// summed over the queues it is granted.
type NominalFigures struct {
	// LentOut is the nominal of its reclaiming queues (reclaimWithinCohort Any,
	// or LowerPriority while it outranks the borrowers) that a peer may hold
	// now: growth into it places without a node hole, since Kueue preempts the
	// borrowers to admit it.
	LentOut int
	// WithinNominal is what it can reach without borrowing (B7).
	WithinNominal int
	// Guaranteed is what it reaches whatever the cohort's free pool holds:
	// nominal for a reclaiming queue, what it kept or uses for one that
	// cannot reclaim.
	Guaranteed int
	// NominalLentOut is the nominal it cannot reach: lent by a queue that cannot
	// reclaim (B10).
	NominalLentOut int
}

// uncountedNominal is, per namespace the read granted borrowing rows for and
// whose borrowing is not counted, and per accelerator type of the entry in
// force, its queues' nominal figures. A namespace withheld as not
// Kueue-managed has none: Kueue reclaims nothing for pods it does not manage,
// so its growth still needs a node hole. Rows the source withheld carry their
// nominal as within nominal and lend nothing reclaimable.
//
// A namespace that is only not selected (kueue.borrowNamespaces) is not
// evaluated for Kueue management; its queues being granted to it is taken as
// Kueue managing its pods, as for any namespace of a Kueue-backed entry.
func (q *QuotaInventory) uncountedNominal(ext config.ExternalQuotas, view *borrowView, entry config.QuotaLimiterConfig) map[string]map[string]NominalFigures {
	out := map[string]map[string]NominalFigures{}
	for ns, rows := range ext.Borrow {
		if !q.governsBorrowRows(ns, rows) || (view != nil && view.byNS[ns] != nil) {
			continue
		}
		if view != nil && view.withheld[ns] == string(kueue.ReasonNotKueueManaged) {
			continue
		}
		for accType := range entry.NamespaceQuotas[ns] {
			nominal, f := 0, NominalFigures{}
			for _, r := range rowsOfType(rows, accType) {
				nominal += r.Nominal
				f.WithinNominal += r.WithinNominal
				f.Guaranteed += r.Guaranteed
				if r.Counted() {
					f.LentOut += r.LentOut
				}
			}
			f.NominalLentOut = max(0, nominal-f.WithinNominal)
			if f.LentOut == 0 && f.NominalLentOut == 0 && f.WithinNominal <= f.Guaranteed {
				continue // nominal, whatever the pool holds: nothing to carry
			}
			if out[ns] == nil {
				out[ns] = map[string]NominalFigures{}
			}
			out[ns][accType] = f
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// rowsOfType is a namespace's rows of one type: those naming the accelerator,
// or, when none does, the untyped ones (as namespaceBorrow matches them).
func rowsOfType(rows []config.ExternalBorrow, accType string) []config.ExternalBorrow {
	matched := slices.DeleteFunc(slices.Clone(rows), func(r config.ExternalBorrow) bool {
		return r.Accelerator == "" || !accelerator.SameName(r.Accelerator, accType)
	})
	if len(matched) == 0 {
		matched = slices.DeleteFunc(slices.Clone(rows), func(r config.ExternalBorrow) bool { return r.Accelerator != "" })
	}
	return matched
}

// NamespaceNominal returns, for the active namespaces whose borrowing this
// entry reads and does not count, their nominal figures (NominalFigures); nil
// when there are none, or the last read was stale.
func (q *QuotaInventory) NamespaceNominal(activeNamespaces []string) map[string]map[string]NominalFigures {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if len(q.nominal) == 0 {
		return nil
	}
	out := map[string]map[string]NominalFigures{}
	for _, ns := range activeNamespaces {
		if f, ok := q.nominal[ns]; ok {
			out[ns] = maps.Clone(f)
		}
	}
	return out
}

// lentOutFor is the nominal of ns's reclaiming queues a peer holds, of the
// type acc, as constraints read it: from NamespaceBorrow where the namespace
// is counted, else from NamespaceNominal. Two entries reading one queue give
// the smaller figure.
func lentOutFor(constraints []*ResourceConstraints, ns, acc string) int {
	got, seen := 0, false
	take := func(n int) {
		if !seen || n < got {
			got, seen = n, true
		}
	}
	for _, c := range constraints {
		if c == nil {
			continue
		}
		if k, ok := accelerator.FindKey(c.NamespaceBorrow[ns], acc); ok {
			take(c.NamespaceBorrow[ns][k].LentOut)
		} else if k, ok := accelerator.FindKey(c.NamespaceNominal[ns], acc); ok {
			take(c.NamespaceNominal[ns][k].LentOut)
		}
	}
	return got
}

// lentLeft is, per type, the nominal ns's reclaiming queues lent out that the
// pass has not grown into yet: from its NamespaceBorrow when counted, else
// from its NamespaceNominal. Nil when none.
func (bs *borrowState) lentLeft(ns string) map[string]int {
	if bs == nil {
		return nil
	}
	left := bs.borrow[ns]
	if left == nil {
		left = bs.lent[ns]
	}
	var out map[string]int
	for t, b := range left {
		if b.lentOut > 0 {
			if out == nil {
				out = map[string]int{}
			}
			out[t] = b.lentOut
		}
	}
	return out
}

// withLent is available with, for namespace ns, what its reclaiming queues
// lent out and it has not grown into yet added to every bounded key covering
// the type: the borrowers hold those GPUs now, and Kueue preempts them to
// admit ns's growth, so the cluster bounds -- the physical limiter's free
// GPUs, a cluster quota -- must not refuse it for their being in use. The
// input when there is nothing to add.
func (bs *borrowState) withLent(ns string, available map[string]int) map[string]int {
	lent := bs.lentLeft(ns)
	if len(lent) == 0 || available == nil {
		return available
	}
	out := maps.Clone(available)
	for t, n := range lent {
		for k, v := range out {
			if !isUnboundedBudget(v) && accelerator.SameName(k, t) {
				out[k] = v + n
			}
		}
	}
	return out
}

// CreditReclaimable returns copies of constraints whose cluster bounds (every
// provider that is not namespace-scoped: the physical limiter, a cluster
// quota) count reclaimable -- per type, the nominal a namespace's reclaiming
// queues lent out (ReclaimableNominal) -- as free for that namespace's wake:
// the borrowers hold those GPUs, and Kueue preempts them to admit it. The
// input when reclaimable is empty.
func CreditReclaimable(constraints []*ResourceConstraints, reclaimable map[string]int) []*ResourceConstraints {
	if len(reclaimable) == 0 {
		return constraints
	}
	out := make([]*ResourceConstraints, 0, len(constraints))
	for _, c := range constraints {
		if c == nil || c.NamespaceScoped || len(c.Pools) == 0 {
			out = append(out, c)
			continue
		}
		cc := *c
		cc.Pools = maps.Clone(c.Pools)
		for t, p := range cc.Pools {
			for rt, n := range reclaimable {
				if accelerator.SameName(rt, t) {
					p.Used = max(0, p.Used-n)
				}
			}
			cc.Pools[t] = p
		}
		cc.TotalLimit, cc.TotalUsed, cc.TotalAvail = poolTotals(cc.Pools)
		out = append(out, &cc)
	}
	return out
}

// NominalLentOutAny reports whether some queue of ns, counted or not, lent
// nominal it cannot reclaim (NamespaceBorrow and NamespaceNominal's
// NominalLentOut): a wake refused within nominal then says why (B10).
func NominalLentOutAny(constraints []*ResourceConstraints, ns string) bool {
	if NominalLentOut(constraints, ns) > 0 {
		return true
	}
	for _, c := range constraints {
		if c == nil {
			continue
		}
		for _, f := range c.NamespaceNominal[ns] {
			if f.NominalLentOut > 0 {
				return true
			}
		}
	}
	return false
}

// WithinNominalNow is WithinNominal for a wake (A1, W7): the figures as of
// what the scaling manager asked for that the Kueue read does not show yet
// (u: borrowed draws in flight and lagging, gated and admitted pods) and the
// cohorts closed to new borrowing (blocked). A queue that cannot reclaim
// reaches past what it kept only through the cohort's free pool, so its
// within-nominal figure loses what of the pool those take: the steady-state
// engine's last free 8 GPUs, drawn for a decode a moment ago, are not there
// for a cold wake too. A namespace read and not counted is held at its
// queues' within-nominal figure (NamespaceNominal), its nominal otherwise,
// the same way. A reclaiming queue reaches its whole nominal whatever the
// pool holds, and is unchanged.
func WithinNominalNow(constraints []*ResourceConstraints, u Unreserved, blocked map[string]bool) []*ResourceConstraints {
	after := BlockCohorts(DeductUnreservedAt(constraints, u), blocked)
	adjusted := make([]*ResourceConstraints, 0, len(after))
	for i, c := range after {
		before := constraints[i]
		if c == nil || (c.NamespaceBorrow == nil && len(c.NamespaceNominal) == 0) {
			adjusted = append(adjusted, c)
			continue
		}
		cc := *c
		taken := func(ns, acc string) int {
			return max(0, chunkFreeFor(before, ns, acc)-chunkFreeFor(c, ns, acc))
		}
		if c.NamespaceBorrow != nil {
			cc.NamespaceBorrow = make(map[string]map[string]NamespaceBorrow, len(c.NamespaceBorrow))
			for ns, perType := range c.NamespaceBorrow {
				out := maps.Clone(perType)
				for t, b := range out {
					g := poolLimit(c, ns, t)
					if g < 0 || b.WithinNominal <= g {
						continue
					}
					b.WithinNominal = max(g, b.WithinNominal-taken(ns, t))
					out[t] = b
				}
				cc.NamespaceBorrow[ns] = out
			}
		}
		if len(c.NamespaceNominal) > 0 {
			cc.NamespacePools = maps.Clone(c.NamespacePools)
			for ns, perType := range c.NamespaceNominal {
				pools, ok := cc.NamespacePools[ns]
				if !ok {
					continue
				}
				pools = maps.Clone(pools)
				for t, f := range perType {
					k, found := accelerator.FindKey(pools, t)
					if !found {
						continue
					}
					p := pools[k]
					within := f.WithinNominal
					if within > f.Guaranteed {
						within = max(f.Guaranteed, within-taken(ns, t))
					}
					if p.Limit < 0 || within < p.Limit {
						p.Limit = within
					}
					pools[k] = p
				}
				cc.NamespacePools[ns] = pools
			}
		}
		adjusted = append(adjusted, &cc)
	}
	return WithinNominal(adjusted)
}

// chunkFreeFor is the free GPUs of ns's cohorts' chunks in c serving acc.
func chunkFreeFor(c *ResourceConstraints, ns, acc string) int {
	if c == nil {
		return 0
	}
	n := 0
	for _, p := range c.SharedPools {
		if slices.Contains(c.NamespaceCohorts[ns], p.Cohort) && p.serves(acc) {
			n += p.Free
		}
	}
	return n
}

// poolLimit is ns's pool Limit of type t in c (its guaranteed figure where
// borrowing is counted); config.QuotaUnlimited when it has none.
func poolLimit(c *ResourceConstraints, ns, t string) int {
	if k, ok := accelerator.FindKey(c.NamespacePools[ns], t); ok {
		return c.NamespacePools[ns][k].Limit
	}
	return config.QuotaUnlimited
}

// ReclaimableNominal is, per accelerator type, the nominal of ns's reclaiming
// queues that a peer holds (NominalFigures.LentOut): a wake into it needs no
// node hole, since Kueue preempts the borrowers to admit it. Read from the
// constraints before WithinNominal drops the borrowing figures.
func ReclaimableNominal(constraints []*ResourceConstraints, ns string) map[string]int {
	types := map[string]bool{}
	for _, c := range constraints {
		if c == nil {
			continue
		}
		for t := range c.NamespaceBorrow[ns] {
			types[t] = true
		}
		for t := range c.NamespaceNominal[ns] {
			types[t] = true
		}
	}
	var out map[string]int
	for t := range types {
		if n := lentOutFor(constraints, ns, t); n > 0 {
			if out == nil {
				out = map[string]int{}
			}
			out[t] = n
		}
	}
	return out
}

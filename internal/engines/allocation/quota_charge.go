package allocation

// The non-managed charge of a Kueue-backed quota (KU1).
//
// A quota is charged what the scaling manager's own variants hold
// (ManagedUsage): a static cap is an allowance for them alone. A figure read
// from Kueue is different. Kueue admits against everything it has reserved in
// a ClusterQueue -- a training job beside the serving variants included -- so
// the GPUs those other workloads hold are not there for the variants, whatever
// the figure says. Each queue's figures are therefore charged, before the
// static entry bounds them, with its non-managed charge: what Kueue has
// reserved in it less what the managed variants hold there. The managed usage
// is charged as before, against the charged figure.

import (
	"maps"
	"slices"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/accelerator"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
)

// ManagedUsageSetter is an Inventory that is told, before each Refresh, what
// the managed variants hold per namespace and accelerator type: the managed
// usage a Kueue-backed figure is charged against (chargeNonManaged).
type ManagedUsageSetter interface {
	SetManagedUsage(usageByNamespace map[string]map[string]int)
}

// SetManagedUsage records what the managed variants hold, per namespace and
// accelerator type, for the next Refresh to charge the Kueue figures with.
// Never called (nil), the figures are not charged.
func (q *QuotaInventory) SetManagedUsage(usageByNamespace map[string]map[string]int) {
	var usage map[string]map[string]int
	if usageByNamespace != nil {
		usage = make(map[string]map[string]int, len(usageByNamespace))
		for ns, perType := range usageByNamespace {
			usage[ns] = maps.Clone(perType)
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.managedUsage = usage
}

var _ ManagedUsageSetter = (*QuotaInventory)(nil)

// chargeNonManaged returns ext with every namespace's figures -- nominal,
// guaranteed and within nominal -- charged with the non-managed charge of the
// queues it is granted, and each borrowing row carrying its own (Charge).
//
// Per namespace, ClusterQueue, flavor and GPU resource, the charge is Kueue's
// Reserved less the GPUs that namespace's managed variants hold in that queue
// (namespaceCharge): another namespace sharing the queue is charged like any
// other workload. The managed usage of a
// namespace granted exactly one GPU queue is counted in that queue; a
// namespace granted several (multi-queue-namespace) counts in none, which
// charges its GPUs to every one of them, the cautious side. Managed usage is
// what the variants' replicas hold, gated pods included, so a replica Kueue
// has not admitted yet is counted as admitted: until it is, the charge of the
// queue it waits in is low by at most its GPUs.
//
// A namespace granted several queues is charged, per type, the largest of
// their charges. ext is returned as is when managed is nil (never set) or
// carries no rows.
func chargeNonManaged(ext config.ExternalQuotas, managed map[string]map[string]int) config.ExternalQuotas {
	if managed == nil || len(ext.Borrow) == 0 {
		return ext
	}
	out := ext
	out.Borrow = make(map[string][]config.ExternalBorrow, len(ext.Borrow))
	byNS := map[string]config.ExternalCaps{}
	for ns, rows := range ext.Borrow {
		charge := namespaceCharge(rows, managed[ns])
		charged := slices.Clone(rows)
		perQueue := map[string]config.ExternalCaps{}
		untyped := map[string]map[string]int{} // per queue, per resource: as clusterQueueCapsAt sums untyped lines
		for i := range charged {
			r := &charged[i]
			r.Charge = charge[r.Queue][r.Flavor+"/"+r.Resource]
			c := perQueue[r.Queue]
			if r.Accelerator != "" {
				c.ByType = addTypedCharge(c.ByType, r.Accelerator, r.Charge)
			} else {
				if untyped[r.Queue] == nil {
					untyped[r.Queue] = map[string]int{}
				}
				untyped[r.Queue][r.Resource] += r.Charge
			}
			perQueue[r.Queue] = c
		}
		for q, byRes := range untyped {
			c := perQueue[q]
			for _, n := range byRes {
				c.Untyped, c.HasUntyped = max(c.Untyped, n), true
			}
			perQueue[q] = c
		}
		out.Borrow[ns] = charged
		byNS[ns] = largestCharge(slices.Collect(maps.Values(perQueue)))
	}
	out.Namespace = chargeCaps(ext.Namespace, byNS)
	out.NamespaceGuaranteed = chargeCaps(ext.NamespaceGuaranteed, byNS)
	out.NamespaceWithinNominal = chargeCaps(ext.NamespaceWithinNominal, byNS)
	return out
}

// namespaceCharge is, per queue of one namespace's rows and per "flavor/
// resource", the queue's charge as that namespace sees it: what Kueue
// reserved in the queue less what this namespace's own variants hold there
// (held). Another namespace's GPUs in a shared queue are charged like any
// other workload's: Kueue admits them against the same nominal, so they are
// not this namespace's to spend.
func namespaceCharge(rows []config.ExternalBorrow, held map[string]int) map[string]map[string]int {
	var queues []string
	rowsOf := map[string][]config.ExternalBorrow{} // per queue, one row per flavor and resource
	for _, r := range rows {
		if !slices.Contains(queues, r.Queue) {
			queues = append(queues, r.Queue)
		}
		if !slices.ContainsFunc(rowsOf[r.Queue], func(o config.ExternalBorrow) bool {
			return o.Flavor == r.Flavor && o.Resource == r.Resource
		}) {
			rowsOf[r.Queue] = append(rowsOf[r.Queue], r)
		}
	}
	charge := map[string]map[string]int{}
	for _, q := range queues {
		var left map[string]int
		if len(queues) == 1 {
			left = maps.Clone(held)
		}
		charge[q] = map[string]int{}
		for _, r := range typedFirst(rowsOf[q]) {
			taken := takeManaged(left, r, rowsOf[q], r.Reserved)
			charge[q][r.Flavor+"/"+r.Resource] = max(0, r.Reserved-taken)
		}
	}
	return charge
}

// typedFirst is rows with the ones naming an accelerator first, each group in
// flavor and resource order: an untyped line takes the managed usage no typed
// line of its queue names.
func typedFirst(rows []config.ExternalBorrow) []config.ExternalBorrow {
	out := slices.Clone(rows)
	slices.SortStableFunc(out, func(a, b config.ExternalBorrow) int {
		switch {
		case (a.Accelerator == "") != (b.Accelerator == ""):
			if a.Accelerator == "" {
				return 1
			}
			return -1
		case a.Flavor != b.Flavor:
			if a.Flavor < b.Flavor {
				return -1
			}
			return 1
		case a.Resource < b.Resource:
			return -1
		case a.Resource > b.Resource:
			return 1
		}
		return 0
	})
	return out
}

// takeManaged takes, from the managed usage left in a queue, at most limit
// GPUs that row r's line holds: the types naming its accelerator, or, for an
// untyped line, the types no typed line of the queue names.
func takeManaged(left map[string]int, r config.ExternalBorrow, rows []config.ExternalBorrow, limit int) int {
	taken := 0
	for _, t := range slices.Sorted(maps.Keys(left)) {
		if taken >= limit {
			break
		}
		matches := accelerator.SameName(r.Accelerator, t)
		if r.Accelerator == "" {
			matches = !slices.ContainsFunc(rows, func(o config.ExternalBorrow) bool {
				return o.Accelerator != "" && accelerator.SameName(o.Accelerator, t)
			})
		}
		if !matches {
			continue
		}
		n := min(left[t], limit-taken)
		left[t] -= n
		taken += n
	}
	return taken
}

// addTypedCharge adds n under the key of caps naming the same accelerator as
// accType, or under accType itself.
func addTypedCharge(caps map[string]int, accType string, n int) map[string]int {
	if caps == nil {
		caps = map[string]int{}
	}
	key := accType
	if k, ok := accelerator.FindKey(caps, accType); ok {
		key = k
	}
	caps[key] += n
	return caps
}

// largestCharge is, per type, the largest of the queues' charges, and the
// largest untyped one.
func largestCharge(cs []config.ExternalCaps) config.ExternalCaps {
	var out config.ExternalCaps
	for _, c := range cs {
		for t, n := range c.ByType {
			k, ok := accelerator.FindKey(out.ByType, t)
			if !ok {
				out.ByType = addTypedCharge(out.ByType, t, n)
				continue
			}
			out.ByType[k] = max(out.ByType[k], n)
		}
		if c.HasUntyped {
			out.Untyped, out.HasUntyped = max(out.Untyped, c.Untyped), true
		}
	}
	return out
}

// chargeCaps returns caps with each namespace's grant less its charge, each
// figure clamped at 0. A namespace with no charge is left as is.
func chargeCaps(caps map[string]config.ExternalCaps, charge map[string]config.ExternalCaps) map[string]config.ExternalCaps {
	if caps == nil {
		return nil
	}
	out := make(map[string]config.ExternalCaps, len(caps))
	for ns, c := range caps {
		ch, ok := charge[ns]
		if !ok {
			out[ns] = c
			continue
		}
		c.ByType = maps.Clone(c.ByType)
		for t, n := range c.ByType {
			if k, found := accelerator.FindKey(ch.ByType, t); found {
				c.ByType[t] = max(0, n-ch.ByType[k])
			}
		}
		if c.HasUntyped && ch.HasUntyped {
			c.Untyped = max(0, c.Untyped-ch.Untyped)
		}
		out[ns] = c
	}
	return out
}

package kueue

import (
	"cmp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
)

// Reason says why borrowing is not counted for a queue, or qualifies its
// guaranteed figure. The empty Reason means borrowing is counted.
type Reason string

// Reasons a queue's borrowable figure is 0, and the one that qualifies its
// guaranteed figure.
const (
	// ReasonStale: the snapshot is not fresh; the last read failed.
	ReasonStale Reason = "stale"
	// ReasonNoStatus: a queue in the cohort has no reservation status, so its
	// usage is unknown. Kueue CRDs installed without a running Kueue look so.
	ReasonNoStatus Reason = "no-status"
	// ReasonHierarchicalCohort: the cohort has a parent, its own
	// resourceGroups, or is named as a parent by another Cohort.
	ReasonHierarchicalCohort Reason = "hierarchical-cohort"
	// ReasonHierarchyUnknown: the Cohort objects could not be listed, so a flat
	// cohort cannot be told apart from a nested one.
	ReasonHierarchyUnknown Reason = "hierarchy-unknown"
	// ReasonEffectiveQuotas: a queue in the cohort carries
	// status.effectiveQuotas, which Kueue then schedules from instead of spec.
	ReasonEffectiveQuotas Reason = "effective-quotas"
	// ReasonFairSharing: Kueue Fair Sharing is on (status.fairSharing).
	ReasonFairSharing Reason = "fair-sharing"
	// ReasonNoCohort: the queue is in no cohort, so there is nothing to borrow.
	ReasonNoCohort Reason = "no-cohort"
	// ReasonInactive: the queue admits nothing (a stopPolicy holds it, or it
	// names a flavor the cluster lacks).
	ReasonInactive Reason = "inactive"
	// ReasonMalformed: a borrowing field of a queue in the cohort did not parse.
	ReasonMalformed Reason = "malformed"
	// ReasonTopologyAware: a queue in the cohort declares a GPU resource of a
	// flavor with Topology-Aware Scheduling on (spec.topologyName). Kueue then
	// places the pods itself, by topology, which the node fit here does not
	// model.
	ReasonTopologyAware Reason = "topology-aware"
	// ReasonAdmissionChecks: a queue in the cohort admits only after admission
	// checks (spec.admissionChecks or spec.admissionChecksStrategy), MultiKueue
	// included. Quota reserved is then not admission, and a check may send the
	// workload to another cluster.
	ReasonAdmissionChecks Reason = "admission-checks"
	// ReasonKueueVersionUntested: the Kueue controller runs a version outside
	// TestedKueueVersions, so the fields and semantics borrowing relies on
	// (kueue_constants.go) are not known to hold. The nominal quotas still
	// apply.
	ReasonKueueVersionUntested Reason = "kueue-version-untested"
	// ReasonKueueControllerDown: no Kueue leader is known to run -- its leader
	// Lease is not renewed within twice its duration, or without a Lease its
	// Deployment has no ready replica, or neither can be found or read
	// (Forbidden included) -- while Kueue's API still answers
	// (ControllerVersion.Down). Nothing admits or preempts, and queue status
	// stops moving, so no new borrowing is counted; what is borrowed is frozen,
	// not shrunk. Decided on the engine side from
	// config.ExternalQuotas.KueueControllerDown, not carried on the rows.
	ReasonKueueControllerDown Reason = "kueue-controller-down"
	// ReasonNominalLentOut qualifies a guaranteed figure below nominal: the
	// queue cannot reclaim, and a peer holds the rest of its nominal quota.
	ReasonNominalLentOut Reason = "nominal-lent-out"
)

// Reasons decided per namespace on the engine side, from the namespace's own
// pods and ResourceQuotas rather than from the Kueue read (B1).
const (
	// ReasonNotKueueManaged: a GPU pod in the namespace is not managed by
	// Kueue, so Kueue's usage misses GPUs serving holds and the cohort looks
	// idler than it is. Also the first answer for a namespace with no GPU pods.
	ReasonNotKueueManaged Reason = "not-kueue-managed"
	// ReasonNoGPUBound: nothing checks that borrowed GPUs exist: no physical
	// limiter, and no namespace ResourceQuota on a GPU resource at most
	// nominal plus borrowingLimit.
	ReasonNoGPUBound Reason = "no-gpu-bound"
	// ReasonResourceQuotaUnreadable: the namespace's ResourceQuotas could not
	// be read. Never read as "no ResourceQuota".
	ReasonResourceQuotaUnreadable Reason = "resourcequota-unreadable"
	// ReasonNotSelected: the quota entry's kueue.borrowNamespaces does not
	// list the namespace, so its borrowing is not counted (a pilot).
	ReasonNotSelected Reason = "not-selected"
	// ReasonLenderCannotReclaim: the cohort's free pool is lent by queues
	// Kueue would not reclaim for -- reclaimWithinCohort Never, or
	// LowerPriority below every borrowing Workload -- and only by them. What
	// such a lender lends is not counted (it could never get it back), so the
	// pool counted is 0 although Kueue's is not.
	ReasonLenderCannotReclaim Reason = "lender-cannot-reclaim"
)

// ResourceKey names one GPU resource of one flavor.
type ResourceKey struct {
	Flavor   string
	Resource string
}

// QueueResource is one ClusterQueue's quota and reservation for one flavor and
// GPU resource. Figures are whole GPUs.
type QueueResource struct {
	// Nominal is spec nominalQuota.
	Nominal int
	// LendingLimit is spec lendingLimit; nil when unset (everything is lendable).
	LendingLimit *int
	// BorrowingLimit is spec borrowingLimit; nil when unset (no limit).
	BorrowingLimit *int
	// Reserved is status.flavorsReservation total: what Kueue has charged.
	Reserved int
	// Borrowed is status.flavorsReservation borrowed: Kueue's own figure, kept
	// as a cross-check on the computed one.
	Borrowed int
}

// QueueState is everything about one ClusterQueue that borrowing depends on.
type QueueState struct {
	Name string
	// Cohort is spec.cohortName (spec.cohort in v1beta1); empty for none.
	Cohort string
	// Reclaim is spec.preemption.reclaimWithinCohort; ReclaimNever when absent.
	Reclaim string
	// Pending is status.pendingWorkloads: every pending Workload, whatever it
	// asks for. Kept for the log; the lender signal reads PendingWorkloads.
	Pending int
	// PendingWorkloads are the queue's pending Workloads that request a GPU
	// resource, read from the Workloads themselves (readPending).
	PendingWorkloads []PendingWorkload
	// ReservedPriority is the highest spec.priority of a Workload Kueue has
	// reserved quota for in the queue; nil when none was read.
	ReservedPriority *int32
	// HasStatus is false when Kueue never wrote status.flavorsReservation.
	HasStatus bool
	// FairSharing is true when status.fairSharing is present.
	FairSharing bool
	// EffectiveQuotas is true when status.effectiveQuotas is present.
	EffectiveQuotas bool
	// Inactive is true for a queue Kueue admits nothing through.
	Inactive bool
	// Malformed is true when a borrowing field failed to parse.
	Malformed bool
	// AdmissionChecks is true when the queue names admission checks, in
	// spec.admissionChecks or spec.admissionChecksStrategy.
	AdmissionChecks bool
	// BorrowWithinCohort is true when spec.preemption.borrowWithinCohort sets
	// a policy other than Never: Kueue may preempt in the cohort to borrow.
	BorrowWithinCohort bool
	// PinRequired is true when a variant must pin a flavor's nodeLabels to be
	// counted (ReasonFlavorUnpinned): the queue offers more than one flavor for
	// one of its GPU resources, or its flavorFungibility.whenCanBorrow is
	// Borrow or MayStopSearch.
	PinRequired bool
	// Resources are the GPU resources the queue declares, per flavor.
	Resources map[ResourceKey]QueueResource
	// CoResources are the other resources (CPU, memory, ...) the resource
	// group of a GPU flavor covers, per flavor, in milli-units: Kueue admits a
	// Workload only when every covered resource fits.
	CoResources map[ResourceKey]CoQuota
}

// CoQuota is one ClusterQueue's quota and reservation of a non-GPU resource a
// GPU flavor's resource group covers, in milli-units (resource.Quantity's
// MilliValue), so CPU and memory share one scale.
type CoQuota struct {
	Nominal      int64
	LendingLimit *int64
	Reserved     int64
}

// lent is the co-resource's L and G, as QueueResource.lent.
func (c CoQuota) lent() (lent, kept int64) {
	lent = c.Nominal
	if c.LendingLimit != nil {
		lent = min(max(*c.LendingLimit, 0), c.Nominal)
	}
	return lent, c.Nominal - lent
}

// CanReclaim reports whether the queue may preempt borrowers to get its
// nominal quota back, whatever they run: only under ReclaimAny.
//
// ReclaimLowerPriority is read as ReclaimNever for the guaranteed and
// within-nominal figures: Kueue then preempts only borrowed workloads of lower
// priority than the one it admits, and with serving above batch (the
// documented setup) a batch lender never reclaims from serving. As a lender,
// whether Kueue reclaims for a LowerPriority queue depends on whose Workloads
// borrow, which the caller judges (LenderState.Priority).
func (q QueueState) CanReclaim() bool {
	return q.Reclaim == ReclaimAny
}

// highestPriority is the highest spec.priority among the queue's Workloads
// read: those Kueue reserved quota for, and its pending ones that request a GPU.
// Nil when none was read.
func (q QueueState) highestPriority() *int32 {
	var out *int32
	if q.ReservedPriority != nil {
		p := *q.ReservedPriority
		out = &p
	}
	for _, w := range q.PendingWorkloads {
		if out == nil || w.Priority > *out {
			p := w.Priority
			out = &p
		}
	}
	return out
}

// CohortState is one Cohort object. A cohort named by a ClusterQueue with no
// Cohort object behind it has none, and is flat.
type CohortState struct {
	Name string
	// Parent is spec.parentName.
	Parent string
	// HasResourceGroups is true when the Cohort declares quota of its own.
	HasResourceGroups bool
}

// FlavorPlacement is where a flavor's pods may run: Kueue pins them to its
// node labels and tolerations.
type FlavorPlacement struct {
	// AcceleratorType is the product named by NodeLabels; empty when none is.
	AcceleratorType string
	NodeLabels      map[string]string
	Tolerations     []corev1.Toleration
	// TopologyName is spec.topologyName: set, Kueue places the flavor's pods
	// by Topology-Aware Scheduling.
	TopologyName string
}

// Topology is one read of everything borrowing depends on.
type Topology struct {
	Queues  map[string]QueueState
	Cohorts map[string]CohortState
	// CohortsUnreadable is set when the Cohort objects could not be listed.
	// Never read as "no Cohort objects": that would call every cohort flat.
	CohortsUnreadable bool
	Flavors           map[string]FlavorPlacement
	// VersionUntested is set when the Kueue controller runs a version outside
	// TestedKueueVersions. An unknown version does not set it.
	VersionUntested bool
	// VersionUnknown is set when the controller's Deployment was read and its
	// image names no version, and kueue.assumeTestedVersion is not set.
	VersionUnknown bool
	// LendersUnreadable is set when the Workloads were not read cluster-wide,
	// or the read failed: no lender's wait can be seen.
	LendersUnreadable bool
}

// QueueFigures is what one ClusterQueue could reach for one flavor and GPU
// resource. Guaranteed plus Borrowable is the reach.
type QueueFigures struct {
	ResourceKey
	Nominal int
	// Guaranteed is the part of the reach that does not depend on the cohort.
	Guaranteed int
	// Borrowable is how far past Guaranteed the queue could reach; 0 whenever
	// Reason is set.
	Borrowable int
	// BorrowCap is the borrowing limit, config.QuotaUnlimited when unset.
	BorrowCap int
	// WithinNominal is what the queue could reach without borrowing (B7):
	// Nominal for a queue that can reclaim, min(N, max(U, G) + cohortFree) for
	// one that cannot. Nominal whenever Reason is set.
	WithinNominal int
	// LentOut is, for a counted queue that can reclaim, the part of its nominal
	// a peer may hold now: N - max(U, G). Growth into it is guaranteed but
	// debits the cohort's free pool (B3). 0 otherwise.
	LentOut int
	// CohortFree is the cohort's free pool for this key; 0 when withheld.
	CohortFree int
	// KueueBorrowed is Kueue's own borrowed figure, a cross-check.
	KueueBorrowed int
	// Reason is why Borrowable is 0; empty when borrowing is counted.
	Reason Reason
	// GuaranteedReason is ReasonNominalLentOut when Guaranteed is below
	// Nominal, empty otherwise.
	GuaranteedReason Reason
	// PinRequired is the queue's QueueState.PinRequired.
	PinRequired bool
}

// SharedPool is one cohort's free capacity of one flavor and GPU resource,
// spent once across every queue in the cohort.
type SharedPool struct {
	Cohort string
	ResourceKey
	FlavorPlacement
	// Free is the cohort's free pool; 0 when Reason is set.
	Free   int
	Reason Reason
	// WaitingLenders are the member queues that, in this read, wait for this
	// key (LenderWaiting). This is the raw per-read predicate; debouncing it
	// is the caller's.
	WaitingLenders []string
	// Lenders is every member queue that declares this key, inactive ones
	// included (they lend, and never wait), with whether Kueue would reclaim
	// for it and what it lends unused: what deciding whether its capacity may
	// be borrowed needs.
	Lenders []LenderState
	// PendingGPUs is what the pending Workloads of every member queue -- every
	// namespace of the cohort's ClusterQueues, not only the governed ones --
	// request of this key, on the flavors their node selection allows (B6):
	// demand Kueue may admit onto the free pool before anything new borrowed
	// here is (B8). Taken out of the pool by the engine
	// (allocation.DeductUnreserved).
	PendingGPUs int
	// CoFree is, per other resource the flavor's resource group covers (CPU,
	// memory, ...), the cohort's free pool of it in milli-units, by the same
	// formula as Free; 0 when Reason is set. A borrowed replica must fit every
	// one of them as well as the GPUs (B3, co-resources).
	CoFree map[string]int64
}

// LenderState is one cohort member as a lender of one flavor and GPU resource.
type LenderState struct {
	Queue string
	// Waiting is LenderWaiting in this read, not debounced.
	Waiting bool
	// CanReclaim is whether Kueue preempts borrowers for it whatever they run:
	// reclaimWithinCohort Any.
	CanReclaim bool
	// LentOut is the nominal it does not hold now: max(0, N - U), the most it
	// may be waiting for.
	LentOut int
	// Reclaim is its spec.preemption.reclaimWithinCohort: whether Kueue would
	// take back what it lends, judged against the borrowers' Workloads.
	Reclaim string
	// Unused is what it lends and does not use itself:
	// max(0, N - max(U, G)), its part of the cohort's free pool before what
	// the borrowers hold is taken off.
	Unused int
	// Priority is the highest spec.priority among its Workloads read, reserved
	// or pending: under LowerPriority, Kueue reclaims for it from a borrowed
	// Workload of lower priority. Nil when none was read.
	Priority *int32
}

// Borrowing is the result of ComputeBorrowing.
type Borrowing struct {
	// Queues holds per ClusterQueue the figures of each GPU resource it
	// declares, sorted by flavor and resource.
	Queues map[string][]QueueFigures
	// Pools holds one SharedPool per cohort, flavor and GPU resource, sorted.
	Pools []SharedPool
}

// LenderWaiting reports whether q, in this read, waits for capacity of key it
// lent: a pending Workload of it requests the key's GPU resource and fits
// within its unused nominal quota of key (lenderPending). A pending count
// alone is not enough: anyone who may create a Workload in a lender's
// namespace -- a CPU job, or one too large to admit -- would otherwise make
// the borrowers give capacity back for nothing.
func LenderWaiting(q QueueState, key ResourceKey) bool {
	return lenderPending(q, key) > 0
}

// LenderWaitingOn is LenderWaiting for a flavor whose nodeLabels are given: a
// pending Workload whose node selection rules the flavor out does not wait
// for it (B6). Unknown labels, or rules that do not say, count it.
func LenderWaitingOn(q QueueState, key ResourceKey, nodeLabels map[string]string) bool {
	return lenderPendingOn(q, key, nodeLabels) > 0
}

// lent returns L = min(lendingLimit, nominal), nominal when unset, and the kept
// part G = nominal - L.
func (r QueueResource) lent() (lent, kept int) {
	lent = r.Nominal
	if r.LendingLimit != nil {
		lent = min(max(*r.LendingLimit, 0), r.Nominal)
	}
	return lent, r.Nominal - lent
}

// unused is what the queue lends and does not use itself: its lent part L
// less its own usage past what it keeps, max(0, N - max(U, G)).
func (r QueueResource) unused() int {
	_, kept := r.lent()
	return max(0, r.Nominal-max(r.Reserved, kept))
}

// ComputeBorrowing derives, from one read, what each ClusterQueue could reach
// in its cohort. Per cohort, flavor and GPU resource, over every member m that
// declares it, inactive ones included (see cohortFree):
//
//	L_m = min(lendingLimit, N_m)   (unset: N_m)
//	G_m = N_m - L_m                kept by m, never lent
//	cohortFree = max(0, Σ L_m − Σ max(0, U_m − G_m))
//
// and for a queue Q, with base = max(U_Q, G_Q):
//
//	reach = min(N_Q + B_Q, base + cohortFree)        reclaimWithinCohort Never or LowerPriority
//	reach = max(N_Q, min(N_Q + B_Q, base + cohortFree))  reclaimWithinCohort Any
//
// Guaranteed is N_Q for a queue that can reclaim, min(N_Q, base) for one that
// cannot; Borrowable is reach minus guaranteed. Where borrowing cannot be
// counted the figures fall back to Guaranteed = N_Q, Borrowable = 0, with the
// reason. stale marks a snapshot that is not fresh.
func ComputeBorrowing(t Topology, stale bool) Borrowing {
	members := make(map[string][]QueueState)
	for _, q := range t.Queues {
		if q.Cohort != "" {
			members[q.Cohort] = append(members[q.Cohort], q)
		}
	}
	cohortReason := make(map[string]Reason, len(members))
	for cohort, qs := range members {
		cohortReason[cohort] = withheldReason(t, cohort, qs, stale)
	}

	out := Borrowing{Queues: make(map[string][]QueueFigures, len(t.Queues))}
	for name, q := range t.Queues {
		own := cohortReason[q.Cohort]
		if q.Cohort == "" {
			own = Precedence(globalReason(t, stale), ReasonNoCohort)
		}
		if q.Inactive {
			own = Precedence(own, ReasonInactive)
		}
		reason := own
		figs := make([]QueueFigures, 0, len(q.Resources))
		for key, r := range q.Resources {
			f := QueueFigures{
				ResourceKey:   key,
				Nominal:       r.Nominal,
				Guaranteed:    r.Nominal,
				WithinNominal: r.Nominal,
				BorrowCap:     config.QuotaUnlimited,
				KueueBorrowed: r.Borrowed,
				Reason:        reason,
				PinRequired:   q.PinRequired,
			}
			if r.BorrowingLimit != nil {
				f.BorrowCap = max(*r.BorrowingLimit, 0)
			}
			if reason == "" {
				f.CohortFree = cohortFree(members[q.Cohort], key)
				f.Guaranteed, f.Borrowable = reach(q, r, f.CohortFree)
				f.WithinNominal, f.LentOut = withinNominal(q, r, f.CohortFree)
				if f.Guaranteed < r.Nominal {
					f.GuaranteedReason = ReasonNominalLentOut
				}
			}
			figs = append(figs, f)
		}
		slices.SortFunc(figs, func(a, b QueueFigures) int { return compareKeys(a.ResourceKey, b.ResourceKey) })
		out.Queues[name] = figs
	}

	for cohort, qs := range members {
		keys := map[ResourceKey]bool{}
		for _, q := range qs {
			if q.Inactive {
				continue
			}
			for key := range q.Resources {
				keys[key] = true
			}
		}
		for key := range keys {
			p := SharedPool{Cohort: cohort, ResourceKey: key, FlavorPlacement: t.Flavors[key.Flavor], Reason: cohortReason[cohort]}
			if p.Reason == "" {
				p.Free = cohortFree(qs, key)
				p.CoFree = cohortCoFree(qs, key.Flavor)
			}
			for _, q := range qs {
				r, declares := q.Resources[key]
				if !declares {
					continue
				}
				// An inactive queue still lends (cohortFree counts it), but
				// never waits. A pending Workload counts only on the flavors
				// its node selection allows (B6).
				waiting := !q.Inactive && LenderWaitingOn(q, key, t.Flavors[key.Flavor].NodeLabels)
				if !q.Inactive {
					p.PendingGPUs = addCapped(p.PendingGPUs, pendingOn(q, key, t.Flavors[key.Flavor].NodeLabels))
				}
				if waiting {
					p.WaitingLenders = append(p.WaitingLenders, q.Name)
				}
				p.Lenders = append(p.Lenders, LenderState{Queue: q.Name, Waiting: waiting,
					CanReclaim: q.CanReclaim(), LentOut: max(0, r.Nominal-r.Reserved),
					Reclaim: q.Reclaim, Unused: r.unused(), Priority: q.highestPriority()})
			}
			slices.Sort(p.WaitingLenders)
			slices.SortFunc(p.Lenders, func(a, b LenderState) int { return strings.Compare(a.Queue, b.Queue) })
			out.Pools = append(out.Pools, p)
		}
	}
	slices.SortFunc(out.Pools, func(a, b SharedPool) int {
		return cmp.Or(strings.Compare(a.Cohort, b.Cohort), compareKeys(a.ResourceKey, b.ResourceKey))
	})
	return out
}

// globalReason is what withholds borrowing in every cohort of a read: the
// Workloads unreadable, the controller's version unknown or untested, a stale
// read. Precedence decides between them.
func globalReason(t Topology, stale bool) Reason {
	var rs []Reason
	if stale {
		rs = append(rs, ReasonStale)
	}
	if t.LendersUnreadable {
		rs = append(rs, ReasonLendersUnreadable)
	}
	if t.VersionUnknown {
		rs = append(rs, ReasonKueueVersionUnknown)
	}
	if t.VersionUntested {
		rs = append(rs, ReasonKueueVersionUntested)
	}
	return Precedence(rs...)
}

// withheldReason is why borrowing cannot be counted anywhere in a cohort, or
// empty. Every condition that holds is collected, and the one shown is the
// first in the B1 table order (Precedence).
func withheldReason(t Topology, cohort string, qs []QueueState, stale bool) Reason {
	rs := []Reason{globalReason(t, stale)}
	add := func(r Reason) { rs = append(rs, r) }
	if t.CohortsUnreadable {
		add(ReasonHierarchyUnknown)
	}
	if c, ok := t.Cohorts[cohort]; ok && (c.Parent != "" || c.HasResourceGroups) {
		add(ReasonHierarchicalCohort)
	}
	for _, c := range t.Cohorts {
		if c.Parent == cohort {
			// A root that looks flat while a child lends into it.
			add(ReasonHierarchicalCohort)
		}
	}
	for _, q := range qs {
		// Inactive members too: their usage counts in the free pool.
		if !q.HasStatus {
			add(ReasonNoStatus)
		}
		if q.EffectiveQuotas {
			add(ReasonEffectiveQuotas)
		}
		if q.FairSharing {
			add(ReasonFairSharing)
		}
		for key := range q.Resources {
			if t.Flavors[key.Flavor].TopologyName != "" {
				add(ReasonTopologyAware)
			}
		}
		if q.AdmissionChecks {
			add(ReasonAdmissionChecks)
		}
		if q.Malformed {
			add(ReasonMalformed)
		}
		if q.BorrowWithinCohort {
			add(ReasonBorrowWithinCohort)
		}
	}
	return Precedence(rs...)
}

// cohortCoFree is, per co-resource the members cover beside flavor's GPUs,
// max(0, Σ L_m − Σ max(0, U_m − G_m)) in milli-units, over every member that
// declares it: the same free pool as cohortFree, per co-resource. Nil when no
// member covers one.
func cohortCoFree(qs []QueueState, flavor string) map[string]int64 {
	type sums struct{ lent, used int64 }
	by := map[string]*sums{}
	for _, q := range qs {
		for key, c := range q.CoResources {
			if key.Flavor != flavor {
				continue
			}
			s := by[key.Resource]
			if s == nil {
				s = &sums{}
				by[key.Resource] = s
			}
			l, g := c.lent()
			s.lent += l
			s.used += max(0, c.Reserved-g)
		}
	}
	if len(by) == 0 {
		return nil
	}
	out := make(map[string]int64, len(by))
	for res, s := range by {
		out[res] = max(0, s.lent-s.used)
	}
	return out
}

// cohortFree is max(0, Σ L_m − Σ max(0, U_m − G_m)) over every member that
// declares key, inactive ones included. Kueue's cache accumulates every
// ClusterQueue into its cohort, and the scheduling snapshot clones the cohort's
// figures before it drops the inactive queues, so an inactive queue's lending
// and its usage both still count: a held queue keeps its admitted workloads
// running, and what they borrowed is not free. An inactive queue is left out
// only as a borrower (ReasonInactive) and as a lender that waits.
func cohortFree(qs []QueueState, key ResourceKey) int {
	var lent, used int64
	for _, q := range qs {
		r, ok := q.Resources[key]
		if !ok {
			continue
		}
		l, g := r.lent()
		lent += int64(l)
		used += int64(max(0, r.Reserved-g))
	}
	return clampQuota(max(0, lent-used))
}

// reach returns a counted queue's guaranteed and borrowable figures for one
// key, given the cohort's free pool.
func reach(q QueueState, r QueueResource, free int) (guaranteed, borrowable int) {
	_, kept := r.lent()
	base := max(r.Reserved, kept)
	top := addCapped(base, free)
	if r.BorrowingLimit != nil {
		top = min(top, addCapped(r.Nominal, max(*r.BorrowingLimit, 0)))
	}
	if q.CanReclaim() {
		return r.Nominal, max(top, r.Nominal) - r.Nominal
	}
	guaranteed = min(r.Nominal, base)
	return guaranteed, max(0, top-guaranteed)
}

// withinNominal returns what a counted queue could reach without borrowing, and
// the part of its nominal a peer may hold now (B3, B7).
//
// A queue that can reclaim reaches its nominal whatever its peers hold, since
// Kueue preempts them; the gap between what it keeps or uses and its nominal is
// lent out, and growing into it takes those GPUs back from the free pool. A
// queue that cannot reclaim reaches only what Kueue would admit now without
// borrowing: what it keeps or uses, plus the free pool, never past nominal.
// Using its guaranteed figure here instead would stop an idle queue on Kueue's
// default (Never) from ever waking.
func withinNominal(q QueueState, r QueueResource, free int) (within, lentOut int) {
	_, kept := r.lent()
	base := max(r.Reserved, kept)
	if q.CanReclaim() {
		return r.Nominal, max(0, r.Nominal-base)
	}
	return min(r.Nominal, addCapped(base, free)), 0
}

// BorrowDrift is, per ClusterQueue in a cohort whose reservation status Kueue
// has written, the GPUs by which the borrowed figure computed here (usage
// past nominal, per flavor and GPU resource) differs from Kueue's own
// status.flavorsReservation[].borrowed, summed over the queue's GPU resources
// as absolute differences. It is the conformance check of this package's
// reading of Kueue: 0 while the two agree (wva_kueue_borrow_drift_gpus, and an
// e2e assertion against a live Kueue).
func BorrowDrift(t Topology) map[string]int {
	out := map[string]int{}
	for name, q := range t.Queues {
		if q.Cohort == "" || !q.HasStatus {
			continue
		}
		drift := 0
		for _, r := range q.Resources {
			computed := max(0, r.Reserved-r.Nominal)
			drift = addCapped(drift, max(computed-r.Borrowed, r.Borrowed-computed))
		}
		out[name] = drift
	}
	return out
}

// compareKeys orders resource keys by flavor, then resource.
func compareKeys(a, b ResourceKey) int {
	return cmp.Or(strings.Compare(a.Flavor, b.Flavor), strings.Compare(a.Resource, b.Resource))
}

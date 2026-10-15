package allocation

import (
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/accelerator"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
)

// NodeView is one cycle's per-node picture of free GPUs, consumed in order: the
// optimizer debits every replica it orders from the node it fits, so the next
// replica, of this model or another, sees the hole that is left. A pool budget
// counts GPUs; a replica needs them on ONE node (per pod). Sixteen free GPUs as
// 4+4+8 hold one 8-GPU pod, not two.
//
// It is built whenever a physical limiter bounds the cycle (the physical
// provider was not skipped) and a fresh node snapshot exists. Without one -- no
// physical limiter, the provider skipped, no snapshot -- the view is nil and
// nothing is fitted: every method accepts a nil receiver, every placement then
// succeeds, and the copies are nil.
//
// It counts GPUs only. CPU or memory exhaustion on a node is not seen, nor
// node affinity beyond the accelerator: a node is a candidate for a pod when
// it carries the pod's accelerator, is schedulable
// (decision.NodeGPU.Unschedulable: cordoned, not Ready, or tainted for
// removal), and every NoSchedule or NoExecute taint it carries is tolerated by
// the pod's template (ReplicaShape.Tolerations). A pod is placed best-fit, in
// the smallest hole that holds it; the pods of a replica with a topology key
// (ReplicaShape.DomainKey: an exclusive-topology LeaderWorkerSet) are placed
// in one domain, the nodes sharing one value of that label.
//
// A NodeView is not safe for concurrent use: one optimizer pass, or one wake
// decision, owns it.
//
// What a node is -- its accelerator, capacity, whether it is schedulable, its
// taints and labels -- does not change within a cycle; only what is debited
// from it does. So the nodes are read once, by NewNodeView, and shared
// read-only by every Clone, and so is the list of nodes each accelerator is
// eligible for (eligible): a clone copies only its debits, which are
// sparse. On a cluster of thousands of nodes a tentative placement then costs
// the nodes it touched, not a copy of every node.
//
// Every debit is charged to an owner -- the model being placed, or a holder
// of GPUs whose pods hold no node yet -- so what one owner placed can be told
// apart from the rest.
type NodeView struct {
	// slots is the snapshot as built, in node-name order, shared by clones and
	// never written. A node is its index in it.
	slots []nodeSlot
	// debit is the GPUs this view placed on each node, on top of Used.
	debit map[int]int
	// elig caches eligible and largest, shared by clones (eligibility does not
	// depend on what is debited).
	elig *eligibleCache
	// owner is who a debit committed now is charged to, and owned what each
	// owner was charged per node. A clone (a tentative placement) records
	// nothing; adopting it charges the difference to the owner.
	owner string
	owned map[string]map[int]int
	// takenAt is when the node snapshot was taken.
	takenAt time.Time
	// layouts is where an unknown pod layout is logged once per variant.
	layouts *LayoutLog
}

// nodeSlot is what the view reads of one node.
type nodeSlot struct {
	name          string
	accelerator   string
	capacity      int
	used          int
	unschedulable bool
	// taints is the node's NoSchedule and NoExecute taints a pod must
	// tolerate (decision.NodeGPU.Taints).
	taints []corev1.Taint
	// labels is the snapshot's own label map, read for a topology domain and
	// never written (decision.NodeGPUStore.Read).
	labels map[string]string
}

// eligibleCache is the nodes, in order, eligible for one accelerator and set of
// tolerations, and the largest node of each accelerator, computed once per
// cycle.
type eligibleCache struct {
	mu      sync.Mutex
	nodes   map[string][]int
	largest map[string]int
}

// LayoutLog remembers the variants whose unknown pod layout a node view has
// logged, so each is logged once by its engine rather than once per cycle. An
// engine keeps one and hands it to every view it builds (WithLayoutLog); a view
// without one logs once per view.
type LayoutLog struct {
	seen sync.Map
}

// first reports whether variant is logged for the first time.
func (l *LayoutLog) first(variant string) bool {
	_, seen := l.seen.LoadOrStore(variant, true)
	return !seen
}

// NewNodeView builds a view over a node snapshot (decision.NodeGPUStore.Latest),
// or returns nil when there is none: without node information nothing is
// fitted. The snapshot's maps are not kept; its label maps are, read-only.
func NewNodeView(snap map[string]decision.NodeGPU) *NodeView {
	return newNodeViewAt(snap, time.Time{})
}

// newNodeViewAt is NewNodeView for a snapshot taken at takenAt.
func newNodeViewAt(snap map[string]decision.NodeGPU, takenAt time.Time) *NodeView {
	if snap == nil {
		return nil
	}
	v := &NodeView{slots: make([]nodeSlot, 0, len(snap)), debit: map[int]int{},
		elig: &eligibleCache{nodes: map[string][]int{}, largest: map[string]int{}}, owned: map[string]map[int]int{},
		takenAt: takenAt, layouts: &LayoutLog{}}
	for _, name := range slices.Sorted(maps.Keys(snap)) {
		n := snap[name]
		v.slots = append(v.slots, nodeSlot{name: name, accelerator: n.Accelerator, capacity: n.Capacity, used: n.Used,
			unschedulable: n.Unschedulable, taints: n.Taints, labels: n.Labels})
	}
	return v
}

// WithLayoutLog makes the view log an unknown pod layout once per variant in
// l, across every view that shares it, and returns v.
func (v *NodeView) WithLayoutLog(l *LayoutLog) *NodeView {
	if v != nil && l != nil {
		v.layouts = l
	}
	return v
}

// NodeViewFor is the node view for a decision bounded by constraints: built
// from store's snapshot when the physical limiter is among them, and nil
// otherwise. A physical limiter that was skipped this cycle (no node read, its
// usage unobserved, a failed refresh) is not among them, and its node fit is
// skipped with it; the skip is already reported as the provider's. reason is
// non-empty when the physical limiter bounds the decision but no node snapshot
// younger than decision.NodeGPUsMaxAge exists, so node fit is skipped: the
// caller reports it.
func NodeViewFor(constraints []*ResourceConstraints, store *decision.NodeGPUStore, now time.Time) (view *NodeView, reason string) {
	if !PhysicalBounds(constraints) {
		return nil, ""
	}
	if store == nil {
		store = decision.DefaultNodeGPUs
	}
	if !store.Read(decision.NodeGPUsMaxAge, now, func(snap map[string]decision.NodeGPU, at time.Time) {
		view = newNodeViewAt(snap, at)
	}) {
		return nil, "no node snapshot younger than " + decision.NodeGPUsMaxAge.String() +
			": replicas are fitted to the pool, not to nodes"
	}
	return view, ""
}

// PhysicalLimiterName is the name the physical limiter is built under: the
// provider label of its constraints.
const PhysicalLimiterName = "gpu-limiter"

// PhysicalBounds reports whether the physical limiter's constraints are among
// constraints.
func PhysicalBounds(constraints []*ResourceConstraints) bool {
	for _, c := range constraints {
		if c != nil && c.ProviderName == PhysicalLimiterName {
			return true
		}
	}
	return false
}

// ReplicaShape is one replica as PlaceReplicas fits it.
type ReplicaShape struct {
	// Variant is "namespace/name", for the unknown-layout log.
	Variant     string
	Accelerator string
	// PodGPUs is each pod's GPUs, leader first; nil when unknown.
	PodGPUs []int
	// GPUs is the replica's GPUs in all.
	GPUs int
	// Tolerations are the pod template's; a node with a NoSchedule or
	// NoExecute taint they do not tolerate holds none of the replica's pods.
	// A LeaderWorkerSet's workers are judged by its leader's.
	Tolerations []corev1.Toleration
	// DomainKey is the node label every pod of the replica must share a value
	// of (an exclusive-topology LeaderWorkerSet), "" for none.
	DomainKey string
}

// replicaFit is one replica as the view places it: its pods' GPUs, the
// accelerator, the tolerations and the topology key.
type replicaFit struct {
	acc         string
	pods        []int
	tolerations []corev1.Toleration
	domain      string
}

// fitOf is s as the view places it (replicaPods for its pods).
func (v *NodeView) fitOf(s ReplicaShape) replicaFit {
	return replicaFit{acc: s.Accelerator, pods: v.replicaPods(s.Variant, s.Accelerator, s.PodGPUs, s.GPUs),
		tolerations: s.Tolerations, domain: s.DomainKey}
}

// PlaceReplicas reports whether every replica fits the nodes together, each on
// what the ones before it left, and debits them all when commit is set and
// they do. A nil view fits everything.
func (v *NodeView) PlaceReplicas(reps []ReplicaShape, commit bool) bool {
	if v == nil {
		return true
	}
	trial := v.Clone()
	for _, r := range reps {
		if !trial.place(trial.fitOf(r)) {
			return false
		}
	}
	if commit {
		v.adopt(trial)
	}
	return true
}

// Clone returns an independent copy, for a pass that must not consume the
// cycle's view (a tentative placement). It copies only the debits; the nodes
// and the eligibility cache are shared, read-only. A clone records no owners:
// what it placed is charged when the view adopts it.
func (v *NodeView) Clone() *NodeView {
	if v == nil {
		return nil
	}
	return &NodeView{slots: v.slots, debit: maps.Clone(v.debit), elig: v.elig,
		takenAt: v.takenAt, layouts: v.layouts}
}

// adopt takes another view's debits: the commit of a tentative placement made
// on a Clone of v. What the clone added is charged to v's owner.
func (v *NodeView) adopt(c *NodeView) {
	if v == nil || c == nil {
		return
	}
	if v.owned != nil {
		for i, g := range c.debit {
			if d := g - v.debit[i]; d != 0 {
				v.record(i, d)
			}
		}
	}
	v.debit = c.debit
}

// charge debits g GPUs of node i, charged to the view's owner.
func (v *NodeView) charge(i, g int) {
	v.debit[i] += g
	if v.owned != nil {
		v.record(i, g)
	}
}

// record charges g GPUs of node i to the view's owner.
func (v *NodeView) record(i, g int) {
	perNode := v.owned[v.owner]
	if perNode == nil {
		perNode = map[int]int{}
		v.owned[v.owner] = perNode
	}
	perNode[i] += g
}

// setOwner charges every debit committed from now on to owner, and returns
// the previous owner.
func (v *NodeView) setOwner(owner string) string {
	if v == nil {
		return ""
	}
	prev := v.owner
	v.owner = owner
	return prev
}

// PromisedOwner is the owner the GPUs the utilization share promised to a
// receiver are charged to (ReservePromised).
const PromisedOwner = "promised"

// ReservePromised holds promised GPUs (scope -> accelerator -> GPUs, as
// decision.LatestSharePromised returns them) on the view's nodes, charged to
// PromisedOwner: a node freed and held for a receiver whose pod is still
// Pending looks free in the snapshot, and neither the optimizer nor a wake
// nor the warm pool may place into it. Which node is not known; Reserve takes
// the freest.
func ReservePromised(v *NodeView, promised map[string]map[string]int) {
	if v == nil {
		return
	}
	for _, scope := range slices.Sorted(maps.Keys(promised)) {
		for _, acc := range slices.Sorted(maps.Keys(promised[scope])) {
			v.Reserve(PromisedOwner, acc, promised[scope][acc])
		}
	}
}

// Reserve debits gpus GPUs of accelerator acc, charged to owner, from the
// nodes with the most free first, each in one piece: GPUs held for a receiver
// whose pods hold no node yet (promised by the utilization-share optimizer)
// are free on some node, which one is not known, and a freed whole node is
// where whole-node pods would land. Taken a GPU at a time across nodes, eight
// promised over two free 8-GPU nodes would leave four on each, and a
// genuinely free node would stop being placeable. Mirrors the share planner's
// own reserve (shareFreeNodes).
func (v *NodeView) Reserve(owner, acc string, gpus int) {
	if v == nil || gpus <= 0 {
		return
	}
	prev := v.setOwner(owner)
	defer v.setOwner(prev)
	eligible := v.eligible(acc, nil, true)
	for gpus > 0 {
		most, mostFree := -1, 0
		for _, i := range eligible {
			if f := v.free(i); f > mostFree {
				most, mostFree = i, f
			}
		}
		if most < 0 {
			return
		}
		take := min(mostFree, gpus)
		v.charge(most, take)
		gpus -= take
	}
}

// free is the GPUs of node i not used nor debited by this view.
func (v *NodeView) free(i int) int {
	n := &v.slots[i]
	f := n.capacity - n.used
	if len(v.debit) > 0 {
		f -= v.debit[i]
	}
	return max(0, f)
}

// eligible is, in order, the nodes a pod of accelerator acc with these
// tolerations may land on (anyTaint: whatever its taints), computed once per
// cycle, accelerator and set of tolerations.
func (v *NodeView) eligible(acc string, tolerations []corev1.Toleration, anyTaint bool) []int {
	key := eligibleKey(acc, tolerations, anyTaint)
	v.elig.mu.Lock()
	defer v.elig.mu.Unlock()
	if nodes, ok := v.elig.nodes[key]; ok {
		return nodes
	}
	var nodes []int
	for i := range v.slots {
		if eligibleNode(&v.slots[i], acc, tolerations, anyTaint) {
			nodes = append(nodes, i)
		}
	}
	v.elig.nodes[key] = nodes
	return nodes
}

// eligibleKey is eligible's cache key.
func eligibleKey(acc string, tolerations []corev1.Toleration, anyTaint bool) string {
	if anyTaint {
		return acc + "\x00*"
	}
	if len(tolerations) == 0 {
		return acc
	}
	var b strings.Builder
	b.WriteString(acc)
	for _, t := range tolerations {
		b.WriteString("\x00" + t.Key + "\x01" + string(t.Operator) + "\x01" + t.Value + "\x01" + string(t.Effect))
	}
	return b.String()
}

// eligibleNode reports whether a pod of accelerator acc may land on n: n is
// schedulable, carries acc (accelerator.SameName, so a family name lands on
// any of its products; a pod that names no accelerator lands on any GPU node),
// and, unless anyTaint, tolerations tolerate every taint n carries.
func eligibleNode(n *nodeSlot, acc string, tolerations []corev1.Toleration, anyTaint bool) bool {
	if n.unschedulable {
		return false
	}
	if constants.IsAcceleratorResolved(acc) && !accelerator.SameName(n.accelerator, acc) {
		return false
	}
	if anyTaint {
		return true
	}
	for i := range n.taints {
		if !slices.ContainsFunc(tolerations, func(t corev1.Toleration) bool { return t.ToleratesTaint(&n.taints[i]) }) {
			return false
		}
	}
	return true
}

// largest is the capacity of the largest node acc is eligible for, whatever
// its taints: how large a replica one node could hold.
func (v *NodeView) largest(acc string) int {
	v.elig.mu.Lock()
	most, ok := v.elig.largest[acc]
	v.elig.mu.Unlock()
	if ok {
		return most
	}
	for _, i := range v.eligible(acc, nil, true) {
		most = max(most, v.slots[i].capacity)
	}
	v.elig.mu.Lock()
	v.elig.largest[acc] = most
	v.elig.mu.Unlock()
	return most
}

// place fits one replica's pods onto the nodes they are eligible for, largest
// pod first, each best-fit -- into the smallest hole that holds it, the first
// such node in name order on a tie -- and debits them.
// With a topology key and more than one pod, every pod lands in one domain:
// the domains are tried in label-value order and the first that holds them
// all is used; a node without the label is in none. Reports whether every
// pod fit; nothing is debited otherwise.
func (v *NodeView) place(r replicaFit) bool {
	if v == nil {
		return true
	}
	pods := make([]int, 0, len(r.pods))
	for _, p := range r.pods {
		if p > 0 {
			pods = append(pods, p)
		}
	}
	if len(pods) == 0 {
		return true
	}
	if len(pods) > 1 {
		pods = largestFirst(pods)
	}
	eligible := v.eligible(r.acc, r.tolerations, false)
	domains := []string{""}
	if r.domain != "" && len(pods) > 1 {
		domains = v.domainsOf(eligible, r.domain)
	}
	for _, d := range domains {
		taken, ok := v.bestFit(eligible, pods, r.domain, d)
		if !ok {
			continue
		}
		for _, i := range slices.Sorted(maps.Keys(taken)) {
			v.charge(i, taken[i])
		}
		return true
	}
	return false
}

// bestFit places pods, in order, each into the smallest hole among eligible
// (only nodes whose label key is domain, when domain is set) that holds it,
// and returns the GPUs taken per node, or false when some pod fits nowhere.
func (v *NodeView) bestFit(eligible, pods []int, key, domain string) (map[int]int, bool) {
	var taken map[int]int
	for _, need := range pods {
		best, bestFree := -1, 0
		for _, i := range eligible {
			if domain != "" && v.slots[i].labels[key] != domain {
				continue
			}
			f := v.free(i) - taken[i]
			if f < need || (best >= 0 && f >= bestFree) {
				continue
			}
			best, bestFree = i, f
			if f == need {
				break // nothing fits tighter
			}
		}
		if best < 0 {
			return nil, false
		}
		if taken == nil {
			taken = map[int]int{}
		}
		taken[best] += need
	}
	return taken, true
}

// domainsOf is the values of label key among nodes, sorted.
func (v *NodeView) domainsOf(nodes []int, key string) []string {
	var out []string
	for _, i := range nodes {
		if d := v.slots[i].labels[key]; d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

// fits reports how many of up to n replicas r fit the nodes together, without
// debiting anything.
func (v *NodeView) fits(r replicaFit, n int) int {
	if v == nil || n <= 0 {
		return n
	}
	if len(r.pods) == 1 {
		// One pod per replica: whole pods per node, summed.
		need := r.pods[0]
		if need <= 0 {
			return n
		}
		count := 0
		for _, i := range v.eligible(r.acc, r.tolerations, false) {
			count += v.free(i) / need
			if count >= n {
				return n
			}
		}
		return count
	}
	trial := v.Clone()
	for i := range n {
		if !trial.place(r) {
			return i
		}
	}
	return n
}

// replicaPods is one replica's pods as GPUs per pod: the variant's measured
// layout when it sums to the replica, else one pod holding the whole replica.
//
// A replica whose layout is unknown and whose GPUs no single node of its
// accelerator holds (a multi-node group, a LeaderWorkerSet whose layout was not
// read) is not judged per node at all: nil, and only the pool budget bounds
// it. Placing it as one pod would hold it back on every cluster, which is the
// opposite of what an unknown layout says. It is logged once per variant
// (variant is "namespace/name") in the view's LayoutLog.
func (v *NodeView) replicaPods(variant, acc string, podGPUs []int, gpusPerReplica int) []int {
	sum := 0
	for _, g := range podGPUs {
		sum += g
	}
	if len(podGPUs) > 0 && sum == gpusPerReplica {
		return podGPUs
	}
	if v != nil && gpusPerReplica > v.largest(acc) {
		if v.layouts.first(variant) {
			ctrl.Log.WithName("allocation").V(1).Info(
				"Pod layout of a replica unknown and larger than any node: not fitting it to nodes",
				"variant", variant, "gpusPerReplica", gpusPerReplica, "podGPUs", podGPUs)
		}
		return nil
	}
	return []int{gpusPerReplica}
}

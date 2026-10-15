package kueue

import (
	"context"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// PendingWorkload is a Workload waiting in a ClusterQueue: no quota reserved,
// active, not finished. Only what the lender-waiting signal needs.
type PendingWorkload struct {
	// Priority is spec.priority; 0 when unset.
	Priority int32
	// GPUs is, per GPU resource name, what the Workload requests in all: per
	// pod set, its count times what one pod requests.
	GPUs map[string]int64
	// Requests is, per resource name other than the GPUs, what it requests in
	// all, in milli-units: the co-resources a flavor's resource group covers
	// must fit too.
	Requests map[string]int64
	// Placements are the node selection rules of its pod sets that request a
	// GPU, one per pod set: a flavor whose nodeLabels one of them rules out is
	// not one the Workload waits for (B6).
	Placements []WorkloadPlacement
}

// WorkloadPlacement is one pod set's node selection: spec.nodeSelector and the
// required node affinity.
type WorkloadPlacement struct {
	NodeSelector map[string]string
	Required     *corev1.NodeSelector
}

// allows reports whether a node carrying nodeLabels (and any other labels)
// could satisfy p. Only what nodeLabels says decides: a rule on a key the
// labels do not name could hold on such a node, so it does not rule the flavor
// out -- the cautious side, which counts the Workload as waiting.
func (p WorkloadPlacement) allows(nodeLabels map[string]string) bool {
	for k, v := range p.NodeSelector {
		if have, ok := nodeLabels[k]; ok && have != v {
			return false
		}
	}
	if p.Required == nil || len(p.Required.NodeSelectorTerms) == 0 {
		return true
	}
	// Terms are ORed: one that does not conflict is enough.
	for _, term := range p.Required.NodeSelectorTerms {
		if !termConflicts(term, nodeLabels) {
			return true
		}
	}
	return false
}

// termConflicts reports whether a node selector term (its expressions ANDed)
// fails on every node carrying nodeLabels: an expression on a key the labels
// name that the label's value does not satisfy.
func termConflicts(term corev1.NodeSelectorTerm, nodeLabels map[string]string) bool {
	for _, e := range term.MatchExpressions {
		v, ok := nodeLabels[e.Key]
		if !ok {
			continue
		}
		switch e.Operator {
		case corev1.NodeSelectorOpIn:
			if !slices.Contains(e.Values, v) {
				return true
			}
		case corev1.NodeSelectorOpNotIn:
			if slices.Contains(e.Values, v) {
				return true
			}
		case corev1.NodeSelectorOpDoesNotExist:
			return true
		}
	}
	return false
}

// allowsFlavor reports whether every GPU pod set of w may run on a flavor
// pinned to nodeLabels; true when the labels are unknown (nil).
func (w PendingWorkload) allowsFlavor(nodeLabels map[string]string) bool {
	if len(nodeLabels) == 0 {
		return true
	}
	for _, p := range w.Placements {
		if !p.allows(nodeLabels) {
			return false
		}
	}
	return true
}

// pendingRead is what one read of the Workloads gave the lender signal and
// the reclaim judgement.
type pendingRead struct {
	// byQueue holds each ClusterQueue's pending Workloads.
	byQueue map[string][]PendingWorkload
	// lenderPriority is, per ClusterQueue, the lowest spec.priority of its
	// unfinished Workloads that request a GPU, pending or reserved
	// (QueueState.LenderPriority).
	lenderPriority map[string]int32
	// classPriority is, per priority class (workloadClass), the highest
	// spec.priority of an unfinished GPU Workload of that class.
	classPriority map[string]int32
	// highestByNamespace is, per namespace, the highest spec.priority of its
	// unfinished GPU Workloads.
	highestByNamespace map[string]int32
}

// readPending reads the Workloads for the lender-waiting signal and the
// reclaim judgement, cluster-wide whatever the install's scope: the pending
// ones of each ClusterQueue, attributed through the LocalQueue spec.queueName
// names in the Workload's namespace, and the priorities a LowerPriority lender
// is judged by. A Finished Workload is skipped: it keeps status.admission, and
// it is neither a lender's demand nor a borrower.
//
// A namespace-scoped install lists its nominal grant's LocalQueues in its own
// namespace only (Options.Namespace), so the LocalQueues every pending
// Workload is attributed through are listed again here, cluster-wide. The
// prerequisites grant both reads (components/kueue-reader).
//
// A read that fails, or is Forbidden, reports unreadable: a lender's pending
// Workload would go unseen, so borrowing is withheld in every cohort
// (ReasonLendersUnreadable). Forbidden is not absent. The failure is logged
// once until a read passes. Callers must hold r.mu.
func (r *Reader) readPending(ctx context.Context, localQueues []unstructured.Unstructured) (out pendingRead, unreadable bool) {
	out = pendingRead{byQueue: map[string][]PendingWorkload{}, lenderPriority: map[string]int32{},
		classPriority: map[string]int32{}, highestByNamespace: map[string]int32{}}
	fail := func(err error) (pendingRead, bool) {
		if !r.warnedPending && ctx.Err() == nil {
			r.warnedPending = true
			kueueLogger(ctx).Info("Cannot read Kueue Workloads, or the LocalQueues they queue through, cluster-wide; cohort borrowing is withheld (reason lenders-unreadable) until they can be read. Logged once until a read passes.",
				"error", err.Error(),
				"remedy", "grant get, list and watch on workloads and get and list on localqueues.kueue.x-k8s.io cluster-wide (config/components/kueue-reader: an admin re-runs make setup-prereqs)")
		}
		return out, true
	}
	if r.opts.Namespace != "" {
		all, err := r.list(ctx, KindLocalQueue)
		if err != nil {
			return fail(err)
		}
		localQueues = all
	}
	src := r.opts.Workloads
	if src == nil {
		src = WorkloadsVia(r.client)
	}
	items, err := src.Workloads(ctx, "", "")
	if err != nil {
		return fail(err)
	}
	r.warnedPending = false
	queueOf := make(map[[2]string]string, len(localQueues))
	for i := range localQueues {
		cq, _, _ := unstructured.NestedString(localQueues[i].Object, "spec", "clusterQueue")
		queueOf[[2]string{localQueues[i].GetNamespace(), localQueues[i].GetName()}] = cq
	}
	lowest := func(m map[string]int32, k string, p int32) {
		if have, ok := m[k]; !ok || p < have {
			m[k] = p
		}
	}
	highest := func(m map[string]int32, k string, p int32) {
		if have, ok := m[k]; !ok || p > have {
			m[k] = p
		}
	}
	for i := range items {
		w := &items[i]
		if workloadFinished(w) {
			continue
		}
		sets := decodePodSets(w)
		gpus := gpusByResource(sets, r.gpuNames)
		if len(gpus) == 0 {
			continue // no GPU borrower, and no GPU demand a lender waits on
		}
		priority := workloadPriority(w)
		highest(out.classPriority, workloadClass(w), priority)
		highest(out.highestByNamespace, w.GetNamespace(), priority)
		if workloadReserved(w) {
			if cq, _, _ := unstructured.NestedString(w.Object, "status", "admission", "clusterQueue"); cq != "" {
				lowest(out.lenderPriority, cq, priority)
			}
			continue
		}
		if !workloadActive(w) {
			continue
		}
		lq, _, _ := unstructured.NestedString(w.Object, "spec", "queueName")
		if cq := queueOf[[2]string{w.GetNamespace(), lq}]; cq != "" {
			lowest(out.lenderPriority, cq, priority)
			requests, placements := workloadShape(sets, r.gpuNames)
			out.byQueue[cq] = append(out.byQueue[cq], PendingWorkload{Priority: priority, GPUs: gpus,
				Requests: requests, Placements: placements})
		}
	}
	return out, false
}

// podSet is one entry of a Workload's spec.podSets, decoded once.
type podSet struct {
	name string
	// count is spec.podSets[].count, 1 when unset, never negative.
	count int64
	spec  corev1.PodSpec
}

// decodePodSets decodes a Workload's pod sets. One whose template does not
// parse is left out: it counts nothing.
func decodePodSets(w *unstructured.Unstructured) []podSet {
	raw, _, _ := unstructured.NestedSlice(w.Object, "spec", "podSets")
	out := make([]podSet, 0, len(raw))
	for _, r := range raw {
		ps, _ := r.(map[string]interface{})
		spec, _, _ := unstructured.NestedMap(ps, "template", "spec")
		var pod corev1.PodSpec
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(spec, &pod); err != nil {
			continue
		}
		count, found, err := unstructured.NestedInt64(ps, "count")
		if !found || err != nil {
			count = 1
		}
		name, _, _ := unstructured.NestedString(ps, "name")
		out = append(out, podSet{name: name, count: max(count, 0), spec: pod})
	}
	return out
}

// gpusByResource is what pod sets request of each named GPU resource: per pod
// set, its count times what one pod requests (podGPUs). Resources not
// requested are absent; nil when none is.
func gpusByResource(sets []podSet, gpuNames map[string]bool) map[string]int64 {
	var out map[string]int64
	for _, s := range sets {
		for name := range gpuNames {
			n := s.count * podGPUs(&s.spec, map[string]bool{name: true})
			if n <= 0 {
				continue
			}
			if out == nil {
				out = map[string]int64{}
			}
			out[name] += n
		}
	}
	return out
}

// workloadShape is what pod sets request of every resource that is not a GPU,
// in milli-units over all of them (count times one pod), and the node
// selection of each pod set that requests a GPU.
func workloadShape(sets []podSet, gpuNames map[string]bool) (map[string]int64, []WorkloadPlacement) {
	var requests map[string]int64
	var placements []WorkloadPlacement
	for _, s := range sets {
		for name, milli := range PodRequestsMilli(&s.spec) {
			if gpuNames[name] {
				continue
			}
			if requests == nil {
				requests = map[string]int64{}
			}
			requests[name] += s.count * milli
		}
		if podGPUs(&s.spec, gpuNames) > 0 {
			p := WorkloadPlacement{NodeSelector: s.spec.NodeSelector}
			if a := s.spec.Affinity; a != nil && a.NodeAffinity != nil {
				p.Required = a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
			}
			placements = append(placements, p)
		}
	}
	return requests, placements
}

// PodRequestsMilli is what one pod of spec requests of each resource, in
// milli-units, as the scheduler and Kueue account it: regular containers add
// up, an init container counts only when larger, and a container that sets
// only a limit requests the limit. A resource no container names is absent.
func PodRequestsMilli(spec *corev1.PodSpec) map[string]int64 {
	of := func(c *corev1.Container, into map[string]int64) {
		for name, q := range c.Resources.Limits {
			if _, ok := c.Resources.Requests[name]; !ok {
				into[string(name)] += q.MilliValue()
			}
		}
		for name, q := range c.Resources.Requests {
			into[string(name)] += q.MilliValue()
		}
	}
	regular := map[string]int64{}
	for i := range spec.Containers {
		of(&spec.Containers[i], regular)
	}
	for i := range spec.InitContainers {
		one := map[string]int64{}
		of(&spec.InitContainers[i], one)
		for name, n := range one {
			regular[name] = max(regular[name], n)
		}
	}
	if len(regular) == 0 {
		return nil
	}
	return regular
}

// workloadPriority is spec.priority, 0 when unset.
func workloadPriority(w *unstructured.Unstructured) int32 {
	p, _, _ := unstructured.NestedInt64(w.Object, "spec", "priority")
	return int32(p)
}

// workloadClass is the priority class a Workload runs at, as
// "<group>/<name>" (TemplatePriorityClass spells a pod template's the same
// way): v1beta2 spec.priorityClassRef, or v1beta1 spec.priorityClassName with
// spec.priorityClassSource. PriorityClassNone when it names none.
func workloadClass(w *unstructured.Unstructured) string {
	if name, _, _ := unstructured.NestedString(w.Object, "spec", "priorityClassRef", "name"); name != "" {
		group, _, _ := unstructured.NestedString(w.Object, "spec", "priorityClassRef", "group")
		return group + "/" + name
	}
	name, _, _ := unstructured.NestedString(w.Object, "spec", "priorityClassName")
	if name == "" {
		return PriorityClassNone
	}
	if source, _, _ := unstructured.NestedString(w.Object, "spec", "priorityClassSource"); source == WorkloadPriorityClassSource {
		return WorkloadPriorityClassGroup + "/" + name
	}
	return PodPriorityClassGroup + "/" + name
}

// TemplatePriorityClass is the priority class Kueue gives the Workload of a
// pod from template t, spelled as workloadClass spells a Workload's: the
// WorkloadPriorityClass its priority-class label names, else its pod
// priorityClassName, else PriorityClassNone. Empty for a nil template: not
// known. A class set by a label on the scale target object alone (a
// LeaderWorkerSet's own labels) is not in the template; the caller reads
// PriorityClassNone cautiously for that reason.
func TemplatePriorityClass(t *corev1.PodTemplateSpec) string {
	switch {
	case t == nil:
		return ""
	case t.Labels[PriorityClassLabel] != "":
		return WorkloadPriorityClassGroup + "/" + t.Labels[PriorityClassLabel]
	case t.Spec.PriorityClassName != "":
		return PodPriorityClassGroup + "/" + t.Spec.PriorityClassName
	}
	return PriorityClassNone
}

// workloadFinished reports whether a Workload carries condition Finished=True.
func workloadFinished(w *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(w.Object, "status", "conditions")
	for _, raw := range conds {
		cond, _ := raw.(map[string]interface{})
		if cond["type"] == WorkloadFinished && cond["status"] == string(corev1.ConditionTrue) {
			return true
		}
	}
	return false
}

// workloadActive reports whether a Workload is active: spec.active not false.
func workloadActive(w *unstructured.Unstructured) bool {
	active, found, _ := unstructured.NestedBool(w.Object, "spec", "active")
	return !found || active
}

// lenderPending is what a lender waits for of one flavor and GPU resource:
// the largest GPU request among its pending Workloads that ask for the
// resource and fit within its unused nominal quota of key (Nominal minus
// Reserved). A pending Workload has no flavor assigned yet, so it counts for
// every flavor of the queue that offers the resource. 0 when none qualifies:
// the lender does not wait.
//
// A Workload asking for more than the unused nominal would have to borrow
// itself, and one asking for another resource (CPU work, another vendor's
// GPUs) gains nothing from capacity handed back, so neither makes a lender
// wait.
func lenderPending(q QueueState, key ResourceKey) (gpus int) {
	return lenderPendingOn(q, key, nil)
}

// lenderPendingOn is lenderPending for a flavor pinned to nodeLabels: a pending
// Workload counts only when its node selection allows the flavor (B6; unknown
// labels or rules count it), and only when it fits the lender's unused nominal
// of every co-resource the flavor's resource group covers as well (B1): one
// held back by its CPU or memory gains nothing from GPUs handed back.
func lenderPendingOn(q QueueState, key ResourceKey, nodeLabels map[string]string) (gpus int) {
	r, ok := q.Resources[key]
	if !ok {
		return 0
	}
	unused := int64(max(0, r.Nominal-r.Reserved))
	for _, w := range q.PendingWorkloads {
		n := w.GPUs[key.Resource]
		if n <= 0 || n > unused || !w.allowsFlavor(nodeLabels) || !fitsCoResources(q, key.Flavor, w) {
			continue
		}
		gpus = max(gpus, clampQuota(n))
	}
	return gpus
}

// pendingOn is what q's pending Workloads request of key's GPU resource in
// all, counting only those whose node selection allows a flavor pinned to
// nodeLabels (unknown counts every one).
func pendingOn(q QueueState, key ResourceKey, nodeLabels map[string]string) int {
	if _, ok := q.Resources[key]; !ok {
		return 0
	}
	var sum int64
	for _, w := range q.PendingWorkloads {
		if n := w.GPUs[key.Resource]; n > 0 && w.allowsFlavor(nodeLabels) {
			sum += n
		}
	}
	return clampQuota(sum)
}

// fitsCoResources reports whether w's request of each co-resource q covers
// beside flavor's GPUs fits q's unused nominal of it.
func fitsCoResources(q QueueState, flavor string, w PendingWorkload) bool {
	for key, c := range q.CoResources {
		if key.Flavor != flavor {
			continue
		}
		if w.Requests[key.Resource] > max(0, c.Nominal-c.Reserved) {
			return false
		}
	}
	return true
}

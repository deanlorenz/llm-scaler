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

// pendingRead is what one read of the Workloads gave the lender signal.
type pendingRead struct {
	// byQueue holds each ClusterQueue's pending Workloads.
	byQueue map[string][]PendingWorkload
	// reservedPriority is, per ClusterQueue, the highest spec.priority of a
	// Workload Kueue reserved quota for in it.
	reservedPriority map[string]int32
	// lowestByNamespace is, per namespace, the lowest spec.priority of its
	// Workloads read that request a GPU, reserved or pending.
	lowestByNamespace map[string]int32
}

// readPending reads the Workloads for the lender-waiting signal: the pending
// ones of each ClusterQueue, attributed through the LocalQueue spec.queueName
// names in the Workload's namespace, and the highest priority Kueue has
// reserved quota for per ClusterQueue (status.admission.clusterQueue). It
// reads every namespace the source can see, or only opts.Namespace for a
// namespace-scoped controller, whose LocalQueues are listed there only.
//
// A read that is not cluster-wide (a namespace-scoped controller sees its own
// namespace only), fails, or is Forbidden reports unreadable: a lender's
// pending Workload would go unseen, so borrowing is withheld in every cohort
// (ReasonLendersUnreadable). Forbidden is not absent. The failure is logged
// once until a read passes. Callers must hold r.mu.
func (r *Reader) readPending(ctx context.Context, localQueues []unstructured.Unstructured) (out pendingRead, unreadable bool) {
	out = pendingRead{byQueue: map[string][]PendingWorkload{}, reservedPriority: map[string]int32{},
		lowestByNamespace: map[string]int32{}}
	lowest := func(ns string, p int32) {
		if have, ok := out.lowestByNamespace[ns]; !ok || p < have {
			out.lowestByNamespace[ns] = p
		}
	}
	if r.opts.Namespace != "" {
		// A namespace-scoped controller lists Workloads (and LocalQueues) in
		// its own namespace only: every other namespace's lender is unseen.
		if !r.warnedPending {
			r.warnedPending = true
			kueueLogger(ctx).Info("Kueue Workloads are read in one namespace only (a namespace-scoped install); a lender waiting in another namespace cannot be seen, so cohort borrowing is withheld (reason lenders-unreadable)",
				"namespace", r.opts.Namespace)
		}
		return out, true
	}
	src := r.opts.Workloads
	if src == nil {
		src = WorkloadsVia(r.client)
	}
	items, err := src.Workloads(ctx, "", "")
	if err != nil {
		if !r.warnedPending && ctx.Err() == nil {
			r.warnedPending = true
			kueueLogger(ctx).Info("Cannot read Kueue Workloads cluster-wide; cohort borrowing is withheld (reason lenders-unreadable) until they can be read. Logged once until a read passes.",
				"error", err.Error())
		}
		return out, true
	}
	r.warnedPending = false
	queueOf := make(map[[2]string]string, len(localQueues))
	for i := range localQueues {
		cq, _, _ := unstructured.NestedString(localQueues[i].Object, "spec", "clusterQueue")
		queueOf[[2]string{localQueues[i].GetNamespace(), localQueues[i].GetName()}] = cq
	}
	for i := range items {
		w := &items[i]
		priority := workloadPriority(w)
		if workloadReserved(w) {
			if len(workloadGPUsByResource(w, r.gpuNames)) > 0 {
				lowest(w.GetNamespace(), priority)
			}
			if cq, _, _ := unstructured.NestedString(w.Object, "status", "admission", "clusterQueue"); cq != "" {
				if p, ok := out.reservedPriority[cq]; !ok || priority > p {
					out.reservedPriority[cq] = priority
				}
			}
			continue
		}
		if !workloadWaiting(w) {
			continue
		}
		gpus := workloadGPUsByResource(w, r.gpuNames)
		if len(gpus) == 0 {
			continue
		}
		lowest(w.GetNamespace(), priority)
		lq, _, _ := unstructured.NestedString(w.Object, "spec", "queueName")
		if cq := queueOf[[2]string{w.GetNamespace(), lq}]; cq != "" {
			requests, placements := workloadShape(w, r.gpuNames)
			out.byQueue[cq] = append(out.byQueue[cq], PendingWorkload{Priority: priority, GPUs: gpus,
				Requests: requests, Placements: placements})
		}
	}
	return out, false
}

// workloadShape is what a Workload requests of every resource that is not a
// GPU, in milli-units over all its pod sets (count times one pod), and the
// node selection of each pod set that requests a GPU.
func workloadShape(w *unstructured.Unstructured, gpuNames map[string]bool) (map[string]int64, []WorkloadPlacement) {
	podSets, _, _ := unstructured.NestedSlice(w.Object, "spec", "podSets")
	var requests map[string]int64
	var placements []WorkloadPlacement
	for _, raw := range podSets {
		ps, _ := raw.(map[string]interface{})
		spec, _, _ := unstructured.NestedMap(ps, "template", "spec")
		var pod corev1.PodSpec
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(spec, &pod); err != nil {
			continue
		}
		count, found, err := unstructured.NestedInt64(ps, "count")
		if !found || err != nil {
			count = 1
		}
		for name, milli := range PodRequestsMilli(&pod) {
			if gpuNames[name] {
				continue
			}
			if requests == nil {
				requests = map[string]int64{}
			}
			requests[name] += max(count, 0) * milli
		}
		if podGPUs(&pod, gpuNames) > 0 {
			p := WorkloadPlacement{NodeSelector: pod.NodeSelector}
			if a := pod.Affinity; a != nil && a.NodeAffinity != nil {
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

// workloadWaiting reports whether a Workload without reserved quota is
// waiting to be admitted: active (spec.active not false) and not finished.
func workloadWaiting(w *unstructured.Unstructured) bool {
	if active, found, _ := unstructured.NestedBool(w.Object, "spec", "active"); found && !active {
		return false
	}
	conds, _, _ := unstructured.NestedSlice(w.Object, "status", "conditions")
	for _, raw := range conds {
		cond, _ := raw.(map[string]interface{})
		if cond["type"] == WorkloadFinished && cond["status"] == string(corev1.ConditionTrue) {
			return false
		}
	}
	return true
}

// workloadGPUsByResource is what a Workload requests of each named GPU
// resource: per pod set, spec.podSets[].count (1 when unset) times what one
// pod requests (podGPUs). Resources it does not request are absent.
func workloadGPUsByResource(w *unstructured.Unstructured, gpuNames map[string]bool) map[string]int64 {
	podSets, _, _ := unstructured.NestedSlice(w.Object, "spec", "podSets")
	var out map[string]int64
	for _, raw := range podSets {
		ps, _ := raw.(map[string]interface{})
		spec, _, _ := unstructured.NestedMap(ps, "template", "spec")
		var pod corev1.PodSpec
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(spec, &pod); err != nil {
			continue
		}
		count, found, err := unstructured.NestedInt64(ps, "count")
		if !found || err != nil {
			count = 1
		}
		for name := range gpuNames {
			n := max(count, 0) * podGPUs(&pod, map[string]bool{name: true})
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

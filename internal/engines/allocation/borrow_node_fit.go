package allocation

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/accelerator"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// eligibleFit is, in order, the nodes a pod of r may land on. A replica with
// no templates and no flavor is the view's own rule (eligible: accelerator,
// schedulable, taints its template tolerates). A replica the Kueue borrowing
// placer orders is judged further: every node must satisfy each pod
// template's nodeSelector and required node affinity, which the scheduler
// enforces whatever flavor Kueue adds; and on borrowed capacity (r.flavor set,
// the flavor Kueue admits it on) only the flavor's nodes count, those carrying
// every node label it names, with its tolerations added to the template's (the
// pod carries both). Computed once per cycle and key: the result is shared by
// clones, like eligible's.
func (v *NodeView) eligibleFit(r replicaFit) []int {
	if len(r.templates) == 0 && r.flavor == nil {
		return v.eligible(r.acc, r.tolerations, false)
	}
	tolerations := r.tolerations
	if r.flavor != nil && len(r.flavor.Tolerations) > 0 {
		tolerations = append(slices.Clone(r.tolerations), r.flavor.Tolerations...)
	}
	base := v.eligible(r.acc, tolerations, false)
	var labels map[string]string
	if r.flavor != nil {
		labels = r.flavor.NodeLabels
	}
	raw, err := json.Marshal(struct {
		Acc         string
		Tolerations []corev1.Toleration
		Templates   []domain.PodPlacement
		Labels      map[string]string
	}{r.acc, tolerations, r.templates, labels})
	// A NUL prefix keeps these keys apart from eligible's accelerator keys.
	key := "\x00fit\x00" + string(raw)
	if err == nil {
		v.elig.mu.Lock()
		nodes, ok := v.elig.nodes[key]
		v.elig.mu.Unlock()
		if ok {
			return nodes
		}
	}
	var nodes []int
	for _, i := range base {
		if admitsPlacement(&v.slots[i], r.templates, labels) {
			nodes = append(nodes, i)
		}
	}
	if err == nil {
		v.elig.mu.Lock()
		v.elig.nodes[key] = nodes
		v.elig.mu.Unlock()
	}
	return nodes
}

// PlaceReplicasReclaiming is PlaceReplicas for a wake that may grow into
// nominal its namespace's reclaiming queues lent out (reclaimable, per type;
// ReclaimableNominal): a replica no node holds still places while what is
// left of reclaimable covers its GPUs -- Kueue preempts the borrowers whose
// pods fill those nodes to admit it -- and is taken from it. A replica a node
// holds is debited there and takes nothing. It reports whether every replica
// placed and, per key of reclaimable, what the placement took from it. A nil
// view places everything and takes nothing.
func (v *NodeView) PlaceReplicasReclaiming(reps []ReplicaShape, commit bool, reclaimable map[string]int) (bool, map[string]int) {
	if v == nil {
		return true, nil
	}
	trial := v.Clone()
	left := maps.Clone(reclaimable)
	var took map[string]int
	for _, r := range reps {
		if trial.place(trial.fitOf(r)) {
			continue
		}
		k, ok := accelerator.FindKey(left, r.Accelerator)
		g := max(r.GPUs, 1)
		if !ok || left[k] < g {
			return false, nil
		}
		left[k] -= g
		if took == nil {
			took = map[string]int{}
		}
		took[k] += g
	}
	if commit {
		v.adopt(trial)
	}
	return true, took
}

// admitsPlacement reports whether a node already eligible for the pod's
// accelerator and tolerations also satisfies its templates and the flavor's
// node labels (nil: no flavor).
func admitsPlacement(n *nodeSlot, templates []domain.PodPlacement, flavorLabels map[string]string) bool {
	for i := range templates {
		if !templateAdmits(&templates[i], n.labels) {
			return false
		}
	}
	for k, val := range flavorLabels {
		if got, ok := n.labels[k]; !ok || got != val {
			return false
		}
	}
	return true
}

// viewDebits is a node view's debits and their owners, taken before a
// tentative placement the borrowing placer may roll back.
type viewDebits struct {
	debit map[int]int
	owned map[string]map[int]int
}

// saveDebits returns the view's debits and owners as they are now. Nil view:
// the zero value.
func (v *NodeView) saveDebits() viewDebits {
	if v == nil {
		return viewDebits{}
	}
	s := viewDebits{debit: maps.Clone(v.debit)}
	if v.owned != nil {
		s.owned = make(map[string]map[int]int, len(v.owned))
		for owner, perNode := range v.owned {
			s.owned[owner] = maps.Clone(perNode)
		}
	}
	return s
}

// restoreDebits puts back debits and owners saveDebits took: a tentative
// placement rolled back leaves no debit and charges no owner.
func (v *NodeView) restoreDebits(s viewDebits) {
	if v == nil {
		return
	}
	v.debit = s.debit
	if v.debit == nil {
		v.debit = map[int]int{}
	}
	v.owned = s.owned
}

// templateAdmits reports whether a node with these labels satisfies a pod
// template's nodeSelector and required node affinity, as the scheduler judges
// them: every nodeSelector pair, and at least one affinity term whose every
// match expression holds. matchFields (which name the node, not its labels)
// are not judged here: a term carrying only them admits the node, so the view
// stays as permissive as it was before templates were read.
func templateAdmits(p *domain.PodPlacement, labels map[string]string) bool {
	for k, val := range p.NodeSelector {
		if got, ok := labels[k]; !ok || got != val {
			return false
		}
	}
	if p.RequiredNodeAffinity == nil || len(p.RequiredNodeAffinity.NodeSelectorTerms) == 0 {
		return true
	}
	for _, term := range p.RequiredNodeAffinity.NodeSelectorTerms {
		if len(term.MatchExpressions) == 0 && len(term.MatchFields) == 0 {
			continue // an empty term matches no node
		}
		if termAdmits(term.MatchExpressions, labels) {
			return true
		}
	}
	return false
}

// termAdmits reports whether every requirement of one node selector term holds
// for labels.
func termAdmits(reqs []corev1.NodeSelectorRequirement, labels map[string]string) bool {
	for _, r := range reqs {
		if !requirementHolds(r, labels) {
			return false
		}
	}
	return true
}

// requirementHolds evaluates one node selector requirement against labels.
func requirementHolds(r corev1.NodeSelectorRequirement, labels map[string]string) bool {
	val, has := labels[r.Key]
	switch r.Operator {
	case corev1.NodeSelectorOpIn:
		return has && slices.Contains(r.Values, val)
	case corev1.NodeSelectorOpNotIn:
		return !has || !slices.Contains(r.Values, val)
	case corev1.NodeSelectorOpExists:
		return has
	case corev1.NodeSelectorOpDoesNotExist:
		return !has
	case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
		if len(r.Values) != 1 || !has {
			return false
		}
		want, err1 := strconv.ParseInt(r.Values[0], 10, 64)
		got, err2 := strconv.ParseInt(val, 10, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		if r.Operator == corev1.NodeSelectorOpGt {
			return got > want
		}
		return got < want
	}
	return false
}

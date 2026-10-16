package allocation

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// The utilization share's node fit judges a receiver's group as the node
// view does (NodeView.place): an exclusive-topology LeaderWorkerSet's pods in
// one domain, a domain holding one group, and each pod by its own template --
// the leader's or the workers' (podTemplates). The idle fill places through a
// node view built from the share's picture (ShareFitPods); the donor-set
// search, which opens holes the view does not model, judges templates by the
// view's functions (sharePodNodes) and domains by the same rule.

// ShareReplicaFit is one receiver replica as the share's node fit places it.
type ShareReplicaFit struct {
	// PodGPUs is each pod's GPUs, leader first.
	PodGPUs []int
	// DomainKey is the node label every pod of a group must share a value
	// of (an exclusive-topology LeaderWorkerSet), "" for none.
	DomainKey string
	// Placements is the pod templates' nodeSelector and required node
	// affinity: one template, or the leader's then the workers'. Nil judges
	// none.
	Placements []domain.PodPlacement
}

// domainKey is the label r's pods must share a value of, and whether r is a
// group bound to one domain: as the node view places it, a topology key and
// more than one pod taking GPUs. "" and false otherwise.
func (r ShareReplicaFit) domainKey() (string, bool) {
	pods := 0
	for _, g := range r.PodGPUs {
		if g > 0 {
			pods++
		}
	}
	if r.DomainKey == "" || pods < 2 {
		return "", false
	}
	return r.DomainKey, true
}

// receiverFit is receiver rc's replica of grow as the share's node fit
// places it.
func (in SharePlanInput) receiverFit(rc string, grow ShareVariant) ShareReplicaFit {
	return ShareReplicaFit{PodGPUs: grow.PodGPUs, DomainKey: in.DomainKey[rc], Placements: in.Placements[rc]}
}

// ShareFitPods places one replica's pods on nodes as the node view places a
// replica (NodeView.place): largest first, each into the node with the
// smallest free count that holds it and that its template admits. A group
// with an exclusive topology lands in one domain, each tried in turn, and
// only in one that holds no group yet (ShareNode.Group, ShareNode.Claimed);
// a node without the label is in none. It spends what it placed and claims
// the domain a group took, and reports whether every pod fitted; it spends
// nothing when not. Where no donor is needed it places as the node-aware set
// search does.
func ShareFitPods(nodes map[string]ShareNode, r ShareReplicaFit) bool {
	v := shareNodeView(nodes)
	if !v.place(replicaFit{pods: r.PodGPUs, domain: r.DomainKey, templates: r.Placements}) {
		return false
	}
	for i := range v.slots {
		info := nodes[v.slots[i].name]
		info.Free = v.free(i)
		nodes[v.slots[i].name] = info
	}
	for id := range v.domains {
		key, value, _ := strings.Cut(id, "\x00")
		claimDomain(nodes, key, value)
	}
	return true
}

// shareNodeView is a node view of the share's picture: each node holding its
// free GPUs, its labels, and a group when its domain holds one. The
// accelerator and taints were judged when the picture was built
// (shareFreeNodesAfter), so the view judges neither.
func shareNodeView(nodes map[string]ShareNode) *NodeView {
	snap := make(map[string]decision.NodeGPU, len(nodes))
	for n, info := range nodes {
		snap[n] = decision.NodeGPU{Capacity: info.Free, Labels: info.Labels, LWSGroup: info.Group || info.Claimed}
	}
	return NewNodeView(snap)
}

// ClaimShareDomains marks, per topology label key of taken, the nodes of each
// value listed Claimed: an exclusive-topology group this cycle already placed
// there (NodeView.TakenDomains).
func ClaimShareDomains(nodes map[string]ShareNode, taken map[string][]string) {
	for key, values := range taken {
		for _, value := range values {
			claimDomain(nodes, key, value)
		}
	}
}

// claimDomain marks the nodes of value of label key Claimed.
func claimDomain(nodes map[string]ShareNode, key, value string) {
	if key == "" || value == "" {
		return
	}
	for n, info := range nodes {
		if info.Labels[key] == value {
			info.Claimed = true
			nodes[n] = info
		}
	}
}

// domainClaimed reports whether a node of names is Claimed.
func domainClaimed(nodes map[string]ShareNode, names []string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return nodes[n].Claimed })
}

// sharePodFit is one receiver pod as the donor-set search places it: its
// GPUs and the nodes its template admits.
type sharePodFit struct {
	gpus  int
	nodes []string
}

// sharePodNodes is r's pods that take GPUs, largest first (the leader first
// on a tie), each with the nodes of names it may land on: judged by its own
// template as the node view judges it (podTemplates, admitsPlacement), names'
// order kept.
func sharePodNodes(nodes map[string]ShareNode, names []string, r ShareReplicaFit) []sharePodFit {
	rf := replicaFit{pods: r.PodGPUs, templates: r.Placements}
	var out []sharePodFit
	for i, g := range r.PodGPUs {
		if g <= 0 {
			continue
		}
		admitted := names
		if tpl := podTemplates(rf, i); len(tpl) > 0 {
			admitted = slices.DeleteFunc(slices.Clone(names), func(n string) bool {
				return !admitsPlacement(&nodeSlot{labels: nodes[n].Labels}, tpl, nil)
			})
		}
		out = append(out, sharePodFit{gpus: g, nodes: admitted})
	}
	slices.SortStableFunc(out, func(a, b sharePodFit) int { return cmp.Compare(b.gpus, a.gpus) })
	return out
}

// freesGroups reports whether every node of names a LeaderWorkerSet group's
// pod is on (ShareNode.Group) has that pod leaving with a group the search
// took: the domain then holds no group but the receiver's once the set lands.
func (s *shareNodeSearch) freesGroups(names []string) bool {
	freed := map[string]bool{}
	for r := range s.used {
		if u := s.in.DonorUnits[r.donor][r.unit]; u.Group {
			for _, p := range u.Pods {
				freed[p.Node] = true
			}
		}
	}
	return !slices.ContainsFunc(names, func(n string) bool { return s.in.Nodes[n].Group && !freed[n] })
}

// TakenDomains is, per topology label key, the values of it in which the view
// placed or holds an exclusive-topology group (NodeView.domains), for every
// owner exclude does not name, each sorted. Nil for a nil view.
func (v *NodeView) TakenDomains(exclude func(owner string) bool) map[string][]string {
	if v == nil {
		return nil
	}
	out := map[string][]string{}
	for _, id := range slices.Sorted(maps.Keys(v.domains)) {
		if exclude != nil && exclude(v.domains[id]) {
			continue
		}
		key, value, _ := strings.Cut(id, "\x00")
		out[key] = append(out[key], value)
	}
	return out
}

package scalefromzero

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
)

// A wake is judged against fresh constraints: the usage the cluster reports
// now. A wake admitted a moment ago is not in that usage yet -- its pods do not
// exist, or are Pending and hold no node -- so within one tick each wake's
// demand is carried forward by hand: debited from the constraints of every
// later wake in the tick (tickWakes), and from the tick's node view
// (tickNodes). Models are decided concurrently, so the check and the debit are
// one step under wakeMu (decideWake).
//
// Across ticks, nothing carries a wake: the next tick's usage counts the
// wake's pods once they hold a node (scheduled, or nominated for one after a
// preemption), and not while they wait unscheduled. That window is the limit
// of the check.

// tickNodeView is a tick's node view, built on first use.
type tickNodeView struct {
	built bool
	view  *allocation.NodeView
	// skipped is the reason last reported for not fitting wakes to nodes
	// ("" when they were fitted); it outlives the tick so a standing reason
	// is reported once.
	skipped string
}

// beginTick forgets the previous tick's wakes and node view.
func (e *Engine) beginTick() {
	e.wakeMu.Lock()
	defer e.wakeMu.Unlock()
	e.tickWakes = nil
	e.tickNodes.built, e.tickNodes.view = false, nil
}

// decideWake judges a model's wake and, when it selects a set, records what
// the set takes, as one step: see the comment at the top of this file.
func (e *Engine) decideWake(ctx context.Context, group modelGroup, candidates []Candidate, covered coverage) (placement, []Candidate, SelectionOutcome) {
	place := e.placementFor(ctx, group.namespace)
	e.wakeMu.Lock()
	defer e.wakeMu.Unlock()
	if place.snapshotTakenAt.IsZero() && place.constraints != nil {
		place.constraints = allocation.WithholdPromised(place.constraints, e.tickWakes)
		place.nodes = e.tickNodeViewFor(ctx, place.constraints)
	}
	selected, outcome := e.selectOrClaim(ctx, group, candidates, covered, place)
	if len(selected) > 0 {
		e.recordWake(ctx, group, place, selected)
	}
	return place, selected, outcome
}

// debitTick records a wake's demand against this tick's later wakes. Caller
// holds wakeMu.
func (e *Engine) debitTick(namespace string, demand map[string]int) {
	if len(demand) == 0 {
		return
	}
	if e.tickWakes == nil {
		e.tickWakes = map[string]map[string]int{}
	}
	if e.tickWakes[namespace] == nil {
		e.tickWakes[namespace] = map[string]int{}
	}
	for acc, gpus := range demand {
		e.tickWakes[namespace][acc] += gpus
	}
}

// tickNodeViewFor is the tick's node view when the physical limiter bounds a
// wake checked against constraints, built on first use; nil otherwise. A
// physical limiter skipped for this wake (no node read, usage unobserved) is
// not among the constraints, and its node fit is skipped with it. Caller holds
// wakeMu.
func (e *Engine) tickNodeViewFor(ctx context.Context, constraints []*allocation.ResourceConstraints) *allocation.NodeView {
	if !allocation.PhysicalBounds(constraints) {
		return nil
	}
	if !e.tickNodes.built {
		now := time.Now()
		view, reason := allocation.NodeViewFor(constraints, nil, now)
		// A node freed for a share receiver is not free to a wake either (the
		// constraints already withhold the promise from the pools).
		allocation.ReservePromised(view, decision.LatestSharePromised(now))
		e.tickNodes.built, e.tickNodes.view = true, view.WithLayoutLog(&e.layouts)
		if reason != e.tickNodes.skipped {
			if reason != "" {
				log.FromContext(ctx).Info("Scale-from-zero: wakes are not fitted to nodes", "reason", reason)
			} else {
				log.FromContext(ctx).Info("Scale-from-zero: wakes are fitted to nodes again")
			}
			e.tickNodes.skipped = reason
		}
	}
	return e.tickNodes.view
}

package learnertest

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// The closed-loop probe's fleet simulator (analyzer-learner.md, 2.5): an
// analyzer driven by a fleet that turns its order into replicas. The open
// loop replays recorded inputs that do not depend on outputs; holds,
// latches, the sibling yield and a self-sealing low figure are closed-loop
// behaviours -- the order changes the load each replica sees, and so what the
// analyzer is next shown -- and only a loop sees them.
//
// The simulator, one Cycle at a time:
//
//   - The router is queue-aware, as the EPP's queue scorer is: each cycle's
//     arrivals go to the replicas holding the fewest waiting requests
//     (route). With no queue anywhere that is an equal split, or a split
//     by Weight, a router that also steers by something else. QueueBlind
//     is a router that splits by Weight alone.
//   - Each replica serves up to its true mu; what it cannot serve queues on
//     it, and its queue drains as the router sends it less than its mu.
//   - The step (the analyzer under test, and how its rows are made from the
//     replicas) returns an order, in replicas.
//   - An order above the fleet starts replicas, Ready after the start time
//     S. An order below it removes replicas, newest first, once the highest
//     order of the last stabilization window is below the fleet, and no
//     replica is starting.
//   - A replica just Ready is first seen by the analyzer then.
//
// The verdict is against need, which the case defines: the fleet a truthful
// analyzer orders at the arrival rate of the moment.

// Simulator defaults.
const (
	// DefaultStartTime is S, from an order to the replica Ready.
	DefaultStartTime = 90 * time.Second
	// DefaultStabilization is the scale-down stabilization window.
	DefaultStabilization = 5 * time.Minute
	// OverTolerance is how far over need the fleet may settle, as a
	// fraction, plus one replica of rounding.
	OverTolerance = 0.15
	// UnderFor is how long the fleet may sit below need in one run: the
	// start time, the joined replica's settle, and a cycle or two of
	// ordering on either side of them.
	UnderFor = 5 * time.Minute
)

// Fleet is one closed-loop scenario.
type Fleet struct {
	// Pods is the fleet's true mu per replica at the start; added replicas
	// serve AddedMu.
	Pods    []float64
	AddedMu float64
	// Mu, when set, is a replica's true mu at an elapsed time, overriding
	// Pods and AddedMu (a slow replica that recovers).
	Mu func(i int, elapsed time.Duration) float64
	// Lambda is the arrival rate at an elapsed time.
	Lambda func(elapsed time.Duration) float64
	// Weight, when set, is a replica's share of the arrivals relative to
	// the other Ready replicas' at an elapsed time, among replicas with the
	// same queue per unit of weight (route): a router that steers by
	// something besides the queue, such as prefix affinity. Nil weighs every
	// replica 1, and so do weights that sum to zero or less, or to no
	// number, on a cycle.
	Weight func(i int, elapsed time.Duration) float64
	// QueueBlind splits the arrivals by Weight alone, whatever each replica
	// holds queued: a Service's round robin, or a router with no queue
	// scorer. A case whose premise is a replica that queues while its
	// siblings do not runs on it; a queue-aware router would not let that
	// queue form.
	QueueBlind bool
	Horizon    time.Duration
	// Need is the fleet a truthful analyzer orders at arrival rate lambda.
	Need func(lambda float64) int
	// Start is the first cycle's instant; Start (the package's) when zero.
	Start time.Time
	// StartTime replaces DefaultStartTime, and Stabilization
	// DefaultStabilization, when set.
	StartTime, Stabilization time.Duration
	// MaxReplicas caps an order, as a scale target's maxReplicas does; zero
	// is no cap. An analyzer whose order feeds itself (each joiner it starts
	// lowers its figure) runs into it rather than without end.
	MaxReplicas int
	// Starting, when set, is called on each cycle before the step with how
	// long each replica still starting has been since its order, as the
	// collector reports a not-Ready Pod's age (PendingAges): an analyzer
	// that reads its starting replicas is shown them.
	Starting func(ages []time.Duration)
}

// Replica is one Ready replica on a cycle, as the step sees it.
type Replica struct {
	// Index is the replica's position in the fleet, Name its Pod name, and
	// ReadyAt when it became Ready.
	Index   int
	Name    string
	ReadyAt time.Time
	// Mu is its true rate, Offered its share of the arrivals, Served what
	// it served, and Backlog its queue after the cycle.
	Mu, Offered, Served, Backlog float64
	// Saturated is whether it served at its limit: it queued, or was
	// offered its mu or more.
	Saturated bool
}

// Step is one analyzer cycle at now: the Ready replicas under arrival rate
// lambda, elapsed into the run. It returns the replicas the analyzer orders,
// or Hold.
type Step func(now time.Time, elapsed time.Duration, lambda float64, ready []Replica) int

// Hold is a Step's order when the analyzer orders nothing on the cycle (it
// has no figure to price from): the fleet, starting replicas included, is
// held as it is, and the hold counts in the stabilization window as an
// order of that size.
const Hold = 0

// Result is what the loop did.
type Result struct {
	// Need is need at the horizon's arrival rate; Final and Peak the fleet
	// at the horizon and at its largest, starting replicas included.
	Need, Final, Peak int
	// OverSince is how long before the horizon the fleet has been over
	// need's tolerance without a break; zero when it ends inside it.
	OverSince time.Duration
	// UnderFor is the longest run the fleet sat below need at the arrival
	// rate of the moment.
	UnderFor time.Duration
	// Trace is the fleet on each cycle.
	Trace []int
}

// pod is one simulated replica.
type pod struct {
	name    string
	readyAt time.Time
	backlog float64
}

// Run drives the fleet to its horizon, one Cycle at a time, and returns what
// it did.
func (f Fleet) Run(step Step) Result {
	start := f.Start
	if start.IsZero() {
		start = Start
	}
	startTime, stabilization := DefaultStartTime, DefaultStabilization
	if f.StartTime > 0 {
		startTime = f.StartTime
	}
	if f.Stabilization > 0 {
		stabilization = f.Stabilization
	}
	pods := make([]*pod, 0, len(f.Pods))
	for i := range f.Pods {
		pods = append(pods, &pod{name: fmt.Sprintf("r%02d", i), readyAt: start.Add(-time.Hour)})
	}
	trueMu := func(i int, elapsed time.Duration) float64 {
		if f.Mu != nil {
			return f.Mu(i, elapsed)
		}
		if i < len(f.Pods) {
			return f.Pods[i]
		}
		return f.AddedMu
	}
	type order struct {
		at time.Time
		n  int
	}
	var orders []order
	res := Result{Need: f.Need(f.Lambda(f.Horizon))}
	bound := float64(res.Need)*(1+OverTolerance) + 1
	var overFrom, underFrom time.Time
	dt := Cycle.Seconds()
	for now := start; now.Sub(start) < f.Horizon; now = now.Add(Cycle) {
		elapsed := now.Sub(start)
		// next is the cycle's end: an order, a started replica and the
		// verdicts are stamped with it, as the analyzer's clock has moved
		// past the cycle it analyzed.
		next := now.Add(Cycle)
		lambda := f.Lambda(elapsed)
		var ready []int
		for i, p := range pods {
			if !p.readyAt.After(now) {
				ready = append(ready, i)
			}
		}
		replicas := make([]Replica, 0, len(ready))
		weights := make([]float64, len(ready))
		queues := make([]float64, len(ready))
		sum := 0.0
		for j, i := range ready {
			weights[j] = 1
			if !f.QueueBlind {
				queues[j] = pods[i].backlog / dt
			}
			if f.Weight != nil {
				weights[j] = f.Weight(i, elapsed)
			}
			sum += weights[j]
		}
		if !(sum > 0) || math.IsInf(sum, 1) {
			for j := range weights {
				weights[j] = 1
			}
		}
		offers := route(lambda, weights, queues)
		for j, i := range ready {
			p, mu := pods[i], trueMu(i, elapsed)
			offered := offers[j]
			p.backlog = max(p.backlog+(offered-mu)*dt, 0)
			saturated := p.backlog > 0 || offered >= mu
			served := offered
			if saturated {
				served = mu
			}
			replicas = append(replicas, Replica{Index: i, Name: p.name, ReadyAt: p.readyAt, Mu: mu,
				Offered: offered, Served: served, Backlog: p.backlog, Saturated: saturated})
		}
		if f.Starting != nil {
			var ages []time.Duration
			for _, p := range pods {
				if p.readyAt.After(now) {
					ages = append(ages, now.Sub(p.readyAt.Add(-startTime)))
				}
			}
			f.Starting(ages)
		}
		ordered := step(now, elapsed, lambda, replicas)
		if ordered <= Hold {
			ordered = len(pods)
		}
		if f.MaxReplicas > 0 {
			ordered = min(ordered, f.MaxReplicas)
		}
		orders = append(orders, order{at: next, n: ordered})
		for len(orders) > 0 && next.Sub(orders[0].at) > stabilization {
			orders = orders[1:]
		}
		starting := len(pods) - len(ready)
		switch {
		case ordered > len(pods):
			for range ordered - len(pods) {
				pods = append(pods, &pod{name: fmt.Sprintf("r%02d", len(pods)), readyAt: next.Add(startTime)})
			}
		case starting == 0:
			keep := 0
			for _, o := range orders {
				keep = max(keep, o.n)
			}
			if keep < len(pods) {
				pods = pods[:keep]
			}
		}
		res.Trace = append(res.Trace, len(pods))
		res.Peak = max(res.Peak, len(pods))
		if len(pods) < f.Need(lambda) {
			if underFrom.IsZero() {
				underFrom = now
			}
			res.UnderFor = max(res.UnderFor, next.Sub(underFrom))
		} else {
			underFrom = time.Time{}
		}
		if float64(len(pods)) > bound {
			if overFrom.IsZero() {
				overFrom = next
			}
		} else {
			overFrom = time.Time{}
		}
	}
	end := start.Add(time.Duration(len(res.Trace)) * Cycle)
	res.Final = len(pods)
	if !overFrom.IsZero() {
		res.OverSince = end.Sub(overFrom)
	}
	return res
}

// route is the router: arrival rate lambda split over replicas of weight w
// holding queue q (requests waiting, over the cycle: a rate). It is the fluid
// limit of a shortest-queue pick, which the EPP's queue scorer makes per
// request: the arrivals fill the replicas to one level L of queue per unit of
// weight, and a replica whose queue is already over the level gets none,
//
//	offer_i = max(w_i L - q_i, 0),  sum_i offer_i = lambda.
//
// With no queue anywhere it is the split by weight, and with equal weights
// the equal split. A replica that queues gets a share that drains it, so the
// queue a slow start leaves on the first replicas is spread over the fleet
// instead of fed. It is a model of the scorer's ordering only: the router
// does not know a replica's mu, and the EPP's other scorers (KV, prefix) are
// what Weight stands for. A replica of weight zero or less gets nothing.
func route(lambda float64, w, q []float64) []float64 {
	offers := make([]float64, len(w))
	order := make([]int, 0, len(w))
	for j := range w {
		if w[j] > 0 {
			order = append(order, j)
		}
	}
	if !(lambda > 0) || len(order) == 0 {
		return offers
	}
	sort.SliceStable(order, func(a, b int) bool { return q[order[a]]/w[order[a]] < q[order[b]]/w[order[b]] })
	level, sumW, sumQ, n := 0.0, 0.0, 0.0, 0
	for n < len(order) {
		j := order[n]
		sumW, sumQ, n = sumW+w[j], sumQ+q[j], n+1
		level = (lambda + sumQ) / sumW
		if n < len(order) && q[order[n]]/w[order[n]] >= level {
			break
		}
	}
	for _, j := range order[:n] {
		offers[j] = max(w[j]*level-q[j], 0)
	}
	return offers
}

// Verdict is "" when the fleet ends within tolerance of need, else why not.
// Over-ordered: the fleet ends more than OverTolerance plus one replica over
// need. Under-ordered: it ends more than one replica under need, or sat
// below need at the arrival rate of the moment for longer than UnderFor at
// any point of the run. A shorter dip, while an order is starting and
// settling, is the start time, not a verdict.
func (r Result) Verdict() string {
	bound := float64(r.Need)*(1+OverTolerance) + 1
	switch {
	case float64(r.Final) > bound:
		return fmt.Sprintf("over-ordered: %d replicas against a need of %d (bound %.1f), over it for the last %s",
			r.Final, r.Need, bound, r.OverSince)
	case r.Final < r.Need-1:
		return fmt.Sprintf("under-ordered: %d replicas against a need of %d", r.Final, r.Need)
	case r.UnderFor > UnderFor:
		return fmt.Sprintf("under-ordered mid-run: below need for %s, over the %s a start and a settle take",
			r.UnderFor, UnderFor)
	}
	return ""
}

// Ceil is the order for a fractional replica count: rounded up, at least
// one, and one for NaN.
func Ceil(replicas float64) int {
	n := int(math.Ceil(replicas - 1e-9))
	if math.IsNaN(replicas) || n < 1 {
		return 1
	}
	return n
}

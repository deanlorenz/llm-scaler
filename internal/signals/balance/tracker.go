package balance

import (
	"sort"
	"sync"
)

// Reading is one role's distribution this cycle together with what the
// tracker remembers about it.
type Reading struct {
	Distribution
	// Sustained is true once the role has read imbalanced on SustainCycles
	// consecutive cycles, and stays true until a cycle reads it balanced.
	Sustained bool
	// Changed is true on the cycle Sustained flipped, either way: the one
	// cycle a warning (or its all-clear) is logged.
	Changed bool
}

// Tracker remembers, per model and role, how many consecutive cycles the role
// has read imbalanced. It is safe for concurrent use.
type Tracker struct {
	mu     sync.Mutex
	models map[string]map[string]roleState
}

type roleState struct {
	streak    int
	sustained bool
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{models: map[string]map[string]roleState{}}
}

// Observe folds one cycle's distributions for the model named by key and
// returns a Reading per role, sorted by role. A role absent this cycle, one
// with no ready replica, is forgotten: when it returns it starts again from
// zero, so a parked role does not come back reporting the imbalance it had
// before it parked.
func (t *Tracker) Observe(key string, dists map[string]Distribution) []Reading {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev := t.models[key]
	next := make(map[string]roleState, len(dists))
	out := make([]Reading, 0, len(dists))
	for role, d := range dists {
		s := prev[role]
		if d.Imbalanced() {
			s.streak++
		} else {
			s.streak = 0
		}
		sustained := s.streak >= SustainCycles
		out = append(out, Reading{Distribution: d, Sustained: sustained, Changed: sustained != s.sustained})
		s.sustained = sustained
		next[role] = s
	}
	if len(next) == 0 {
		delete(t.models, key)
	} else {
		t.models[key] = next
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
	return out
}

// Forget drops what the tracker remembers about the model named by key.
func (t *Tracker) Forget(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.models, key)
}

// Retain forgets every model whose key is not in active, so an empty set
// forgets them all.
func (t *Tracker) Retain(active map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key := range t.models {
		if !active[key] {
			delete(t.models, key)
		}
	}
}

package allocation

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// InFlightTTL is how long a replica admitted onto a node hole holds that hole
// while its pods hold no node. It is about how long placement takes: a pod
// Pending longer than this is not waiting for the hole it was admitted to --
// it is unschedulable for another reason (a taint, an affinity, CPU or memory
// on the node), or the hole went to someone else -- and holding the hole
// longer would only keep it from a replica that could use it.
const InFlightTTL = 5 * time.Minute

// InFlightOwner is the owner the in-flight ledger's holes are charged to on a
// node view (NodeView.DebitsExcept).
const InFlightOwner = "in-flight"

// InFlightLedger remembers the replicas a decision admitted onto node holes
// whose pods hold no node yet, so later decisions do not admit another replica
// into the same hole. The node snapshot counts a pod on its node once it is
// scheduled, or nominated for the node after a preemption; before that, a
// Pending pod holds nothing there, and a wake or a scale-up in that window
// -- scheduling takes seconds, an image pull or a preemption longer -- saw the
// hole free again. Two cold models woke into one free 8-GPU node, one tick
// apart.
//
// One ledger serves both engines (DefaultInFlight): the steady-state engine
// admits the growth it orders, the scale-from-zero engine the wakes it admits,
// and every node view either builds holds what the ledger holds
// (NodeViewFor). Mirrors the utilization share's promised GPUs, which hold a
// freed node for a receiver until its pods land.
//
// An entry is per variant ("namespace/name"): the number of replicas the
// variant is to have holding a node (want), and how to count those it has
// (placed, from the variant's pods: scheduled, or nominated). What it holds is
// want less placed, until placed reaches want or InFlightTTL passes since the
// growth was admitted. A count that rises is applied to a node view only
// once the view's snapshot is at least as recent as when the rise was seen:
// before, the snapshot does not count the pod on its node either.
//
// Safe for concurrent use.
type InFlightLedger struct {
	mu      sync.Mutex
	entries map[string]*inFlightEntry
}

// inFlightEntry is one variant's replicas in flight.
type inFlightEntry struct {
	shape ReplicaShape
	want  int
	// base is how many replicas held a node when the entry was admitted,
	// assumed already in any snapshot; seen is each higher count since, with
	// when it was first observed.
	base int
	seen []placedAt
	// expires is when the entry stops holding anything; it is kept, holding
	// nothing, until the variant reaches want or wants more, so an
	// unschedulable replica is not admitted again every cycle.
	expires time.Time
	// placed counts the variant's replicas holding a node, ok false when it
	// cannot tell.
	placed func() (int, bool)
}

// placedAt is a placed count and when it was first observed.
type placedAt struct {
	n  int
	at time.Time
}

// NewInFlightLedger returns an empty ledger.
func NewInFlightLedger() *InFlightLedger {
	return &InFlightLedger{entries: map[string]*inFlightEntry{}}
}

// DefaultInFlight is the process-wide ledger both engines share.
var DefaultInFlight = NewInFlightLedger()

// Admit records that variant key ("namespace/name") is to have want replicas
// of shape holding a node, placedNow of which do. placed counts them later
// (nil: never known, so the entry holds until it expires). A want no higher
// than placedNow ends the entry. A want higher than the entry's admits more
// replicas and starts InFlightTTL afresh; an equal or lower want keeps the
// clock, so a replica Pending for good is not held again every cycle.
func (l *InFlightLedger) Admit(key string, shape ReplicaShape, want, placedNow int, placed func() (int, bool), now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if want <= placedNow {
		delete(l.entries, key)
		return
	}
	e, ok := l.entries[key]
	switch {
	case !ok:
		l.entries[key] = &inFlightEntry{shape: shape, want: want, base: placedNow,
			expires: now.Add(InFlightTTL), placed: placed}
		return
	case want > e.want:
		e.expires = now.Add(InFlightTTL)
	}
	if shape.Flavor == nil && e.shape.Flavor != nil && want <= e.want {
		// The replicas admitted on a borrowed flavor's nodes are the same
		// ones once the borrowed draw resolves (their pods exist, gated by
		// Kueue and still holding no node): their holes stay on that flavor.
		shape.Flavor, shape.Placements = e.shape.Flavor, e.shape.Placements
	}
	e.shape, e.want, e.placed = shape, want, placed
	e.base = max(e.base, placedNow)
}

// Holds reports whether the ledger holds anything for variant key now.
func (l *InFlightLedger) Holds(key string, now time.Time) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	return ok && now.Before(e.expires)
}

// Forget drops variant key's entry.
func (l *InFlightLedger) Forget(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// Reset empties the ledger. For tests.
func (l *InFlightLedger) Reset() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	clear(l.entries)
}

// settle counts each entry's placed replicas: an entry whose variant has
// them all ends, a higher count is remembered with when it was seen, and an
// entry expired a TTL ago is dropped (its variant was deleted, or stopped
// asking). The counts are read without the lock: they read the pods.
func (l *InFlightLedger) settle(now time.Time) {
	l.mu.Lock()
	keys := slices.Sorted(maps.Keys(l.entries))
	counts := make(map[string]func() (int, bool), len(keys))
	for _, k := range keys {
		if e := l.entries[k]; e.placed != nil {
			counts[k] = e.placed
		}
	}
	l.mu.Unlock()
	observed := map[string]int{}
	for _, k := range keys {
		if f := counts[k]; f != nil {
			if n, ok := f(); ok {
				observed[k] = n
			}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		e, ok := l.entries[k]
		if !ok {
			continue
		}
		if now.Sub(e.expires) > InFlightTTL {
			delete(l.entries, k)
			continue
		}
		n, ok := observed[k]
		if !ok {
			continue
		}
		if n >= e.want {
			delete(l.entries, k)
			continue
		}
		if last := e.base; n > last && (len(e.seen) == 0 || n > e.seen[len(e.seen)-1].n) {
			e.seen = append(e.seen, placedAt{n: n, at: now})
		}
	}
}

// holdOn debits from v the replicas the ledger holds as of now, each charged
// to its own owner under InFlightOwner: per live entry, in key order, want less the replicas placed
// by the time v's snapshot was taken, each placed best-fit. One that no
// longer fits is skipped -- it is in flight either way, and the hole it was
// admitted to has gone to someone else.
func (l *InFlightLedger) holdOn(v *NodeView, now time.Time) {
	if l == nil || v == nil {
		return
	}
	l.settle(now)
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := v.setOwner(InFlightOwner)
	defer v.setOwner(prev)
	for _, k := range slices.Sorted(maps.Keys(l.entries)) {
		e := l.entries[k]
		if !now.Before(e.expires) {
			continue
		}
		placed := e.base
		for _, s := range e.seen {
			if !s.at.After(v.takenAt) {
				placed = max(placed, s.n)
			}
		}
		r := v.fitOf(e.shape)
		// Each held replica is charged to its own owner, numbered from the
		// variant's first replica, so ReleaseUncreated can take back the
		// holes of the replicas whose pods do not exist yet.
		for n := placed + 1; n <= e.want; n++ {
			v.setOwner(inFlightReplicaOwner(k, n))
			v.place(r)
		}
	}
}

// inFlightReplicaOwner is the owner of the hole the ledger holds for variant
// key's n-th replica (1-based): InFlightOwner, then the variant and number.
func inFlightReplicaOwner(key string, n int) string {
	return InFlightOwner + "\x00" + key + "\x00" + strconv.Itoa(n)
}

// ReleaseUncreated takes back from v the holes the in-flight ledger holds for
// replicas whose pods do not exist yet: per variant key ("namespace/name") of
// created, every held replica numbered above its created replicas. A pass that
// sizes a variant from its current replicas re-places exactly those replicas;
// holding their holes against it refused the very replica it ordered a cycle
// ago, and its target alternated. The holes of replicas whose pods exist and
// hold no node yet stay held. A variant not in created is left as it is.
func (l *InFlightLedger) ReleaseUncreated(v *NodeView, created map[string]int) {
	if l == nil || v == nil || len(created) == 0 {
		return
	}
	release := map[string]bool{}
	for owner := range v.owned {
		rest, ok := strings.CutPrefix(owner, InFlightOwner+"\x00")
		if !ok {
			continue
		}
		key, num, ok := strings.Cut(rest, "\x00")
		if !ok {
			continue
		}
		have, tracked := created[key]
		n, err := strconv.Atoi(num)
		if tracked && err == nil && n > have {
			release[owner] = true
		}
	}
	for owner := range release {
		for i, g := range v.owned[owner] {
			if v.debit[i] -= g; v.debit[i] <= 0 {
				delete(v.debit, i)
			}
		}
		delete(v.owned, owner)
	}
}

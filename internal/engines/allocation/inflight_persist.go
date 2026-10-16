package allocation

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/kueue"
)

// InFlightFormat is the version of the persisted in-flight ledger
// (PersistedInFlight.Version) this build writes and reads. A ledger of another
// version is ignored.
const InFlightFormat = 1

// Bounds on the persisted in-flight ledger. It is written to the
// controller-status ConfigMap, whose whole content must stay well under the
// 1 MiB object limit, and read back from a ConfigMap anyone who can write it
// may have edited. Past maxInFlightPersisted entries, or maxInFlightBytes of
// them, the entries whose holds end soonest are left out; a ledger read back
// past them, or with a figure this controller does not write, has those
// entries dropped.
const (
	maxInFlightPersisted = 512
	maxInFlightBytes     = 128 << 10
	// maxInFlightPods bounds one replica's pods.
	maxInFlightPods = 256
	// maxInFlightTTL bounds how long a restored entry may still hold.
	maxInFlightTTL = 24 * time.Hour
)

// PersistedInFlight is the in-flight ledger as the controller status keeps it
// (MarshalInFlight, RestoreInFlight): per variant key ("namespace/name"), the
// replicas still held and until when. Only what a hold needs is kept -- the
// replica's shape, how many replicas are to hold a node and how many do, the
// partial groups' placed pods -- not how to count the variant's pods, which
// the first cycle after a restart supplies again.
type PersistedInFlight struct {
	Version int                               `json:"version"`
	Entries map[string]PersistedInFlightEntry `json:"entries,omitempty"`
}

// PersistedInFlightEntry is one variant's replicas in flight.
type PersistedInFlightEntry struct {
	Accelerator string                  `json:"accelerator,omitempty"`
	PodGPUs     []int                   `json:"podGPUs,omitempty"`
	GPUs        int                     `json:"gpus"`
	Tolerations []corev1.Toleration     `json:"tolerations,omitempty"`
	DomainKey   string                  `json:"domainKey,omitempty"`
	Placements  []domain.PodPlacement   `json:"placements,omitempty"`
	Flavor      *kueue.FlavorPlacement  `json:"flavor,omitempty"`
	Want        int                     `json:"want"`
	Placed      int                     `json:"placed"`
	Expires     time.Time               `json:"expires"`
	TTLSeconds  int64                   `json:"ttlSeconds"`
	Partial     []domain.PartialReplica `json:"partial,omitempty"`
}

// MarshalInFlight is the ledger's live entries as of now, JSON, for the
// controller-status ConfigMap: every entry still holding a replica (before
// its expiry, fewer replicas placed than wanted), bounded by
// maxInFlightPersisted and maxInFlightBytes -- past them, the entries whose
// holds end soonest are left out.
func (l *InFlightLedger) MarshalInFlight(now time.Time) (string, error) {
	out := PersistedInFlight{Version: InFlightFormat}
	if l != nil {
		l.mu.Lock()
		live := map[string]PersistedInFlightEntry{}
		for k, e := range l.entries {
			if !now.Before(e.expires) {
				continue
			}
			placed := e.base
			for _, s := range e.seen {
				placed = max(placed, s.n)
			}
			if e.want <= placed {
				continue
			}
			pe := PersistedInFlightEntry{Accelerator: e.shape.Accelerator, PodGPUs: slices.Clone(e.shape.PodGPUs),
				GPUs: e.shape.GPUs, Tolerations: e.shape.Tolerations, DomainKey: e.shape.DomainKey,
				Placements: e.shape.Placements, Flavor: e.shape.Flavor, Want: e.want, Placed: placed,
				Expires: e.expires.UTC(), TTLSeconds: int64((e.ttl + time.Second - 1) / time.Second)}
			for _, p := range e.partial {
				pe.Partial = append(pe.Partial, domain.PartialReplica{Node: p.Node, PlacedGPUs: slices.Clone(p.PlacedGPUs)})
			}
			live[k] = pe
		}
		l.mu.Unlock()
		// The holds that end last are kept first; on a tie, by key.
		keys := slices.SortedFunc(maps.Keys(live), func(a, b string) int {
			return cmp.Or(live[b].Expires.Compare(live[a].Expires), cmp.Compare(a, b))
		})
		size := 0
		for _, k := range keys {
			if len(out.Entries) >= maxInFlightPersisted {
				break
			}
			raw, err := json.Marshal(live[k])
			if err != nil {
				return "", fmt.Errorf("in-flight ledger: %w", err)
			}
			if size += len(k) + len(raw); size > maxInFlightBytes {
				break
			}
			if out.Entries == nil {
				out.Entries = map[string]PersistedInFlightEntry{}
			}
			out.Entries[k] = live[k]
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("in-flight ledger: %w", err)
	}
	return string(raw), nil
}

// UnmarshalInFlight parses a persisted in-flight ledger. A ledger that is not
// JSON, or of another version than InFlightFormat, is an error: the caller
// ignores it. Fields it does not know are ignored.
func UnmarshalInFlight(s string) (PersistedInFlight, error) {
	var p PersistedInFlight
	if s == "" {
		return PersistedInFlight{Version: InFlightFormat}, nil
	}
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return PersistedInFlight{}, fmt.Errorf("in-flight ledger: %w", err)
	}
	if p.Version != InFlightFormat {
		return PersistedInFlight{}, fmt.Errorf("in-flight ledger: version %d, this build reads %d", p.Version, InFlightFormat)
	}
	return p, nil
}

// RestoreInFlight loads a persisted ledger after a restart, normally before
// the first node view is built. Each entry still within its hold (its expiry
// after now) is restored as it was persisted: the same replicas held, on the
// same clock -- the restart neither extends a hold nor ends it -- and a
// partial group's placed pods, so its other pods stay held in their domain.
// That is what keeps a replica whose pods are Unschedulable, or a
// LeaderWorkerSet group whose leader holds a node and whose workers do not,
// held across a restart: the first cycle after it only updates an entry it
// finds (InFlightAdmission.ExistingOnly). A restored entry counts no pods
// until a steady-state cycle admits its variant again and supplies the count.
//
// A variant the ledger already holds is left as it is: a restore that comes
// late never overwrites a newer entry. An entry past the ledger's bounds, or
// with a figure this controller does not write -- no "/" in its key, a want
// at or below its placed count or past maxLedgerReplicas, more partial groups
// than replicas held, a TTL under InFlightTTL -- is dropped; an expiry further
// than its TTL (at most maxInFlightTTL) from now is brought back to it. It
// returns how many entries it restored, and how many it dropped as out of
// bounds, for the caller to log.
func (l *InFlightLedger) RestoreInFlight(p PersistedInFlight, now time.Time) (restored, dropped int) {
	if l == nil {
		return 0, 0
	}
	keys := slices.Sorted(maps.Keys(p.Entries))
	if len(keys) > maxInFlightPersisted {
		dropped += len(keys) - maxInFlightPersisted
		keys = keys[:maxInFlightPersisted]
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		pe := p.Entries[k]
		if !persistedEntryOK(k, pe) {
			dropped++
			continue
		}
		if !now.Before(pe.Expires) {
			continue // its hold ended while the controller was down
		}
		if _, held := l.entries[k]; held {
			continue
		}
		ttl := time.Duration(min(pe.TTLSeconds, int64(maxInFlightTTL/time.Second))) * time.Second
		e := &inFlightEntry{
			shape: ReplicaShape{Variant: k, Accelerator: pe.Accelerator, PodGPUs: pe.PodGPUs, GPUs: pe.GPUs,
				Tolerations: pe.Tolerations, DomainKey: pe.DomainKey, Placements: pe.Placements, Flavor: pe.Flavor},
			want: pe.Want, base: pe.Placed, expires: minTime(pe.Expires, now.Add(ttl)), ttl: ttl,
		}
		// The placed pods are in any snapshot taken after the restart: seen
		// at the zero time, none is charged again (holdPartial).
		for _, r := range pe.Partial {
			e.partial = append(e.partial, partialSeen{PartialReplica: r, at: make([]time.Time, len(r.PlacedGPUs))})
		}
		l.entries[k] = e
		restored++
	}
	return restored, dropped
}

// persistedEntryOK reports whether a persisted entry keyed k holds only
// figures this controller writes (RestoreInFlight).
func persistedEntryOK(k string, pe PersistedInFlightEntry) bool {
	if !strings.Contains(k, "/") || pe.Placed < 0 || pe.Want <= pe.Placed || pe.Want > maxLedgerReplicas || pe.GPUs < 0 {
		return false
	}
	if pe.TTLSeconds < int64(InFlightTTL/time.Second) || len(pe.PodGPUs) > maxInFlightPods || len(pe.Partial) > pe.Want-pe.Placed {
		return false
	}
	return !slices.ContainsFunc(pe.Partial, func(r domain.PartialReplica) bool { return len(r.PlacedGPUs) > maxInFlightPods })
}

// minTime is the earlier of a and b.
func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

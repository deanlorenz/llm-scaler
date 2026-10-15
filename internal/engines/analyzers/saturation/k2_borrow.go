package saturation

import (
	"strings"
	"time"

	"github.com/go-logr/logr"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/logging"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// Borrowing a sibling's k2: a variant with no saturated reading of its own
// starting from one a compatible variant measured. See
// docs/proposals/learned-state-sharing-by-similarity.md.
//
// Two rules make it safe, and each answers one direction of being wrong.
//
// LOWER ONLY. The borrowed figure is used only where it is below what the
// variant would otherwise get (P3 derived, or P4 k1), so it can never raise
// capacity: whatever the donor measured, the variant orders at least as many
// replicas as it would without borrowing. A too-HIGH k2 is the expensive
// error -- fewer replicas, a saturated fleet, a TTFT tail that does not recover
// -- and this rule means borrowing cannot introduce one.
//
// IT EXPIRES. A too-LOW k2 does not correct itself. k2 is measured only at
// saturation (P1-obs needs the queue over its threshold), and a low k2 orders
// extra replicas, so each runs light, nothing saturates, nothing is measured,
// and the low figure also blocks the scale-down that would let a replica
// saturate. A donor's figure can be right for the donor and too low for the
// borrower, so it may not hold forever: after BorrowedK2Hold it relaxes
// linearly to the variant's own figure over BorrowedK2Relax. As it rises the
// fleet thins until one replica saturates and the variant measures k2 for
// itself, which displaces the borrow at once (P1, then P2 on its own window).
// The worst case of the relaxation is therefore the variant's no-borrow
// behaviour, reached hours later rather than at once.
//
// The clock is per BUCKET: one per own history key. A variant whose traffic
// moves to an output bucket it has never measured gets that bucket's own hold
// and relaxation, from the donor's figure FOR that bucket. Keyed per variant
// instead, a variant past its first 6.5 hours jumped straight to its own k1 in
// every new bucket -- measured in review at 3.2x the donor's figure, with no
// hold and no gradual rise -- which is the TTFT-direction gap this borrow
// exists to close. The cost of keying per bucket is the other direction: at
// most one hold per output bucket a variant passes through, extra replicas
// for a bounded time, which the k2 asymmetry ranks as the cheaper error.
//
// A clock is not reset by a donor that goes and comes back. It re-arms in two
// bounded ways, both deliberate: a retention after the relaxation ended the
// sweep drops it, so a variant that has still never measured that bucket
// borrows again -- a weekly re-probe -- and a controller restart drops it with
// every other learned figure, the donors' windows included, so nothing is
// borrowed until a donor has measured again.

const (
	// BorrowedK2Hold is how long a borrowed k2 is used at full strength
	// before it starts to relax: long enough to cover a canary's first hours,
	// when it most needs a measured figure and is least likely to saturate.
	BorrowedK2Hold = 30 * time.Minute
	// BorrowedK2Relax is how long the relaxation to the variant's own figure
	// takes once the hold is over. Slow on purpose: capacity rises by a
	// fraction of the gap per cycle, so the fleet thins a replica at a time
	// and the first saturation is a short, measured one rather than a cliff.
	BorrowedK2Relax = 6 * time.Hour
)

// k2Borrow is the outcome of borrowedK2 for one replica's decision.
type k2Borrow struct {
	k2      int64         // the figure to use: between the donor's and own
	donor   string        // the lending variant's name
	donorK2 int64         // what the donor measured
	relaxed float64       // 0 at full strength, 1 when fully relaxed to own
	age     time.Duration // how long this variant has been borrowing
	// prior is true when the figure is the variant's own, measured under the
	// engine arguments it ran before a restart found them changed
	// (installPriorsLocked); donor is then the variant itself.
	prior bool
}

// splitVariantKey splits a historyKey into its namespace, variant and the rest
// (model|accelerator|gpus|role|bucket|qN). No field contains "|", so the first
// two separators are the variant's.
func splitVariantKey(key string) (namespace, variant, rest string, ok bool) {
	parts := strings.SplitN(key, "|", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// borrowedK2 returns a sibling's measured k2 for a replica whose own window
// (ownKey) is empty, if one applies: a variant in the same namespace, on the
// same model, accelerator, GPU count, role, bucket and queue threshold, whose
// engine parameters are capacity-compatible (EngineParams.IsCapacityCompatible:
// the twelve fields that decide a replica's capacity, max-num-seqs and the
// batch-token budget among them), and whose figure is below own. Among
// several, the highest is taken: it is the least far below own, so the least
// replicas spent on a figure that may be someone else's.
//
// The donor's window is read without Touch: borrowing must not keep a donor's
// measurement alive past its own owner's use of it.
//
// own is the CAPACITY the variant would have without borrowing: min(k1, its
// own k2). Compared against its own k2 alone, a donor between k1 and a derived
// k2 above k1 was "lower", was reported as P2-borrow and started the clock,
// while capacity -- clamped to k1 either way -- did not move. ok is false when
// there is nothing to borrow, or the relaxation has run its course.
//
// armClock false is a row that may not record the variant's k2 (a warm-pool
// bridge, a Pod that is not Ready): it reads a borrow on the clock as it
// stands, and when no clock has started it reads the borrow at full hold
// without starting one. The hold is the variant's own replicas' to spend, not
// a bridge's.
func (a *SaturationAnalyzer) borrowedK2(ownKey, modelID string, own int64, ownParams *capacity.EngineParams, armClock bool) (k2Borrow, bool) {
	if own <= 0 || ownParams == nil {
		return k2Borrow{}, false
	}
	namespace, variant, rest, ok := splitVariantKey(ownKey)
	if !ok {
		return k2Borrow{}, false
	}

	type candidate struct {
		variant string
		k2      int64
	}
	var candidates []candidate
	a.mu.Lock()
	for key, ra := range a.computeCapacityHistory {
		ns, v, r, ok := splitVariantKey(key)
		if !ok || ns != namespace || v == variant || r != rest || ra.Len() == 0 {
			continue
		}
		if k2 := int64(ra.Average()); k2 > 0 && k2 < own {
			candidates = append(candidates, candidate{variant: v, k2: k2})
		}
	}
	// The variant's own figure from before its arguments changed. It needs
	// no compatibility check -- that the arguments differ is the point -- and
	// takes the same two rules as a sibling's: lower only, and it expires.
	var prior int64
	if p, ok := a.priorK2[ownKey]; ok && p.k2 > 0 && p.k2 < own {
		prior = p.k2
	}
	a.mu.Unlock()

	// The store has its own lock; checked outside a.mu.
	best := candidate{}
	for _, c := range candidates {
		rec := a.capacityStore.Get(namespace, modelID, c.variant)
		if rec == nil || !ownParams.IsCapacityCompatible(rec.EngineParams) {
			continue
		}
		if c.k2 > best.k2 || (c.k2 == best.k2 && c.variant < best.variant) {
			best = c
		}
	}
	fromPrior := false
	if prior > best.k2 {
		best, fromPrior = candidate{variant: variant, k2: prior}, true
	}
	if best.k2 == 0 {
		return k2Borrow{}, false
	}

	clockKey := ownKey
	now := a.now()
	a.mu.Lock()
	since, started := a.k2BorrowedSince[clockKey]
	if !started {
		since = now
		if armClock {
			a.k2BorrowedSince[clockKey] = since
		}
	}
	a.mu.Unlock()

	age := now.Sub(since)
	f := 0.0
	if age > BorrowedK2Hold {
		f = float64(age-BorrowedK2Hold) / float64(BorrowedK2Relax)
	}
	if f >= 1 {
		return k2Borrow{}, false
	}
	return k2Borrow{
		k2:      best.k2 + int64(f*float64(own-best.k2)),
		donor:   best.variant,
		donorK2: best.k2,
		relaxed: f,
		age:     age,
		prior:   fromPrior,
	}, true
}

// logBorrowedK2 is the k2-decision line for a borrowed figure. It names the
// donor and what the variant would have used instead, because the borrow is
// standing state, not a one-cycle event: a fleet holding more replicas than its
// load needs is explained by this line for as long as the borrow lasts.
func logBorrowedK2(logger logr.Logger, b k2Borrow, modelID, namespace, variantName, historyKey string, own int64, ownSource capacity.K2Source) {
	logger.V(logging.DEFAULT).Info("k2-decision",
		"modelID", modelID, "namespace", namespace, "variant", variantName,
		"priority", capacity.K2SrcBorrowed.String(), "historyKey", historyKey,
		"donor", b.donor, "donorK2", b.donorK2, "priorConfiguration", b.prior,
		"ownCapacity", own, "ownPriority", ownSource.String(),
		"relaxed", b.relaxed, "borrowAgeSeconds", int64(b.age.Seconds()),
		"k2", b.k2)
}

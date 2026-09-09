package aggregation

// This file establishes the "undefined value" convention used by every
// coverage/N/replica helper in this package from here on (spec §2.5, A14):
// coverage and N are meaningless when PRC or demand is zero, and that
// meaninglessness must be represented explicitly rather than as a number.
//
// The convention is the plain (value float64, ok bool) pair already used by
// DemandForRole — no new named type. A pair keeps every helper's signature
// self-describing at the call site and composes directly with Go's multiple
// return values, without forcing callers to unwrap a struct first. Later
// helpers (Agg_N, Agg_Spare, ...) return this same shape; none of them
// redefines it.
//
// The rule this exists to enforce: a skipped/undefined contribution must
// never silently enter a min or max as a magic number. Feeding it in as 0
// would make a min (e.g. an all-agree spare-capacity gate) wrongly veto a
// scale-down; feeding it in as +Inf would make a max (e.g. Agg_N) wrongly
// demand infinite replicas. maxOfDefined and minOfDefined below are the one
// shared, swappable place every aggregator's combination rule goes through,
// per A18 — so every aggregator in this package skips undefined contributors
// the same way, and a future change to the combination rule is contained to
// these two functions.

// maxOfDefined returns the maximum of the defined (ok == true) values a
// generator function yields for each of n items, skipping every undefined
// one. ok is false when no item yielded a defined value at all — a max over
// nothing is itself undefined, never a magic 0 or -Inf.
func maxOfDefined(n int, at func(i int) (value float64, ok bool)) (value float64, ok bool) {
	var best float64
	found := false
	for i := 0; i < n; i++ {
		v, defined := at(i)
		if !defined {
			continue
		}
		if !found || v > best {
			best = v
			found = true
		}
	}
	return best, found
}

// minOfDefined returns the minimum of the defined (ok == true) values a
// generator function yields for each of n items, skipping every undefined
// one. ok is false when no item yielded a defined value at all — a min over
// nothing is itself undefined, never a magic 0 or +Inf.
func minOfDefined(n int, at func(i int) (value float64, ok bool)) (value float64, ok bool) {
	var best float64
	found := false
	for i := 0; i < n; i++ {
		v, defined := at(i)
		if !defined {
			continue
		}
		if !found || v < best {
			best = v
			found = true
		}
	}
	return best, found
}

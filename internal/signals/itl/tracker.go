package itl

import (
	"maps"
	"math"
	"sync"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// The tiers a Tracker's line comes from.
const (
	// TierOLS is a full least-squares fit: the window spans enough of k.
	TierOLS = "ols"
	// TierPinnedB is a fit of A alone, with B pinned to the key's last full
	// fit or, before it has one, to DefaultBaselineSec.
	TierPinnedB = "pinned-B"
)

// Tracker keeps, per key, the rolling window of (k, ITL) readings a line is
// fitted from and the intercept of the key's last full fit. It is the one copy
// of that bookkeeping: which replica rows are readings, when the window is
// enough for a full fit, and what B the fallback pins. Every analyzer that
// fits ITL(k) uses it, so a fix to any of it reaches all of them.
//
// The caller names the key, and stamps and sweeps it on its own clock: what
// counts as "the variant is still there" belongs to the caller.
//
// Safe for concurrent use.
type Tracker struct {
	mu        sync.Mutex
	windows   map[string]*Window
	baselines map[string]float64
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{windows: map[string]*Window{}, baselines: map[string]float64{}}
}

// Reading is what one Observe saw and fitted, for the caller to log and use.
type Reading struct {
	// Model is the fitted line; zero when there is none (Tier "").
	Model Model
	// Tier is TierOLS, TierPinnedB, or "" when no line could be fitted.
	Tier string
	// Declined is set when the window held enough samples but the pinned fit
	// refused them.
	Declined bool
	// BaselineStored reports whether the key holds an intercept from an
	// earlier full fit. BaselineLearned is that intercept being positive, so
	// the pinned B is this key's measurement rather than DefaultBaselineSec.
	BaselineStored, BaselineLearned bool

	// What the window was offered this cycle, by why a row was turned away.
	Considered, NotReady, NoITL, NoK, AboveBand, Offered int
	// Held is the window's size after pruning, Ready its own confidence gate.
	Held  int
	Ready bool
}

// Observe offers one cycle's replica rows of a variant to the key's window and
// fits ITL(k) = A*k + B over it.
//
// A row is a reading when it is the variant's, Ready and not lent by the warm
// pool, with a finite positive ITL and a k in (0, DefaultMaxObservableK]; the window
// itself drops a k below DefaultMinObservableK. A full fit is taken when the
// window is Ready, and its B is remembered when positive. Otherwise, with at
// least DefaultMinSamples readings, A alone is fitted with B pinned to the
// remembered intercept, or DefaultBaselineSec before there is one: a fleet the
// router balances spans little of k, so this is the normal case, not a
// degenerate one.
func (t *Tracker) Observe(key, variant string, rows []domain.ReplicaMetrics, now time.Time) Reading {
	var r Reading
	t.mu.Lock()
	w, ok := t.windows[key]
	if !ok {
		w = NewWindow(DefaultWindowMaxSize, DefaultObservationMaxAge, DefaultMinSamples,
			DefaultMinKSpread, DefaultMinObservableK, DefaultMaxObservableK)
		t.windows[key] = w
	}
	for _, rm := range rows {
		if rm.VariantName != variant {
			continue
		}
		r.Considered++
		switch {
		case !rm.Ready || rm.FromWarmPool:
			r.NotReady++
		case !(rm.AvgITL > 0) || math.IsInf(rm.AvgITL, 1):
			// +Inf passes "> 0": a rate over a window with no tokens in it.
			r.NoITL++
		case !(rm.KvUsageInstant > 0):
			r.NoK++
		case rm.KvUsageInstant > DefaultMaxObservableK:
			r.AboveBand++
		default:
			w.Add(rm.KvUsageInstant, rm.AvgITL, now)
			r.Offered++
		}
	}
	w.Prune(now)
	obs := w.Observations()
	r.Held, r.Ready = len(obs), w.Ready()
	baseline, stored := t.baselines[key]
	t.mu.Unlock()

	r.BaselineStored = stored
	r.BaselineLearned = stored && baseline > 0
	if !r.BaselineLearned {
		baseline = DefaultBaselineSec
	}
	// The fits run outside the lock: they read a copy of the window.
	if r.Ready {
		if m, ok := Fit(obs); ok {
			// B, not A, is remembered: B is the hardware floor, A the
			// contention that moves with the workload. And only a physical,
			// positive floor -- a negative B pinned into the next fit would
			// be worse than the constant it replaced.
			if m.B > 0 {
				t.mu.Lock()
				t.baselines[key] = m.B
				t.mu.Unlock()
			}
			r.Model, r.Tier = m, TierOLS
			return r
		}
	}
	// The sample floor still applies when the spread is waived: the pinned
	// fit answers from a single pair, and a line should not rest on one
	// reading from one replica on one cycle.
	if len(obs) < DefaultMinSamples {
		return r
	}
	m, ok := FitPinnedB(obs, baseline)
	if !ok {
		r.Declined = true
		return r
	}
	r.Model, r.Tier = m, TierPinnedB
	return r
}

// Sweep prunes every window and forgets the window and intercept of each key
// stale reports true for. It returns how many of each it forgot.
func (t *Tracker) Sweep(now time.Time, stale func(key string) bool) (windows, baselines int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, w := range t.windows {
		w.Prune(now)
	}
	for key := range t.windows {
		if stale(key) {
			delete(t.windows, key)
			windows++
		}
	}
	// Apart from the windows: a key can hold an intercept with no window, one
	// restored from a snapshot before the variant was seen again.
	for key := range t.baselines {
		if stale(key) {
			delete(t.baselines, key)
			baselines++
		}
	}
	return windows, baselines
}

// Baselines returns a copy of every key's remembered intercept.
func (t *Tracker) Baselines() map[string]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return maps.Clone(t.baselines)
}

// Baseline returns the key's remembered intercept.
func (t *Tracker) Baseline(key string) (float64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.baselines[key]
	return b, ok
}

// RestoreBaseline sets the key's intercept unless it already holds one: what
// was learned since the process started wins over what a snapshot carried.
func (t *Tracker) RestoreBaseline(key string, b float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, have := t.baselines[key]; !have {
		t.baselines[key] = b
	}
}

// SetBaseline sets the key's intercept.
func (t *Tracker) SetBaseline(key string, b float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.baselines[key] = b
}

// WindowLens returns, for every key holding a window, how many readings it
// holds.
func (t *Tracker) WindowLens() map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]int, len(t.windows))
	for key, w := range t.windows {
		out[key] = w.Len()
	}
	return out
}

// ForgetBaselines drops every remembered intercept and keeps the windows: the
// readings are this process's own, the intercepts may have been restored.
func (t *Tracker) ForgetBaselines() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.baselines)
}

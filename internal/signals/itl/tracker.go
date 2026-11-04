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
	// steady holds, per key, only the readings taken while their replica's
	// load had held still, and is the only window the full fit -- and so the
	// remembered B -- is taken from. See DefaultSteadyFor.
	steady map[string]*Window
	traces map[string]*keyTraces
	// slopes holds, per key, the A of its last line, which the pinned fit
	// holds while too few readings are past their settle to fit a new one,
	// or the window too few readings at all; with when it was fitted and
	// under which engine arguments, which bound the hold (Admission).
	slopes map[string]heldSlope
	// above and aboveSteady hold, per key, the readings above the band that
	// were not admitted, and the steady ones among them, for the fallback
	// line (Admission.AboveBandFallback). Empty for a key whose admission
	// never asked for it.
	above, aboveSteady map[string]*Window
}

// heldSlope is a key's last fitted A, when it was fitted, and the engine
// arguments (Admission.Args) its readings were taken under.
type heldSlope struct {
	a        float64
	fittedAt time.Time
	args     string
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{
		windows:     map[string]*Window{},
		baselines:   map[string]float64{},
		steady:      map[string]*Window{},
		traces:      map[string]*keyTraces{},
		slopes:      map[string]heldSlope{},
		above:       map[string]*Window{},
		aboveSteady: map[string]*Window{},
	}
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
	// Offered counts the readings the window kept; BelowBand those it dropped
	// for a k below the band's lower edge (an idle replica).
	Considered, NotReady, NoITL, NoK, AboveBand, BelowBand, Offered int
	// Capped counts the Offered readings above the band's upper edge, taken
	// because their replica was at its sequence cap (Admission.atSequenceCap).
	Capped int
	// Unsteady counts the Offered readings kept out of the full fit because
	// their replica's load had moved within DefaultSteadyFor.
	Unsteady int
	// Settling counts the Unsteady readings whose replica was inside its
	// settle -- it joined, or the load came back to it, within
	// DefaultSteadyFor -- and which the pinned fit leaves out too.
	Settling int
	// SlopeHeld is set when the pinned fit had too few readings past their
	// settle to fit, or the window too few readings at all, and Model's A is
	// the key's last one.
	SlopeHeld bool
	// SlopeFittedAt is when the held A was fitted; zero unless SlopeHeld.
	SlopeFittedAt time.Time
	// AboveBandFit is set when Model is the above-band fallback
	// (Admission.AboveBandFallback): the band gave no line, the key held no
	// slope it may hold, and A was fitted from the readings above the band.
	AboveBandFit bool
	// PinnedAtLeastITL is set when Model is the pinned fit's line through
	// the least ITL of the window's readings at a k of at least DefaultKSat,
	// not through the default baseline (Admission.OneSidedAboveKSat): it
	// priced the higher ITL at DefaultKSat.
	PinnedAtLeastITL bool
	// AboveBandHeld is the above-band window's size after pruning; zero
	// without the fallback.
	AboveBandHeld int
	// AboveBandUnderKSat counts the AboveBand readings at a k under
	// DefaultKSat: above the band, and too low for the fallback to keep.
	AboveBandUnderKSat int
	// Held is the window's size after pruning.
	Held int
	// SteadyHeld is how many steady readings the full fit may use, and Ready
	// is the confidence gate judged on those alone: a spread made of a
	// transient is not a spread. It can exceed Held: a steady reading
	// outlives the main window by up to DefaultSteadyCarry.
	SteadyHeld int
	Ready      bool
	// SteadySpread is max k - min k over the steady readings: the other half
	// of Ready, and how far it is from DefaultMinKSpread.
	SteadySpread float64
}

// SlopeAge is how long ago the held A was fitted, zero when none is held:
// what tells a held line from a fit made this cycle in a log.
func (r Reading) SlopeAge(now time.Time) time.Duration {
	if !r.SlopeHeld || r.SlopeFittedAt.IsZero() {
		return 0
	}
	return now.Sub(r.SlopeFittedAt)
}

// Observe offers one cycle's replica rows of a variant to the key's window and
// fits ITL(k) = A*k + B over it.
//
// A row's k is domain.ReplicaMetrics.KvUsageForITL: the KV fill averaged over
// the minute its ITL rate covers when the collector read that, the instant
// fill otherwise. Every use of k below -- the band, the steady gate, the
// reading -- is that one figure. An instant k against a minute's ITL, with
// the batch moving inside the minute, flattened the fitted slope and raised
// the remembered B (issue #393).
//
// A row is a reading when it is the variant's, Ready and not lent by the warm
// pool, with a finite positive ITL and a k inside adm's band (by default
// [DefaultMinObservableK, DefaultMaxObservableK]). A k above the band is a
// reading too when the replica is at its sequence cap (Admission.atSequenceCap);
// the zero Admission knows no cap and admits nothing above it. The band is
// judged per call, so a key's window may hold readings from a band the policy
// has since narrowed; they age out with the window. A full fit is taken when the
// window is Ready, and its B is remembered when positive. Otherwise, with at
// least DefaultMinSamples readings, A alone is fitted with B pinned to the
// remembered intercept, or DefaultBaselineSec before there is one: a fleet the
// router balances spans little of k, so this is the normal case, not a
// degenerate one.
//
// The full fit is judged and taken on steady readings only: those whose
// replica's k had not moved by more than DefaultMaxKDrift within
// DefaultSteadyFor, from a replica that has been serving at least that long.
// In a balanced fleet the only spread in k a window ever sees is a transient
// -- a replica joining, or the load stepping -- and across a transient k and
// ITL describe different moments, so the line through them is not ITL(k).
// Its intercept was remembered and pinned into every fit after it: on one
// run, two identical models in two namespaces kept B = 4.5 ms and 14.8 ms
// from whichever transient each window happened to catch, and priced the
// same replica at 5.3 and 7.7 requests/s (issue #270). The steady readings
// outlive the window by up to DefaultSteadyCarry, so a step's old level is
// still there when the new one settles, and the two give the spread.
//
// The pinned fit takes the steady readings too while there are at least
// DefaultMinSamples of them, and every reading only below that, so a fleet
// that has not settled yet still gets a line. A load step is a transient the
// pinned fit would otherwise take whole: on the issue #270 rerun A jumped from
// 0.077 to 0.207 on the cycle the load stepped, with every new reading
// unsteady and 10 steady ones held, and the derived mu fell from 6.4 to 2.65
// requests/s for two and a half minutes. It does not take the carry: see
// pinnedReadings. Taking every reading, it still leaves out those of a replica
// inside its settle -- a joiner's, or one the load came back to -- whose ITL
// covers its warm-up, and while fewer than DefaultMinSettledSamples remain it
// holds the key's last A. It holds the last A too once the window has fewer
// than DefaultMinSamples readings left -- a fleet below the band ages its
// readings out and offers none -- so a key that has fitted keeps a line through
// light load, or above the band (SlopeHeld says so, SlopeFittedAt when the A
// was fitted). Both holds end once the A is older than adm.SlopeHoldFor or was
// fitted under other engine arguments than adm.Args.
func (t *Tracker) Observe(key, variant string, rows []domain.ReplicaMetrics, adm Admission, now time.Time) Reading {
	var r Reading
	lo, hi := adm.band()
	s := adm.Steady.withDefaults()
	t.mu.Lock()
	w, ok := t.windows[key]
	if !ok {
		// The windows span the whole cache: the band is the Admission's,
		// applied below, where a capped replica's reading may pass it.
		w = newKeyWindow(DefaultWindowMaxSize)
		t.windows[key] = w
	}
	sw, ok := t.steady[key]
	if !ok {
		sw = newKeyWindow(DefaultSteadyWindowMaxSize)
		t.steady[key] = sw
	}
	traces, ok := t.traces[key]
	if !ok {
		traces = &keyTraces{since: now, replicas: map[string]*replicaTrace{}}
		t.traces[key] = traces
	}
	var aw, asw *Window
	if adm.AboveBandFallback {
		aw, asw = t.aboveWindows(key)
	}
	seen := make(map[string]bool, len(rows))
	for _, rm := range rows {
		if rm.VariantName != variant {
			continue
		}
		r.Considered++
		// The k paired with the row's ITL: over the same minute as its rate
		// when the collector read that (issue #393).
		k := rm.KvUsageForITL()
		// Judged before the switch and noted after it, so a row that is no
		// reading -- k below the band, no ITL yet -- still says where the
		// replica's load is.
		steady, settling := false, false
		// The same for the above-band window, which does not wait after a
		// controller restart (aboveBandSettling).
		aboveSteady, aboveSettling := false, false
		hasITL := adm.hasITL(rm)
		var trace *replicaTrace
		if rm.Ready && !rm.FromWarmPool {
			seen[rm.PodName] = true
			trace = traces.replicas[rm.PodName]
			// Back, on the key's own trace, from an absence -- cycles of the
			// key that went without it -- longer than the tolerance: it
			// starts over, as one missing from a cycle did before
			// (KeepsThroughMissingRow). On the tracker, the tracker judges
			// the absence.
			if trace != nil && adm.KeepsThroughMissingRow && adm.Settle == nil && trace.missed &&
				now.Sub(trace.lastSeen) > adm.absenceTolerance() {
				trace = nil
			}
			if trace == nil {
				first := !now.After(traces.since)
				trace = &replicaTrace{since: now, joined: !first || adm.WaitsAfterRestart,
					restartWait: first && adm.WaitsAfterRestart}
				traces.replicas[rm.PodName] = trace
			}
			trace.lastSeen, trace.missed = now, false
			restartOnly := false
			if adm.Settle == nil {
				trace.serving(hasITL, traceable(k), now, s)
				settling = trace.settling(now, s)
				restartOnly = settling && trace.restartWait
			} else {
				trace.countIdle(hasITL, traceable(k), now, s)
				settling, restartOnly = trace.settlingOnTracker(adm, rm, traces.since, now, s)
			}
			aboveSettling = aboveBandSettling(settling, restartOnly && !adm.AboveBandWaitsAfterRestart, rm, now, s)
			if traceable(k) {
				still := trace.still(k, now, s)
				steady, aboveSteady = !settling && still, !aboveSettling && still
			} else {
				// Present, so its trace is kept; no k, so nothing is noted.
				trace = nil
			}
		}
		switch {
		case !rm.Ready || rm.FromWarmPool:
			r.NotReady++
		case !hasITL:
			// No ITL to pair (Admission.hasITL). +Inf passes "> 0": a rate
			// over a window with no tokens in it.
			r.NoITL++
		case !(k > 0):
			r.NoK++
		case k < lo:
			r.BelowBand++
		case k > hi && !adm.atSequenceCap(rm):
			r.AboveBand++
			if !atOrPastKSat(k) {
				r.AboveBandUnderKSat++
			}
			// Kept for the fallback only, stamped as a reading in the band
			// is, and only at a k of at least the one the line is priced at
			// (fitAboveBand: what makes the fallback one-sided).
			if aw == nil || !atOrPastKSat(k) {
				break
			}
			dropped := aw.add(Observation{K: k, ITLSec: rm.AvgITL, Timestamp: now, settling: aboveSettling})
			if !dropped && aboveSteady {
				asw.Add(k, rm.AvgITL, now)
			}
		default:
			// Add drops nothing the cases above let through but a k above a
			// full cache, which the collector never reports; counted as
			// above the band all the same.
			if w.add(Observation{K: k, ITLSec: rm.AvgITL, Timestamp: now, settling: settling}) {
				r.AboveBand++
			} else {
				r.Offered++
				if k > hi {
					r.Capped++
				}
				switch {
				case steady:
					sw.Add(k, rm.AvgITL, now)
				case settling:
					r.Unsteady++
					r.Settling++
				default:
					r.Unsteady++
				}
			}
		}
		if trace != nil {
			trace.note(k, now, s)
		}
	}
	// A replica missing from this cycle starts over when it returns: what it
	// served in between is unknown. With KeepsThroughMissingRow, only one
	// missing past the tolerance does (above), or, on the tracker, one the
	// tracker starts again; its trace is kept until the tracker would have
	// forgotten it. A row that is not Ready, or is lent by the warm pool,
	// still starts it over.
	notServing := make(map[string]bool, len(rows))
	for _, rm := range rows {
		if rm.VariantName == variant && (!rm.Ready || rm.FromWarmPool) && !seen[rm.PodName] {
			notServing[rm.PodName] = true
		}
	}
	for pod, rt := range traces.replicas {
		switch {
		case seen[pod]:
		case !adm.KeepsThroughMissingRow || notServing[pod] || now.Sub(rt.lastSeen) > adm.forgetAfter():
			delete(traces.replicas, pod)
		default:
			rt.missed = true
		}
	}
	obs, steadyObs := pruneWithCarry(w, sw, now, s.Carry)
	var aboveObs, aboveSteadyObs []Observation
	if aw != nil {
		aboveObs, aboveSteadyObs = pruneWithCarry(aw, asw, now, s.Carry)
		r.AboveBandHeld = len(aboveObs)
	}
	r.Held, r.Ready, r.SteadyHeld, r.SteadySpread = len(obs), sw.Ready(), len(steadyObs), sw.KSpread()
	baseline, stored := t.baselines[key]
	t.mu.Unlock()

	r.BaselineStored = stored
	r.BaselineLearned = stored && baseline > 0
	if !r.BaselineLearned {
		baseline = DefaultBaselineSec
	}
	// The fits run outside the lock: they read a copy of the window.
	c := chooseFit(obs, steadyObs, r.Ready, baseline, s.MinSettledSamples)
	if c.tier == TierOLS {
		// B is what is remembered as the key's: B is the hardware floor,
		// A the contention that moves with the workload, kept only for
		// the pinned fit to hold through a settle. And only a physical,
		// positive floor -- a negative B pinned into the next fit would
		// be worse than the constant it replaced.
		t.mu.Lock()
		if c.model.B > 0 {
			t.baselines[key] = c.model.B
		}
		t.slopes[key] = heldSlope{a: c.model.A, fittedAt: now, args: adm.Args}
		t.mu.Unlock()
		r.Model, r.Tier = c.model, TierOLS
		return r
	}
	if c.hold {
		// Too few readings to fit, for one of two reasons, and the last A
		// holds through both.
		//
		// The window is full of replicas still settling: a scale-up's
		// joiners, or a fleet the load just came back to. Their readings
		// are the transient, and what remains is too little to price the
		// fleet, so the last A holds until they settle.
		//
		// Or the window has run short: its readings aged out past
		// DefaultObservationMaxAge and nothing replaced them. That is what
		// light load does -- a replica below the band's lower edge offers no
		// reading at all -- so a fleet that learned its line at load and
		// then sat below the band for half an hour lost the line, the
		// derived mu with it, and was sized by KV occupancy alone, which at
		// light load reads far below the throughput share it serves
		// (issue #284). The key's last A is the newest evidence of its line
		// there is -- the mu derived from it is still priced at the shape
		// arriving now -- and a fresh fit replaces it as soon as the window
		// holds DefaultMinSamples again. A key that never fitted has no A
		// and still gets no line, unless the admission asks for the
		// above-band fallback (aboveBandLine). The same holds above the
		// band: a replica there that is not at its sequence cap offers no
		// reading to the band either.
		//
		// Not for ever. An A fitted longer ago than adm.SlopeHoldFor, or
		// under other engine arguments than adm.Args, is not held: the hold
		// ends where a finished fit is no longer trusted elsewhere, and a
		// different engine is not the one the slope was measured on.
		t.mu.Lock()
		held, have := t.slopes[key]
		t.mu.Unlock()
		if !have || !adm.mayHold(held, now) {
			return aboveBandLine(r, aboveObs, aboveSteadyObs, s)
		}
		r.Model, r.Tier, r.SlopeHeld = Model{A: held.a, B: baseline}, TierPinnedB, true
		r.SlopeFittedAt = held.fittedAt
		return r
	}
	if c.declined {
		r.Declined = true
		return r
	}
	m, heldA := c.model, c.model.A
	if adm.OneSidedAboveKSat && !r.BaselineLearned {
		if lo, ok := atLeastITLLine(c.read); ok && lo.ITLAt(DefaultKSat) > m.ITLAt(DefaultKSat) {
			// The hold pairs its A with the baseline (above), so the A held
			// is the one that prices the chosen line's ITL at DefaultKSat
			// through it: a held line prices what this one did.
			m, r.PinnedAtLeastITL = lo, true
			heldA = (m.ITLAt(DefaultKSat) - baseline) / DefaultKSat
		}
	}
	t.mu.Lock()
	t.slopes[key] = heldSlope{a: heldA, fittedAt: now, args: adm.Args}
	t.mu.Unlock()
	r.Model, r.Tier = m, TierPinnedB
	return r
}

// atLeastITLLine is the line pinned at the least ITL among obs's readings at
// a k of at least DefaultKSat, fitted over those readings alone
// (fitAboveBand): the above-band fallback's line, over the readings of the
// band's own window that lie at or past where the line is priced. ok is
// false when there is none.
//
// Why the pinned fit needs it (Admission.OneSidedAboveKSat). A key with no
// learned B pins DefaultBaselineSec, and a fit through B0 over readings at
// k above DefaultKSat prices at kSat, on readings of the true line ITL = A'k
// + B', A'kSat + B' + (B0 - B')(1 - kSat/kbar): under the truth whenever B'
// is above B0. Readings at a replica's sequence cap sit there. Pinned at the
// least ITL of those readings instead, which is at least B' (every ITL is B'
// + A'k plus a noise small against A'k), the same term is >= 0
// (fitAboveBand), so the higher of the two lines at kSat never prices a
// higher mu than the truth, on readings without noise. With the gauge's
// noise the margin, (b - B')(1 - kSat/kbar), can be smaller than the noise
// on the readings' mean: under 1 ms on a shallow line at a cap near kSat,
// where it measured at most 0.5 % under the truth at 2 % noise, within
// known truth's 5 %.
//
// One reading at or past kSat is enough. Through a single reading the line
// is flat at its ITL (A 0), and that ITL is B' + A'k with k >= kSat, so at
// kSat it is at least the truth: one reading errs the same way, only by
// more. The window's floors (DefaultMinSamples, MinSettledSamples) have
// already been met by the pinned fit it is set against, and the higher of
// the two is taken, so a lone reading can only raise the price.
func atLeastITLLine(obs []Observation) (Model, bool) {
	var past []Observation
	for _, o := range obs {
		if atOrPastKSat(o.K) {
			past = append(past, o)
		}
	}
	if len(past) == 0 {
		return Model{}, false
	}
	return fitAboveBand(past, aboveBandB(past))
}

// atOrPastKSat reports whether a reading at fill k lies at or past
// DefaultKSat, where the line is priced: the floor both one-sided lines
// read from (the above-band window, and atLeastITLLine over the band's
// window), since a reading under it turns their margin against the truth
// (fitAboveBand).
func atOrPastKSat(k float64) bool { return k >= DefaultKSat }

// fitChoice is chooseFit's outcome: a line at a tier, or hold (too few
// settled readings: Observe holds the key's last slope, or tries the
// above-band fallback), or declined (the pinned fit refused its readings).
// read is the readings the fit was taken from.
type fitChoice struct {
	model          Model
	tier           string
	hold, declined bool
	read           []Observation
}

// chooseFit is Observe's choice of fit over a key's readings, in one place
// so that a refit of the same readings (Refit) makes the same choice: the
// full fit on the steady readings when they are ready, and otherwise, or
// when the full fit fails, the pinned fit at baseline on pinnedReadings.
//
// The sample floor still applies when the spread is waived: the pinned fit
// answers from a single pair, and a line should not rest on one reading
// from one replica on one cycle.
func chooseFit(obs, steady []Observation, ready bool, baseline float64, minSettled int) fitChoice {
	if ready {
		if m, ok := Fit(steady); ok {
			return fitChoice{model: m, tier: TierOLS, read: steady}
		}
	}
	pinned, settled := pinnedReadings(obs, steady)
	if !settled || len(pinned) < minSettled {
		return fitChoice{hold: true}
	}
	m, ok := FitPinnedB(pinned, baseline)
	if !ok {
		return fitChoice{declined: true, read: pinned}
	}
	return fitChoice{model: m, tier: TierPinnedB, read: pinned}
}

// Refitted is a line fitted again from readings a key's windows held, by the
// tracker's own choice of fit (Refit, RefitAboveBand).
type Refitted struct {
	// Model is the line, zero with none.
	Model Model
	// Tier is TierOLS or TierPinnedB, "" with no line.
	Tier string
	// Hold is set where Observe would hold the key's last slope: too few
	// settled readings to fit.
	Hold bool
	// Read is the readings the line was fitted from.
	Read []Observation
}

// Refit reruns Observe's choice of fit (chooseFit) on readings a key's
// windows held: obs the main window's and steady the steady window's, each
// in time order, as Readings returns them. baseline is the intercept the
// pinned fit pins, and minSettled the steady gate's MinSettledSamples (0 is
// the default). The steady readings are ready as the steady window judges
// them: DefaultMinSamples of them, spread DefaultMinKSpread. Nothing is
// remembered: the what-if planner bands a line by refitting resamples of
// its readings (internal/signals/whatif, BootstrapLines).
func Refit(obs, steady []Observation, baseline float64, minSettled int) Refitted {
	if minSettled <= 0 {
		minSettled = DefaultMinSettledSamples
	}
	c := chooseFit(obs, steady, ready(steady, DefaultMinSamples, DefaultMinKSpread), baseline, minSettled)
	return Refitted{Model: c.model, Tier: c.tier, Hold: c.hold, Read: c.read}
}

// RefitAboveBand reruns the above-band fallback (aboveBandLine) on the
// readings the above-band windows held. Tier is TierPinnedB when it fits.
func RefitAboveBand(obs, steady []Observation, minSettled int) Refitted {
	if minSettled <= 0 {
		minSettled = DefaultMinSettledSamples
	}
	m, read, ok := aboveBandFit(obs, steady, minSettled)
	if !ok {
		return Refitted{}
	}
	return Refitted{Model: m, Tier: TierPinnedB, Read: read}
}

// pruneWithCarry prunes a window and its steady one, and returns what each
// then holds. A steady reading outlives the main window's oldest by at most
// carry (DefaultSteadyCarry): long enough for the old level to still be
// there when a step's new level settles, and no longer, because A moves with
// the workload and a steady window kept for its own sake would keep fitting
// a line the fleet has since moved off.
func pruneWithCarry(w, sw *Window, now time.Time, carry time.Duration) (obs, steady []Observation) {
	w.Prune(now)
	obs = w.Observations()
	sw.Prune(now)
	cutoff := now.Add(-carry)
	if len(obs) > 0 && obs[0].Timestamp.Before(cutoff) {
		cutoff = obs[0].Timestamp
	}
	sw.dropBefore(cutoff)
	return obs, sw.Observations()
}

// aboveWindows is the key's above-band window and its steady one, made on
// first use. Called with t.mu held.
func (t *Tracker) aboveWindows(key string) (all, steady *Window) {
	all, ok := t.above[key]
	if !ok {
		// The band is the Admission's: these windows take what lies above it.
		all = newKeyWindow(DefaultWindowMaxSize)
		t.above[key] = all
	}
	steady, ok = t.aboveSteady[key]
	if !ok {
		steady = newKeyWindow(DefaultSteadyWindowMaxSize)
		t.aboveSteady[key] = steady
	}
	return all, steady
}

// newKeyWindow is one of a key's windows, holding up to size readings. It
// spans the whole cache, [0, 1]: the band is the Admission's, applied in
// Observe, where a capped replica's reading may pass it and a reading above
// it may go to the above-band window.
func newKeyWindow(size int) *Window {
	return NewWindow(size, DefaultObservationMaxAge, DefaultMinSamples, DefaultMinKSpread, 0, 1)
}

// aboveBandLine is r with the above-band fallback's line, when obs and
// steady -- the key's readings above the band at a k of at least
// DefaultKSat, and the steady ones among them -- give one by the pinned
// fit's own rules: pinnedReadings, at least DefaultMinSamples readings and
// s.MinSettledSamples past their settle. Observe calls it only when the band
// gave no line and the key holds no slope it may hold, so it never replaces
// an in-band fit or a held slope, and the first in-band fit replaces it. Its
// A is not remembered, and its B never becomes the key's intercept: a later
// hold holds only a slope fitted in the band, and a later pinned fit pins
// only a B a full fit measured.
//
// The case it is for: a key that has never fitted, under load that holds
// every replica above the band -- a controller restart into saturation, or a
// variant that starts there. The band then never gets its readings, and the
// line was silent for as long as the load stayed up; with the throughput
// analyzer alone, nothing ordered the scale-up that would bring the fleet
// back into the band.
//
// It errs on one side only, by construction: see fitAboveBand.
func aboveBandLine(r Reading, obs, steady []Observation, s Steadiness) Reading {
	m, _, ok := aboveBandFit(obs, steady, s.MinSettledSamples)
	if !ok {
		return r
	}
	r.Model, r.Tier, r.AboveBandFit = m, TierPinnedB, true
	return r
}

// aboveBandFit is the above-band fallback's line and the readings it read,
// ok false when there is none.
func aboveBandFit(obs, steady []Observation, minSettled int) (Model, []Observation, bool) {
	pinned, settled := pinnedReadings(obs, steady)
	if !settled || len(pinned) < minSettled {
		return Model{}, nil, false
	}
	m, ok := fitAboveBand(pinned, aboveBandB(pinned))
	if !ok {
		return Model{}, nil, false
	}
	return m, pinned, true
}

// aboveBandB is the B the above-band fallback pins: the least ITL among its
// readings, the strict minimum. Zero when there is no finite positive one.
//
// Every reading is ITL = B' + A k + noise, with A >= 0 and k > 0, so the
// least ITL is at least the true B' up to the noise -- and above the band the
// noise is small against A k (a 2 % gauge on 50 ms is 1 ms, A k is 40 ms or
// more), so one low reading does not bring it under B'. A low quantile would
// guard against an outlier the gauge does not produce, at the cost of a B
// nearer B'; the strict minimum is kept.
func aboveBandB(obs []Observation) float64 {
	b := math.Inf(1)
	for _, o := range obs {
		if o.ITLSec > 0 && !math.IsInf(o.ITLSec, 0) && o.K > 0 && !math.IsInf(o.K, 0) {
			b = min(b, o.ITLSec)
		}
	}
	if math.IsInf(b, 1) {
		return 0
	}
	return b
}

// fitAboveBand is the above-band fallback's line through b: A = sum(ITL_i -
// b) / sum(k_i), the pinned fit's k-weighted slope (FitPinnedB). Unlike
// FitPinnedB it takes A = 0: with b the least ITL, readings that all share
// one ITL give a flat line at that ITL, which is still a price (see below),
// where FitPinnedB refuses a flat fit as noise.
//
// Why it errs on one side only. With b at or above the true B' (aboveBandB)
// and every reading at k_i >= DefaultKSat (Observe keeps no other), the line
// priced at kSat = DefaultKSat is, on readings of the true line ITL = A' k +
// B',
//
//	b + kSat (ITLbar - b) / kbar = A' kSat + B' + (b - B')(1 - kSat/kbar)
//
// with kbar the readings' mean k and ITLbar their mean ITL. Both factors of
// the last term are >= 0, so the priced ITL is at least the true line's at
// kSat: never a higher mu than the truth. A replica pushed past the band reads
// above the line (it queues; its batch is past where the line was fitted),
// which raises ITLbar and the price with it. The k floor is what makes the
// second factor >= 0: the band's top, 0.80 by default, lies under kSat (0.85),
// and a reading between them would turn the term against the truth.
//
// The term is also the margin that absorbs the gauge's noise: b, the least
// ITL, sits far above B' (about 48 ms against 4 or 10 on the throughput
// property case, which measured the priced ITL at least 2 % over the truth).
// Pinned at DefaultBaselineSec instead, as the band's fit pins a key that
// never fitted, b is below a true B' of 10 ms and the term goes negative:
// that case then prices under the truth on every seed.
//
// No clamp on A is needed: with b the least ITL, every ITL_i - b is >= 0,
// and a sum of non-negative floats is non-negative. A b passed below that
// (the tests' negative control pins DefaultBaselineSec) still gives A >= 0
// on a fleet above the band; a negative A is refused all the same.
func fitAboveBand(obs []Observation, b float64) (Model, bool) {
	a, ok := pinnedSlope(obs, b)
	if !ok || !(a >= 0) {
		return Model{}, false
	}
	return Model{A: a, B: b}, true
}

// DefaultMinSettledSamples is the fewest readings, from replicas past their
// settle, the pinned fit is taken from on the every-reading path; below it the
// key's last A holds. The window's DefaultMinSamples still has to be met
// first: this floor is for what is left once the settling replicas' readings
// are set aside, and three is one cycle of a three-replica fleet or three of a
// lone replica -- not one reading of one replica on one cycle.
// It is the default of analyzerTuning.steadyGate.minSettledSamples.
const DefaultMinSettledSamples = 3

// pinnedReadings is what the pinned fit reads: the steady readings the main
// window spans, never more than it holds, reaching back into the carry only
// as far as DefaultMinSamples needs, and every reading of the main window when
// fewer than DefaultMinSamples are steady at all. Both windows are in time
// order.
//
// The carry is the full fit's: it keeps a step's old level until the new one
// settles, which B needs and A does not. A moves with the workload, and
// fitted on the carried window it followed a heavier shape at the same fill
// twice as slowly -- two replicas took 17 cycles to price the new A, against 8
// on the main window alone, and a minute in priced 0.090 for a true 0.12, so
// the derived mu read about 30% high. Through a load step the main
// window's steady readings run short, and the newest DefaultMinSamples, the
// old level's, hold A until the new level settles.
//
// Fewer than DefaultMinSamples are steady on a saturated fleet nearly always:
// k swings between above the band and the dips into it by more than
// DefaultMaxKDrift, and on a two-namespace run the steady readings numbered 0
// on almost every cycle. Taking every reading there took each settling
// replica's warm-up too, so those are set aside by the replica's state when
// the reading was taken, not by its value: see FitPinnedB. ok is false when
// neither window holds DefaultMinSamples, the floor the fit has always had.
func pinnedReadings(obs, steady []Observation) (pinned []Observation, ok bool) {
	if len(steady) < DefaultMinSamples {
		if len(obs) < DefaultMinSamples {
			return nil, false
		}
		pinned = make([]Observation, 0, len(obs))
		for _, o := range obs {
			if !o.settling {
				pinned = append(pinned, o)
			}
		}
		return pinned, true
	}
	start := len(steady)
	for start > 0 && len(obs) > 0 && len(steady)-start < len(obs) &&
		!steady[start-1].Timestamp.Before(obs[0].Timestamp) {
		start--
	}
	return steady[min(start, len(steady)-DefaultMinSamples):], true
}

// Sweep prunes every window and forgets the window and intercept of each key
// stale reports true for. It returns how many of each it forgot.
//
// stale runs under the tracker's lock: it must be cheap and must not call back
// into the tracker. What it reads of the caller's state is the caller's to
// guard -- both analyzers call Sweep holding their own lock.
func (t *Tracker) Sweep(now time.Time, stale func(key string) bool) (windows, baselines int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, w := range t.windows {
		w.Prune(now)
	}
	for _, m := range []map[string]*Window{t.steady, t.above, t.aboveSteady} {
		for _, w := range m {
			w.Prune(now)
		}
	}
	for key := range t.windows {
		if stale(key) {
			delete(t.windows, key)
			windows++
		}
	}
	// The steady window, the traces and the held slope are part of the
	// window: counted with it, not apart.
	for key := range t.steady {
		if stale(key) {
			delete(t.steady, key)
		}
	}
	for key := range t.traces {
		if stale(key) {
			delete(t.traces, key)
		}
	}
	for key := range t.slopes {
		if stale(key) {
			delete(t.slopes, key)
		}
	}
	for key := range t.above {
		if stale(key) {
			delete(t.above, key)
			delete(t.aboveSteady, key)
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

// ClearWindow drops the readings filed under key, the steady ones and those
// above the band too, and keeps the key's intercept, its held slope and its
// replicas' settle traces: the next fit is made from new readings only, and
// until there are enough the held slope is the line, as on light load. It
// reports whether the key held any readings.
func (t *Tracker) ClearWindow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	had := false
	for _, m := range []map[string]*Window{t.windows, t.steady, t.above, t.aboveSteady} {
		if w, ok := m[key]; ok {
			had = had || w.Len() > 0
			w.Clear()
		}
	}
	return had
}

// ForgetTraces drops the replicas' settle traces of every key match reports
// true for, and keeps their windows, intercepts and held slopes: the next
// Observe of such a key reads every replica as on the key's first, so a
// settle that began before it is a settle only with
// Admission.WaitsAfterRestart, and the drift test starts with no history.
// It returns how many keys' traces it dropped. match runs under the
// tracker's lock, as Sweep's stale does.
func (t *Tracker) ForgetTraces(match func(key string) bool) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for key := range t.traces {
		if match(key) {
			delete(t.traces, key)
			n++
		}
	}
	return n
}

// ForgetSlope drops the key's last A, so no slope is held for it until a
// new one is fitted, and keeps its windows and intercept: ForgetSlopes for
// one key.
func (t *Tracker) ForgetSlope(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.slopes, key)
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

// Slope is a key's last fitted A, with when and under which engine arguments
// (Admission.Args) it was fitted: what Observe holds through light load, in a
// form a snapshot can carry across a restart.
type Slope struct {
	A        float64
	FittedAt time.Time
	Args     string
}

// Slopes returns a copy of every key's last fitted A.
func (t *Tracker) Slopes() map[string]Slope {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]Slope, len(t.slopes))
	for key, s := range t.slopes {
		out[key] = Slope{A: s.a, FittedAt: s.fittedAt, Args: s.args}
	}
	return out
}

// ForgetSlopes drops every key's last A, fitted or restored, and keeps the
// windows, which fit a new one.
func (t *Tracker) ForgetSlopes() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.slopes)
}

// RestoreSlope sets the key's last A unless it already holds one: what was
// fitted since the process started wins over what a snapshot carried. The
// hold it allows is bounded as a live one is, from FittedAt and by Args
// (Admission).
func (t *Tracker) RestoreSlope(key string, s Slope) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, have := t.slopes[key]; !have {
		t.slopes[key] = heldSlope{a: s.A, fittedAt: s.FittedAt, args: s.Args}
	}
}

// SetBaseline sets the key's intercept.
func (t *Tracker) SetBaseline(key string, b float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.baselines[key] = b
}

// WindowLen is how many readings the window filed under key holds, 0 when there
// is none.
func (t *Tracker) WindowLen(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if w, ok := t.windows[key]; ok {
		return w.Len()
	}
	return 0
}

// Newest is when the newest reading of the window filed under key was taken,
// zero when it holds none.
func (t *Tracker) Newest(key string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if w, ok := t.windows[key]; ok && len(w.observations) > 0 {
		return w.observations[len(w.observations)-1].Timestamp
	}
	return time.Time{}
}

// KeyReadings is a copy of the readings filed under one key, each window in
// time order: what Refit and RefitAboveBand refit a line from.
type KeyReadings struct {
	// All is every reading the main window holds, and Steady the steady
	// ones the full fit is taken from (a steady reading may outlive the main
	// window by up to DefaultSteadyCarry).
	All, Steady []Observation
	// Above and AboveSteady are the above-band fallback's windows: empty for
	// a key whose admission never asked for it.
	Above, AboveSteady []Observation
}

// Readings is a copy of the readings filed under key; every window is nil
// for a key that has none. Read-only: the what-if planner bands a line by
// refitting it from these (internal/signals/whatif, BootstrapLines).
func (t *Tracker) Readings(key string) KeyReadings {
	t.mu.Lock()
	defer t.mu.Unlock()
	return KeyReadings{
		All: observationsOf(t.windows[key]), Steady: observationsOf(t.steady[key]),
		Above: observationsOf(t.above[key]), AboveSteady: observationsOf(t.aboveSteady[key]),
	}
}

// observationsOf is a copy of w's readings, nil for no window.
func observationsOf(w *Window) []Observation {
	if w == nil {
		return nil
	}
	return w.Observations()
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

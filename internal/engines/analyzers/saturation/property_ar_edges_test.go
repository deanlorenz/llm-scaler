package saturation

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// The source-switching thresholds at the gauges' autocorrelation (2.6, edges
// by enumeration): the mu latch's return ratio, the weighing's sibling
// tolerance, the output-key band and the shape tracker's tolerance. edge3
// holds each at one instant either side; here a noise-free path crosses the
// threshold once (one excursion), AR(1) noise rides on it, and the learner's
// source may change at most once per excursion. Where the learner computes a
// value from its source, the value is also held Lipschitz in its input
// between switches.

// arPath is a noise-free generator path: from for hold cycles, a straight
// ramp over ramp cycles, then the end value (to) for rest cycles.
func arPath(from, to float64, hold, ramp, rest int) []float64 {
	out := make([]float64, 0, hold+ramp+rest)
	for range hold {
		out = append(out, from)
	}
	for i := 1; i <= ramp; i++ {
		out = append(out, from+(to-from)*float64(i)/float64(ramp))
	}
	for range rest {
		out = append(out, to)
	}
	return out
}

// valueChanges counts how often v changed along a trajectory.
func valueChanges(v []float64) int {
	n := 0
	for i := 1; i < len(v); i++ {
		if v[i] != v[i-1] {
			n++
		}
	}
	return n
}

func arEdgeCases() []propCase {
	ret := tuningWith(func(t *config.AnalyzerTuning) { t.MuChoice.ReturnRatio = 1.3 })
	return []propCase{
		{class: classEdges, learner: "mu", name: "latch-return-excursion", family: familyDirect, tuning: tunedDefault,
			seeds: cheapSeeds, check: checkLatchReturnExcursion(tuningDefault)},
		{class: classEdges, learner: "mu", name: "latch-return-excursion-nondefault", family: familyDirect, tuning: tunedPolicy,
			policy: tuningPolicy(ret), seeds: cheapSeeds, check: checkLatchReturnExcursion(ret)},
		{class: classEdges, learner: "mu", name: "sibling-tolerance-excursion", family: familyDirect, tuning: tunedDefault,
			check: checkWeighExcursion(weighSigma)},
		{class: classEdges, learner: "k2", name: "key-bucket-excursion", family: familyDirect, tuning: tunedDefault,
			check: checkKeyBucketExcursion(false, nil)},
		{class: classEdges, learner: "mu", name: "key-bucket-excursion", family: familyDirect, tuning: tunedDefault,
			check: checkKeyBucketExcursion(true, nil)},
		{class: classEdges, learner: "shape-hold", name: "tolerance-excursion", family: familyDirect, tuning: tunedDefault,
			seeds: cheapSeeds, check: checkShapeExcursion(0)},
	}
}

// --- mu: the latch's return ratio ---

// Latch noise: 4 % of the ratio, lag-one correlation 0.9 at the 15 s cycle
// (a rate[1m] read every 15 s shares 45 s of its minute with the last).
const (
	latchSigma = 0.04
	latchPhi   = 0.9
)

// checkLatchReturnExcursion: a stale measurement latched past maxRatio of the
// derived mu (2.5x; 0.4x on the low side) comes back across returnRatio
// (1.6x; 1/1.6) once, as a 30 min ramp to 0.75 of it, or as a step onto it
// held for 40 min. The latch's set point is maxRatio, so once it lets go the
// noise at returnRatio cannot set it again: the choice changes at most once
// per excursion. On the ramp it must change: a latch that never returns
// passes the reversal bound for the wrong reason. The choice is a source,
// measured or derived, and the price on each is that figure unchanged, so
// there is no value to bound between the switches.
func checkLatchReturnExcursion(tn config.AnalyzerTuning) func(uint64) error {
	return func(seed uint64) error {
		r := propRand(seed)
		t := tuning{AnalyzerTuning: tn.WithDefaults()}
		maxR, retR := t.MuChoice.MaxRatio, t.MuChoice.ReturnRatio
		d := whatif.DerivedMu{Rate: 1, OK: true}
		// Sampled long before: stale either side, past lowHoldFor below.
		sampled := propStart.Add(-2 * time.Hour)
		for _, side := range []struct {
			name string
			// inv puts the path on the low side: 1/x of the high side's.
			inv  bool
			ramp bool
		}{{"high, ramp", false, true}, {"high, on the return ratio", false, false}, {"low, ramp", true, true}, {"low, on the return ratio", true, false}} {
			path := arPath(1.25*maxR, 0.75*retR, 8, 120, 40)
			if !side.ramp {
				path = arPath(1.25*maxR, retR, 8, 0, 160)
			}
			a := NewSaturationAnalyzer(capacity.NewStore())
			noise := arNoise(r, len(path), latchSigma, latchPhi)
			v := make([]bool, len(path))
			for i, x := range path {
				if side.inv {
					x = 1 / x
				}
				now := propStart.Add(time.Duration(i) * propCycle)
				v[i] = a.measuredYieldsToDerived(propMuKey, throughputReading{rate: x * (1 + noise[i]), sampledAt: sampled}, d, 0, t, now)
			}
			if !v[0] {
				return violation(kindNotDetected, "%s: a stale measurement at %.2fx the derived mu did not yield", side.name, path[0])
			}
			if n := reversals(v); n > 1 {
				return violation(kindFlipped, "%s (maxRatio %.2f, returnRatio %.2f, noise %.0f%% phi %.2f): the choice reversed %d times in %d cycles, first at cycles %v",
					side.name, maxR, retR, 100*latchSigma, latchPhi, n, len(v), flipCycles(v, 4))
			}
			if side.ramp && v[len(v)-1] {
				return violation(kindStale, "%s: the ramp ended at %.2fx and the measurement still yields", side.name, path[len(path)-1])
			}
		}
		return nil
	}
}

// flipCycles is the first n cycles at which v changed.
func flipCycles(v []bool, n int) []int {
	var out []int
	for i := 1; i < len(v) && len(out) < n; i++ {
		if v[i] != v[i-1] {
			out = append(out, i)
		}
	}
	return out
}

// --- mu: the weighing's sibling tolerance ---

// Weighing noise: 4 % on the queued replica's rate, lag-one correlation 0.9.
const (
	weighSigma = 0.04
	weighPhi   = 0.9
)

// weighSample is the newest sample of the rig's one measured-mu window and
// the readers' mean it was formed from; ok is false before the first.
func (r *propRig) weighSample() (sample, read float64, ok bool) {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	for key, ra := range r.a.saturatedThroughput {
		reads := r.a.throughputPodReads[key]
		if ra.Len() == 0 || len(reads.pods) == 0 {
			continue
		}
		return ra.Last(), meanOf(reads.pods), true
	}
	return 0, 0, false
}

// checkWeighExcursion: one replica queued with a full cache beside three
// unqueued at half their cache serving 0.6 (counted at 0.6 / 0.85, h), as
// TestMuSlowPod_TheCreditReachesTheSample. The queued replica's rate over h
// ramps from 0.95 to 0.65 over 30 min, crossing siblingMuTolerance (0.8)
// once. Under it the sample counts the three unread replicas
// (weighUnreadReplicas); at or over it the sample is the reader alone. The
// source changes at most once per excursion, and between changes the sample
// moves no more than the reading it is formed from (the weighing's slope is
// a quarter, the reader's own is one). sigma is the noise's (controls).
//
// A ledger row (#495): the tolerance has no band, so the source changes on
// the noise for as long as the reader sits near it, 17 of 20 seeds.
func checkWeighExcursion(sigma float64) func(uint64) error {
	return func(seed uint64) error {
		r := propRand(seed)
		const served = 0.6
		h := served / floorTarget
		path := arPath(0.95, 0.65, 8, 120, 40)
		noise := arNoise(r, len(path), sigma, weighPhi)
		rig := newPropRig()
		var weighed []bool
		var samples, reads []float64
		for i, x := range path {
			slow := x * h * (1 + noise[i])
			if _, err := rig.analyze(slowPodRows(4, 1, slow, served, 0.5), aggStates(4), slow+3*served, propCycle); err != nil {
				return err
			}
			sample, read, ok := rig.weighSample()
			if !ok {
				if i < 4 {
					continue
				}
				return violation(kindNotLearned, "cycle %d: no sample", i)
			}
			weighed = append(weighed, sample > read*(1+1e-9))
			samples, reads = append(samples, sample), append(reads, read)
		}
		if n := reversals(weighed); n > 1 {
			return violation(kindFlipped, "the reader's rate over its unread siblings ramped 0.95 -> 0.65 across siblingMuTolerance 0.8 (noise %.0f%%, phi %.2f): the sample's source reversed %d times in %d cycles, first at cycles %v; the sample stepped %.3f -> %.3f of h at the first",
				100*sigma, weighPhi, n, len(weighed), flipCycles(weighed, 4), firstFlipFrom(weighed, samples)/h, firstFlipTo(weighed, samples)/h)
		}
		if !weighed[len(weighed)-1] {
			return violation(kindNotDetected, "the reader ended at 0.65 of its unread siblings and the sample does not count them")
		}
		for i := 1; i < len(samples); i++ {
			if weighed[i] == weighed[i-1] && math.Abs(samples[i]-samples[i-1]) > math.Abs(reads[i]-reads[i-1])+1e-9 {
				return violation(kindFlipped, "cycle %d: the sample stepped %.4f on a reading step of %.4f, one source", i, samples[i]-samples[i-1], reads[i]-reads[i-1])
			}
		}
		return nil
	}
}

// firstFlipFrom is the sample before v's first change, NaN with none.
func firstFlipFrom(v []bool, s []float64) float64 {
	if c := flipCycles(v, 1); len(c) > 0 {
		return s[c[0]-1]
	}
	return math.NaN()
}

// firstFlipTo is the sample at v's first change, NaN with none.
func firstFlipTo(v []bool, s []float64) float64 {
	if c := flipCycles(v, 1); len(c) > 0 {
		return s[c[0]]
	}
	return math.NaN()
}

// --- k2 and mu: the output-key band ---

// Key noise: 6 % on the output length, lag-one correlation 0.9, as
// edges/k2/key-near-output-boundary.
const (
	keySigma = 0.06
	keyPhi   = 0.9
)

// keyRampStart is the first cycle of keyExcursionPath's first ramp. Changes
// are counted from it: before it the path has not moved, and the band is
// choosing its starting bucket from its first readings (the first-sample
// class, not this one).
const keyRampStart = 40

// keyExcursionPath is the output length: 560 for 10 min, down to 440 over
// 20 min, 440 for 20 min, back up to 560 over 20 min, 560 for 30 min. Two
// crossings of the 500-token medium/long boundary; split is the first
// cycle of the second excursion.
func keyExcursionPath() (path []float64, split int) {
	down := arPath(560, 440, keyRampStart, 80, 80)
	return append(down, arPath(440, 560, 0, 80, 120)...), len(down)
}

// checkKeyBucketExcursion: the noise-free output length crosses the 500-token
// boundary down and back up (keyExcursionPath). The selected key (the k2 key,
// or with mu the throughput key) changes at most once in each excursion, ends
// the first in "medium" and the second in "long". The key is a source: the
// window it names, so there is no value to bound between the switches. wrap
// edits each row and edit the analyzer before the first cycle (controls).
func checkKeyBucketExcursion(mu bool, edit func(*SaturationAnalyzer), wrap ...func(*domain.ReplicaMetrics)) func(uint64) error {
	return func(seed uint64) error {
		path, split := keyExcursionPath()
		noise := arNoise(propRand(seed), len(path), keySigma, keyPhi)
		lengths := make([]float64, len(path))
		for i, x := range path {
			lengths[i] = x * (1 + noise[i])
		}
		var w func(*domain.ReplicaMetrics)
		if len(wrap) > 0 {
			w = wrap[0]
		}
		k2Keys, muKeys, err := selectedKeys(lengths, w, edit)
		if err != nil {
			return err
		}
		keys, which := k2Keys, "k2"
		if mu {
			keys, which = muKeys, "mu"
		}
		if !strings.Contains(keys[keyRampStart], "|long|") {
			return violation(kindWrongSide, "after 10 min at 560 tokens the %s key is %s, want \"long\"", which, keys[keyRampStart])
		}
		for _, ex := range []struct {
			from, to int
			want     string
		}{{keyRampStart, split, "|medium|"}, {split - 1, len(keys), "|long|"}} {
			seg := keys[ex.from:ex.to]
			for i, k := range seg {
				if k == "" {
					return violation(kindNotLearned, "cycle %d selected no %s key", ex.from+i, which)
				}
			}
			n := 0
			for i := 1; i < len(seg); i++ {
				if seg[i] != seg[i-1] {
					n++
				}
			}
			if n > 1 {
				return violation(kindFlipped, "output length across the 500-token boundary (noise %.0f%%, phi %.2f), cycles %d-%d: the %s key changed %d times",
					100*keySigma, keyPhi, ex.from, ex.to-1, which, n)
			}
			if last := seg[len(seg)-1]; !strings.Contains(last, ex.want) {
				return violation(kindNotDetected, "cycles %d-%d: the %s key ended on %s, want %q", ex.from, ex.to-1, which, last, ex.want)
			}
		}
		return nil
	}
}

// --- shape-hold: the tracker's 20 % tolerance ---

// Shape noise: 4 % on the output length, lag-one correlation 0.9, as
// checkShapeWobble.
const (
	shapeSigma = 0.04
	shapePhi   = 0.9
)

// checkShapeExcursion: the first reading is 1000 tokens out (the tracker's
// anchor, so the band is 800-1200), then the noise-free output length leaves
// it once: up to 1225 over 30 min, down to 775, or a step onto 1200 held for
// 40 min. The key's output length (stableOut) changes at most once per
// excursion, and on the ramps it changes. Between changes it is constant: the
// key holds its shape. tol, when non-zero, is the tracker's tolerance in
// place of the production one (controls).
//
// A ledger row (#495): the anchor is the one reading that crossed, so a tail
// reading after it moves the key again, 1 of 100 seeds (5 of 1000).
func checkShapeExcursion(tol float64) func(uint64) error {
	return func(seed uint64) error {
		r := propRand(seed)
		for _, ex := range []struct {
			name string
			path []float64
			ramp bool
		}{
			{"up to 1225", arPath(1000, 1225, 8, 120, 120), true},
			{"down to 775", arPath(1000, 775, 8, 120, 120), true},
			{"on 1200", arPath(1000, 1200, 8, 0, 160), false},
		} {
			rig := newShapeRig()
			if tol > 0 {
				rig.a.fleetShape["ns|m"] = &shapeMemo{tracker: shape.NewTracker(tol)}
			}
			noise := arNoise(r, len(ex.path), shapeSigma, shapePhi)
			noise[0] = 0
			keys := make([]float64, len(ex.path))
			for i, x := range ex.path {
				keys[i], _, _ = rig.note(1000, x*(1+noise[i]), ShapeChangeHoldMax)
			}
			if n := valueChanges(keys); n > 1 {
				return violation(kindFlipped, "%s (noise %.0f%%, phi %.2f): the key's output length changed %d times in %d cycles: %s",
					ex.name, 100*shapeSigma, shapePhi, n, len(keys), keyTrail(keys, 5))
			}
			if ex.ramp && keys[len(keys)-1] == keys[0] {
				return violation(kindNotDetected, "%s: the key never left %.0f", ex.name, keys[0])
			}
		}
		return nil
	}
}

// keyTrail is the first n values v took, in order.
func keyTrail(v []float64, n int) string {
	out := []string{fmt.Sprintf("%.0f", v[0])}
	for i := 1; i < len(v) && len(out) < n; i++ {
		if v[i] != v[i-1] {
			out = append(out, fmt.Sprintf("%.0f@%d", v[i], i))
		}
	}
	return strings.Join(out, " -> ")
}

// --- negative controls ---

// arControlSeeds is the seeds a control runs: each must fail on at least one,
// and the test logs how many.
func arControlSeeds(n int) []uint64 {
	seeds := make([]uint64, n)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	return seeds
}

// arControl runs check over n seeds and fails the test when none fails.
func arControl(t *testing.T, what string, check func(uint64) error, n int) {
	t.Helper()
	run, err := runCase(check, arControlSeeds(n))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Failures) == 0 {
		t.Fatalf("%s: no seed failed; the case does not see the flap", what)
	}
	t.Logf("%s: %s", what, run)
}

// With returnRatio at maxRatio, an operator setting validation admits, the
// latch has no band: the ratio is judged on maxRatio each way, and the noise
// on it reverses the choice.
func TestPropertyARLatchReturn_NoBandFlaps(t *testing.T) {
	noBand := tuningWith(func(t *config.AnalyzerTuning) { t.MuChoice.ReturnRatio = config.DefaultMeasuredDerivedMaxRatio })
	arControl(t, "returnRatio = maxRatio", checkLatchReturnExcursion(noBand), 20)
}

// The output-key band with its smoothing removed (every cycle restarts it,
// so the smoothed length is the reading): the 3 % margin is inside the 6 %
// noise, and the key flaps across the boundary.
func TestPropertyARKeyBucket_UnsmoothedFlaps(t *testing.T) {
	for _, mu := range []bool{false, true} {
		var a *SaturationAnalyzer
		edit := func(an *SaturationAnalyzer) { a = an }
		unsmoothed := func(*domain.ReplicaMetrics) {
			a.mu.Lock()
			defer a.mu.Unlock()
			for _, m := range a.fleetShape {
				m.k2Key.n = 0
			}
		}
		arControl(t, fmt.Sprintf("unsmoothed band, mu key %v", mu), checkKeyBucketExcursion(mu, edit, unsmoothed), 20)
	}
}

// The weighing's case is a ledger row: it fails at the default noise. Its
// control runs the same path without noise, where the source changes once
// and the sample stays Lipschitz in the reading: what fails is the noise at
// the tolerance, not the rig.
func TestPropertyARWeigh_NoiseFreeChangesOnce(t *testing.T) {
	run, err := runCase(checkWeighExcursion(0), arControlSeeds(3))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Failures) != 0 {
		t.Fatalf("the noise-free excursion across siblingMuTolerance: %s", run)
	}
	t.Logf("noise-free: %s", run)
}

// The shape tracker at a 2 % tolerance, inside the 4 % noise: the anchor
// moves on noise and the key with it.
func TestPropertyARShapeTolerance_NarrowToleranceFlaps(t *testing.T) {
	arControl(t, "shape tolerance 0.02", checkShapeExcursion(0.02), 20)
}

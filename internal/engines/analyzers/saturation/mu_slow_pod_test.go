package saturation

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/internal/learnertest"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// The two rules that bound one slow pod (mu_slow_pod.go): the unread
// replica's credit (SwitchMuUnreadAtTarget) and the minority's figure
// expiring (SwitchMuMinorityFigureExpires). The closed-loop probe holds them
// together (TestClosedLoopProbe_ASlowReplicaInsideTheServedBoundStaysUnderTheBoundAndEndsAtNeed);
// these hold each edge alone, each run on and off.

// slowPodRows is n replicas of variant v at 1000 in, 1000 out: the first
// readers queued with a full cache, serving slow, and the rest unqueued at
// fill kv, serving rate.
func slowPodRows(n, readers int, slow, rate, kv float64) []domain.ReplicaMetrics {
	rows := make([]domain.ReplicaMetrics, 0, n)
	for i := range n {
		if i < readers {
			rows = append(rows, imbRow(fmt.Sprintf("r%d", i), "v", 0.95, 10, 1000, 1000, slow))
			continue
		}
		rows = append(rows, imbRow(fmt.Sprintf("r%d", i), "v", kv, 0, 1000, 1000, rate))
	}
	return rows
}

// slowPodRig is a rig with switches set.
func slowPodRig(t *testing.T, switches map[string]bool) *propRig {
	t.Helper()
	rig := newPropRig()
	require.NoError(t, rig.a.SetSwitchesForReplay(switches))
	return rig
}

// An unread replica under the KV line counts at what it serves over the
// target; one with a full cache, at what it serves; and a target outside
// (0, 1), the switch off, credits nothing.
func TestMuSlowPod_AnUnreadReplicaUnderTheKVLineCountsAtWhatItServesOverTheTarget(t *testing.T) {
	served := servedRates{pods: map[string]servedPod{
		"slow": {rate: 0.4, queued: true, full: true},
		"open": {rate: 0.6},
		"full": {rate: 0.9, full: true},
	}}
	reads := map[string]float64{"slow": 0.4}
	for _, c := range []struct {
		target, want float64
	}{
		{0.85, (0.6/0.85 + 0.9) / 2},
		{0, (0.6 + 0.9) / 2},
		{1, (0.6 + 0.9) / 2},
		{1.2, (0.6 + 0.9) / 2},
	} {
		s := served
		s.target = c.target
		got, n := s.unread(reads)
		require.Equal(t, 2, n)
		require.InDelta(t, c.want, got, 1e-12, "target %v", c.target)
	}
}

// Through Analyze: one replica queued at 0.4 beside three unqueued at half
// their cache serving 0.6. The sample counts the three at 0.6 / 0.85, so mu
// is (0.4 + 3 x 0.706) / 4 = 0.629. With the credit off it counts them at
// 0.6, 0.550: the floor would then charge the target on a rate they were
// only offered.
func TestMuSlowPod_TheCreditReachesTheSample(t *testing.T) {
	for _, c := range []struct {
		on   bool
		want float64
	}{
		{true, (0.4 + 3*0.6/floorTarget) / 4},
		{false, (0.4 + 3*0.6) / 4},
	} {
		rig := slowPodRig(t, map[string]bool{SwitchMuUnreadAtTarget: c.on})
		for range 13 {
			_, err := rig.analyze(slowPodRows(4, 1, 0.4, 0.6, 0.5), aggStates(4), 2.2, propCycle)
			require.NoError(t, err)
		}
		got := rig.measuredMu()
		t.Logf("credit %v: mu %.4f, want %.4f", c.on, got, c.want)
		require.InDelta(t, c.want, got, 1e-3*c.want, "credit %v", c.on)
	}
}

// The writers are a minority when fewer than half of the replicas that read
// the newest sample and the unqueued ones that did not; a queued replica that
// did not read it counts on neither side.
func TestMuSlowPod_TheWritersAreAMinorityOfTheReadAndUnqueued(t *testing.T) {
	queued := servedPod{rate: 1, queued: true, full: true}
	open := servedPod{rate: 1}
	for _, c := range []struct {
		name     string
		pods     map[string]servedPod
		reads    []string
		minority bool
	}{
		{"one of four read", map[string]servedPod{"a": queued, "b": open, "c": open, "d": open}, []string{"a"}, true},
		{"two of four read: half is no minority", map[string]servedPod{"a": queued, "b": queued, "c": open, "d": open}, []string{"a", "b"}, false},
		{"every replica read: saturated together", map[string]servedPod{"a": queued, "b": queued, "c": queued, "d": queued}, []string{"a", "b", "c", "d"}, false},
		{"a queued replica not read counts on neither side", map[string]servedPod{"a": queued, "b": queued, "c": queued, "d": open}, []string{"a"}, false},
	} {
		a := NewSaturationAnalyzer(capacity.NewStore())
		reads := podReads{pods: map[string]float64{}}
		for _, p := range c.reads {
			reads.pods[p] = 1
		}
		a.throughputMuChoice[propMuKey] = &muChoice{}
		a.throughputPodReads[propMuKey] = reads
		a.noteSampleWriters(propMuKey, servedRates{pods: c.pods}, muSampled)
		require.Equal(t, c.minority, a.throughputMuChoice[propMuKey].minority, c.name)
	}
}

// A minority's figure with no line expires once nothing has read saturated
// in agreement with it for lowHoldFor, not a cycle before; and never with a
// line (the hold already releases it to the derived figure), with the
// fleet's figure, with no choice on record (a restore: the safe side), or
// with the switch off.
func TestMuSlowPod_AMinorityFigureExpiresOnlyPastTheHold(t *testing.T) {
	tn := tuning{AnalyzerTuning: tuningDefault.WithDefaults()}
	hold := tn.MuChoice.LowHoldFor.Duration()
	at := propStart
	reading := throughputReading{rate: 0.55, sampledAt: at, writtenAt: at}
	expired := func(on, minority, choice, derivedOK bool, now time.Time) bool {
		a := NewSaturationAnalyzer(capacity.NewStore())
		require.NoError(t, a.SetSwitchesForReplay(map[string]bool{SwitchMuMinorityFigureExpires: on}))
		if choice {
			a.throughputMuChoice[propMuKey] = &muChoice{minority: minority, saturatedAt: at}
		}
		return a.minorityFigureExpired(propMuKey, reading, derivedOK, tn, now)
	}
	require.False(t, expired(true, true, true, false, at.Add(hold-propCycle)), "inside the hold")
	require.True(t, expired(true, true, true, false, at.Add(hold)), "past the hold")
	require.False(t, expired(true, true, true, true, at.Add(2*hold)), "a line prices it")
	require.False(t, expired(true, false, true, false, at.Add(2*hold)), "the fleet wrote it")
	require.False(t, expired(true, true, false, false, at.Add(2*hold)), "no choice on record")
	require.False(t, expired(false, true, true, false, at.Add(2*hold)), "the switch off")
}

// slowPodEpisode runs a fleet of four through Analyze: readers queued at
// 0.55 beside the rest unqueued at 0.6 for 3 min, then all four unqueued at
// 0.4 of their cache serving 0.6 for 40 min, and returns when the minority's
// figure first expired, from the end of the saturated part (negative when it
// never did), and the mu on record at the end.
func slowPodEpisode(t *testing.T, readers int, switches map[string]bool) (time.Duration, float64) {
	t.Helper()
	rig := slowPodRig(t, switches)
	for range 12 {
		_, err := rig.analyze(slowPodRows(4, readers, 0.55, 0.6, 0.5), aggStates(4), 2.4, propCycle)
		require.NoError(t, err)
	}
	quietFrom := rig.clock
	first := time.Duration(-1)
	for rig.clock.Sub(quietFrom) < 40*time.Minute {
		elapsed := rig.clock.Sub(quietFrom)
		_, err := rig.analyze(slowPodRows(4, 0, 0, 0.6, 0.4), aggStates(4), 2.4, propCycle)
		require.NoError(t, err)
		if first < 0 && muCount(rig.a, muReasonMinorityExpired) > 0 {
			first = elapsed
		}
	}
	return first, rig.measuredMu()
}

// One replica of four wrote the figure: it expires 30 min (lowHoldFor) after
// its last saturated reading, and with the switch off it never does.
func TestMuSlowPod_OneReplicasFigureExpiresAfterTheHold(t *testing.T) {
	hold := tuningDefault.WithDefaults().MuChoice.LowHoldFor.Duration()
	first, mu := slowPodEpisode(t, 1, nil)
	t.Logf("one reader: expired %s after the fleet stopped queueing; mu on record %.3f", first, mu)
	require.Positive(t, mu, "premise: the slow replica wrote a figure")
	require.GreaterOrEqual(t, first, hold-propCycle, "not before the hold")
	require.Less(t, first, hold+time.Minute, "within a minute of it")

	off, _ := slowPodEpisode(t, 1, map[string]bool{SwitchMuMinorityFigureExpires: false})
	require.Negative(t, off, "with the switch off a minority's figure stands")
}

// A fleet that saturated together keeps its figure: every replica read it,
// so it is no minority's, and stands however long nothing queues.
func TestMuSlowPod_AFleetThatSaturatedTogetherKeepsItsFigure(t *testing.T) {
	first, mu := slowPodEpisode(t, 4, nil)
	t.Logf("four readers: first expiry %s; mu on record %.3f", first, mu)
	require.Positive(t, mu, "premise: the fleet wrote a figure")
	require.Negative(t, first, "the fleet's own figure never expires")
}

// The two ledger cases #495 cleared, each failing with its switch off, as
// they did on the parent: the slow reading refused (SwitchMuWeighsFleet) and
// the yield to the full siblings' median (SwitchMuYieldCappedAtMeasurement).
func TestMuSlowPod_TheClearedLedgerCasesFailWithTheirSwitchOff(t *testing.T) {
	for _, c := range []struct {
		name  string
		check func(map[string]bool) func(uint64) error
		off   string
	}{
		{"half-served-max-slow-pod", checkMuHalfServedWith, SwitchMuWeighsFleet},
		{"sibling-yield-slow-pod", checkMuSiblingYieldWith, SwitchMuYieldCappedAtMeasurement},
	} {
		for seed := uint64(1); seed <= 3; seed++ {
			require.NoError(t, c.check(nil)(seed), "%s seed %d with the switch on", c.name, seed)
			err := c.check(map[string]bool{c.off: false})(seed)
			var v *learnertest.Violation
			require.ErrorAs(t, err, &v, "%s seed %d with %s off", c.name, seed, c.off)
			require.Equal(t, kindOverPriced, v.Kind)
			t.Logf("%s, %s off, seed %d: %s", c.name, c.off, seed, v.Msg)
		}
	}
}

// The sibling yield never prices mu above the measurement it replaces
// (SwitchMuYieldCappedAtMeasurement). The fixture is the ttft-three-pods
// cycle the uncapped fleet yield moved: a variant measured at 3.39 whose full
// replicas served 5.18 unqueued. Capped, there is no yield; uncapped, the
// negative control, it priced 5.18, 53 % over the measurement.
func TestMuSlowPod_TheYieldNeverPricesAboveTheMeasurement(t *testing.T) {
	tn := tuning{AnalyzerTuning: tuningDefault.WithDefaults()}
	served := servedRates{max: 5.18, saturated: 5.18, unqueued: true}
	for _, capped := range []bool{true, false} {
		a := NewSaturationAnalyzer(capacity.NewStore())
		require.NoError(t, a.SetSwitchesForReplay(map[string]bool{SwitchMuYieldCappedAtMeasurement: capped}))
		rate, yielded := a.siblingYield(propMuKey, throughputReading{rate: 3.39}, served, tn)
		t.Logf("capped %v: yielded %v at %.2f against a measured 3.39", capped, yielded, rate)
		if capped {
			require.False(t, yielded, "a yield priced above the measurement")
		} else {
			require.True(t, yielded, "premise: uncapped, the yield prices the siblings")
			require.Greater(t, rate, 3.39)
		}
	}

	// Through Analyze, the weighing off so the yield alone decides: p0 queued
	// at 3.39, two full siblings unqueued at 5.18.
	bucket := func(switches map[string]bool) string {
		a := newE1Analyzer(t, false)
		require.NoError(t, a.SetSwitchesForReplay(switches))
		var b map[string]string
		for i := range 20 {
			rows := make([]e1Row, 0, 3)
			rows = append(rows, e1Row{"p0", 30, 330_000, 3.39, 3.39*498 + float64(i%3)})
			for _, p := range []string{"p1", "p2"} {
				rows = append(rows, e1Row{p, 0, 330_000, 5.18, 5.18*498 + float64(i%3)})
			}
			b, _ = tunedE1(t, a, e1Start.Add(time.Duration(i)*15*time.Second), rows, nil)
		}
		return b["p0"]
	}
	require.NotEqual(t, servedBucket, bucket(map[string]bool{SwitchMuWeighsFleet: false}), "capped, p0 priced its siblings")
	require.Equal(t, servedBucket, bucket(map[string]bool{SwitchMuWeighsFleet: false, SwitchMuYieldCappedAtMeasurement: false}),
		"premise: uncapped, p0 yields to its siblings")
	require.NotEqual(t, servedBucket, bucket(nil), "all on, p0 priced its siblings")
}

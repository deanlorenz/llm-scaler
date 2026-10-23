package saturation

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// muChoiceSeed measures p0's own mu with no line (so nothing derived refuses
// it): four saturated cycles a minute apart at gps tokens/s over 500 out,
// from 10:00. Then it publishes the E1 line (derived about 3.6 req/s).
func muChoiceSeed(t *testing.T, gps float64) (*SaturationAnalyzer, func(time.Duration) string) {
	t.Helper()
	a := newE1Analyzer(t, false)
	start, _ := time.Parse("15:04:05", "10:00:00")
	at := func(d time.Duration) string { return start.Add(d).Format("15:04:05") }
	seed := make([]e1Cycle, 0, 4)
	for i := range 4 {
		seed = append(seed, e1Cycle{at(time.Duration(i) * time.Minute),
			[]e1Row{{"p0", 30, 330_000, gps / 500, gps + float64(i)}}})
	}
	_, _ = replayE1(t, a, seed)
	publishE1Line(a)
	return a, at
}

// Review probe (round 2, continuous saturation always picked the higher mu).
// A replica queued and saturated every minute keeps pricing from its own
// measurement, low or high: every cycle re-reads it. Under half the derived
// figure each reading was refused, so the measurement went stale by
// construction and gave way to a derived figure three times its rate.
func TestMuChoice_ContinuousSaturationKeepsItsMeasurement(t *testing.T) {
	for _, gps := range []float64{600, 4000} { // 1.2 and 8.0 req/s, derived ~3.6
		t.Run(fmt.Sprintf("measured %.1f req/s", gps/500), func(t *testing.T) {
			a, at := muChoiceSeed(t, gps)
			sat := make([]e1Cycle, 0, 12)
			for i := range 12 {
				sat = append(sat, e1Cycle{at(4*time.Minute + time.Duration(i)*time.Minute),
					[]e1Row{{"p0", 30, 330_000, gps / 500, gps + float64(i%3)}}})
			}
			decisions, _ := replayE1(t, a, sat)
			for _, d := range decisions {
				assert.Equal(t, "medium", d.bucket, "%s: a replica saturated every minute is priced from its own measurement, not from %s at %.3f",
					d.at, d.bucket, d.mu)
			}
		})
	}
}

// The 0.5x refusal does not freeze a real low measurement for ever: a
// replica that reads under half the derived figure on every saturated cycle
// for MeasuredLowConfirmFor has its readings recorded from then on.
func TestMuChoice_ALowRateThatRepeatsIsRecorded(t *testing.T) {
	a, at := muChoiceSeed(t, 600)
	samples := func() int {
		a.mu.Lock()
		defer a.mu.Unlock()
		n := 0
		for _, ra := range a.saturatedThroughput {
			n += ra.Len()
		}
		return n
	}
	seeded := samples()
	sat := make([]e1Cycle, 0, 16)
	for i := range 16 {
		sat = append(sat, e1Cycle{at(4*time.Minute + time.Duration(i)*15*time.Second),
			[]e1Row{{"p0", 30, 330_000, 1.2, 600 + float64(i%5)}}})
	}
	_, _ = replayE1(t, a, sat)
	after := samples()
	t.Logf("samples: %d seeded, %d after four minutes saturated at 1.2 req/s under a derived ~3.6", seeded, after)
	assert.Greater(t, after, seeded, "a low rate read on every saturated cycle for four minutes was never recorded")
}

// A stale measurement far above the derived figure, which the bucket's own
// saturated readings now contradict, prices the lower figure: the readings do
// not keep it fresh.
func TestMuChoice_AHighMeasurementTheReadingsContradictYields(t *testing.T) {
	a, at := muChoiceSeed(t, 4000) // 8.0 req/s
	sat := make([]e1Cycle, 0, 8)
	for i := range 8 {
		sat = append(sat, e1Cycle{at(4*time.Minute + time.Duration(i)*time.Minute),
			[]e1Row{{"p0", 30, 330_000, 1.2, 600 + float64(i%3)}}})
	}
	decisions, _ := replayE1(t, a, sat)
	last := decisions[len(decisions)-1]
	t.Logf("after %s saturated at 1.2 req/s: priced %.3f from %s", last.at, last.mu, last.bucket)
	assert.Less(t, last.mu, 4.0, "a measurement of 8.0 that every reading contradicts still prices the replica")
}

// Review probe (round 2, the yield flipped cycle to cycle). A stale
// measurement near twice the derived figure, derived moving with 4 % noise on
// the fleet's output length: the priced mu may change source once, not
// alternate (it flipped 8-11 times in 20 cycles).
func TestMuChoice_TheYieldDoesNotFlipNearTheRatio(t *testing.T) {
	for _, gps := range []float64{3500, 3700, 3900} {
		t.Run(fmt.Sprintf("measured %.1f req/s", gps/500), func(t *testing.T) {
			a := newE1Analyzer(t, false)
			start := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
			analyze := func(at time.Time, rm domain.ReplicaMetrics) string {
				a.now = func() time.Time { return at }
				ctx, logs := observedCtx(t)
				_, err := a.Analyze(ctx, makeAnalyzerInput([]domain.ReplicaMetrics{rm}, []domain.VariantReplicaState{{
					VariantName: e1Variant, AcceleratorName: "H200", CurrentReplicas: 1,
					GPUsPerReplica: 1, Role: domain.RoleDecode,
				}}))
				require.NoError(t, err)
				for _, e := range logs.FilterMessage("replica-capacity-decision").All() {
					return fmt.Sprint(e.ContextMap()["saturatedThroughputBucket"])
				}
				return ""
			}
			for i := range 4 {
				analyze(start.Add(time.Duration(i)*time.Minute), e1Metrics(e1Row{"p0", 30, 330_000, gps / 500, gps + float64(i)}))
			}
			publishE1Line(a)
			rng := rand.New(rand.NewSource(1))
			flips, prev := 0, ""
			var trace strings.Builder
			for i := range 40 {
				rm := e1Metrics(e1Row{"p0", 0, 330_000, gps / 500, gps})
				rm.AvgOutputTokens = 498 * (1 + 0.04*rng.NormFloat64())
				b := analyze(start.Add(4*time.Minute+time.Duration(i)*15*time.Second), rm)
				if i > 20 {
					fmt.Fprintf(&trace, " %s", b)
					if prev != "" && b != prev {
						flips++
					}
				}
				prev = b
			}
			t.Logf("buckets after the measurement went stale:%s", trace.String())
			assert.LessOrEqual(t, flips, 1, "the priced mu changed source %d times in 19 cycles", flips)
		})
	}
}

// muChoiceFleet runs one cycle of the E1 variant at arrival rate lambda and
// returns the replicas its demand implies (TotalDemand over 0.85 x the
// per-replica capacity), and p0's priced mu and its bucket.
func muChoiceFleet(t *testing.T, a *SaturationAnalyzer, at time.Time, lambda float64, rows []e1Row) (float64, float64, string) {
	t.Helper()
	a.now = func() time.Time { return at }
	rms := make([]domain.ReplicaMetrics, 0, len(rows))
	for _, r := range rows {
		rms = append(rms, e1Metrics(r))
	}
	in := makeAnalyzerInput(rms, []domain.VariantReplicaState{{
		VariantName: e1Variant, AcceleratorName: "H200", CurrentReplicas: len(rows),
		GPUsPerReplica: 1, Role: domain.RoleDecode,
	}})
	in.ArrivalRate = lambda
	ctx, logs := observedCtx(t)
	res, err := a.Analyze(ctx, in)
	require.NoError(t, err)
	mu, bucket := 0.0, ""
	for _, e := range logs.FilterMessage("replica-capacity-decision").All() {
		if f := e.ContextMap(); f["pod"] == "p0" {
			mu, _ = f["saturatedThroughput"].(float64)
			bucket = fmt.Sprint(f["saturatedThroughputBucket"])
		}
	}
	per := 0.0
	for _, vc := range res.VariantCapacities {
		if vc.VariantName == e1Variant {
			per = vc.PerReplicaCapacity
		}
	}
	require.Positive(t, per)
	return res.TotalDemand / (0.85 * per), mu, bucket
}

// Review probe (round 3, one slow replica set the fleet's mu). p0 is queued
// and full at 0.3 req/s -- a degraded GPU, a CPU-starved pod, an engine
// thrashing on preemption -- while p1-p3 serve 3.6 req/s each with no queue,
// and the line derives about 3.6. p0 is the only replica admitted saturated,
// so its reading was the sample: confirmed after two minutes, it priced the
// fleet at 0.3 (45 replicas ordered for a load four serve), and 30 minutes
// after p0 recovered the fleet was still priced at 55.5 replicas against the
// 4.7 it needs. A Ready replica of the variant serving 3.6 can sustain 3.6, so
// a reading under half of it is not the variant's service rate.
func TestMuChoice_OneSlowReplicaIsNotTheFleetsMu(t *testing.T) {
	a := newE1Analyzer(t, true)
	start := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	var trace strings.Builder
	for i := range 40 { // ten minutes at 15 s
		rows := []e1Row{{"p0", 30, 330_000, 0.3, 150 + float64(i%3)}}
		for _, p := range []string{"p1", "p2", "p3"} {
			rows = append(rows, e1Row{p, 0, 200_000, 3.6, 1800 + float64(i%3)})
		}
		implied, mu, bucket := muChoiceFleet(t, a, start.Add(time.Duration(i)*15*time.Second), 0.3+3*3.6, rows)
		if i%4 == 0 {
			fmt.Fprintf(&trace, " %dm:%.1f(%s %.2f)", i/4, implied, bucket, mu)
		}
		// 11.1 req/s at 3.6 is 3.6 replicas at the 0.85 threshold.
		assert.Less(t, implied, 5.0, "%ds: one slow replica's rate priced the fleet at %.1f replicas (mu %.2f from %s)",
			i*15, implied, mu, bucket)
	}
	t.Logf("one slow replica beside three serving 3.6 req/s, implied replicas:%s", trace.String())

	trace.Reset()
	for i := 40; i < 160; i++ { // thirty minutes recovered: four replicas at 3.6, nothing queued
		rows := make([]e1Row, 0, 4)
		for _, p := range []string{"p0", "p1", "p2", "p3"} {
			rows = append(rows, e1Row{p, 0, 200_000, 3.6, 1800 + float64(i%3)})
		}
		implied, mu, bucket := muChoiceFleet(t, a, start.Add(time.Duration(i)*15*time.Second), 14.4, rows)
		if i%20 == 0 {
			fmt.Fprintf(&trace, " %dm:%.1f(%s %.2f)", i/4, implied, bucket, mu)
		}
		// 14.4 req/s at 3.6 is 4.7 replicas.
		assert.Less(t, implied, 6.0, "%ds: a recovered fleet is priced at %.1f replicas (mu %.2f from %s)",
			i*15, implied, mu, bucket)
	}
	t.Logf("after the slow replica recovered, implied replicas:%s", trace.String())
}

// A measured mu under the derived figure gives way the moment a Ready
// replica of the variant serves more than MeasuredDerivedMaxRatio times it,
// fresh or not: a replica serving 3.6 req/s can sustain 3.6, so a
// measurement of 1.2 is not what one replica does.
func TestMuChoice_ALowMeasurementAServedRateContradictsYields(t *testing.T) {
	a, at := muChoiceSeed(t, 600) // measured 1.2 req/s, derived ~3.6
	cycles := make([]e1Cycle, 0, 8)
	for i := range 8 {
		cycles = append(cycles, e1Cycle{at(4*time.Minute + time.Duration(i)*15*time.Second),
			[]e1Row{{"p0", 0, 200_000, 3.6, 1800 + float64(i%3)}}})
	}
	decisions, _ := replayE1(t, a, cycles)
	for _, d := range decisions {
		assert.Equal(t, derivedBucket, d.bucket, "%s: a replica serving 3.6 req/s is priced from a measurement of %.3f", d.at, d.mu)
	}
}

// Review probe (round 3, a stale low measurement never yielded). Four
// saturated minutes at 1.2 req/s before the line fitted, then the replica runs
// unsaturated at 1.2 with the line deriving about 3.6. The lower figure keeps
// pricing -- over-ordering, the side a doubt is resolved to -- but for at most
// MeasuredLowHoldFor without a saturated reading that agrees with it: an
// over-ordered fleet never saturates, so nothing would ever refresh or replace
// it. Before, it priced at 1.207 for as long as the window lived.
func TestMuChoice_AStaleLowMeasurementHoldsForAtMostMeasuredLowHoldFor(t *testing.T) {
	a, at := muChoiceSeed(t, 600)
	const lastAgreeing = 3 * time.Minute // the seed's last cycle
	idle := make([]e1Cycle, 0, 40)
	for i := range 40 {
		idle = append(idle, e1Cycle{at(4*time.Minute + time.Duration(i)*time.Minute),
			[]e1Row{{"p0", 0, 120_000, 1.2, 600}}})
	}
	decisions, _ := replayE1(t, a, idle)
	for i, d := range decisions {
		stale := 4*time.Minute + time.Duration(i)*time.Minute - lastAgreeing
		switch {
		case stale < MeasuredLowHoldFor:
			assert.Equal(t, "medium", d.bucket, "%s, stale %s: the lower figure gave way inside MeasuredLowHoldFor", d.at, stale)
		case stale > MeasuredLowHoldFor:
			assert.Equal(t, derivedBucket, d.bucket, "%s, stale %s: a measurement %.3f under derived still prices", d.at, stale, d.mu)
		}
	}
}

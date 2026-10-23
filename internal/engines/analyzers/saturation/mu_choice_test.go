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

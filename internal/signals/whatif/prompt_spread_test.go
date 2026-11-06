package whatif

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

func bucket(u, r float64) domain.LengthBucket { return domain.LengthBucket{UpperBound: u, Rate: r} }

// rowsSpread is the spread of rows' pooled histograms at mean.
func rowsSpread(rows []domain.ReplicaMetrics, mean float64) PromptSpread {
	return ReadPromptSpread(PoolComputedBuckets(nil, rows), mean)
}

func TestReadPromptSpread(t *testing.T) {
	inf := math.Inf(1)
	withBuckets := func(sets ...[]domain.LengthBucket) []domain.ReplicaMetrics {
		rows := make([]domain.ReplicaMetrics, len(sets))
		for i, b := range sets {
			rows[i] = domain.ReplicaMetrics{Ready: true, ComputedTokensBuckets: b}
		}
		return rows
	}
	// 2000-token prompts of which half hit 1800 tokens: I_eff 2000 or 200, a
	// cv of 0.82, where the raw prompts have none.
	got := rowsSpread(withBuckets([]domain.LengthBucket{bucket(200, 1), bucket(2000, 2), bucket(inf, 2)}), 1100)
	assert.False(t, got.Assumed)
	assert.False(t, got.Clamped)
	assert.GreaterOrEqual(t, got.CV, 0.818)

	// A bucketed sample reads at or above the true cv.
	r := rand.New(rand.NewPCG(1, 2))
	bounds := []float64{100, 200, 500, 1000, 2000, 5000, 10000, 20000, 50000, 100000}
	for range 200 {
		xs := make([]float64, 0, 400)
		for range 400 {
			xs = append(xs, 100+r.Float64()*r.Float64()*40000)
		}
		var sum, sq float64
		b := make([]domain.LengthBucket, 0, len(bounds)+1)
		for _, x := range xs {
			sum += x
			sq += x * x
		}
		for _, u := range bounds {
			n := 0.0
			for _, x := range xs {
				if x <= u {
					n++
				}
			}
			b = append(b, bucket(u, n))
		}
		b = append(b, bucket(inf, float64(len(xs))))
		n := float64(len(xs))
		mean := sum / n
		truth := math.Sqrt(sq/n/(mean*mean) - 1)
		assert.GreaterOrEqual(t, rowsSpread(withBuckets(b), mean).CV, truth*(1-1e-12))
	}

	// Pooled over pods: each pod constant, the pool spread.
	a := []domain.LengthBucket{bucket(1000, 1), bucket(10000, 1), bucket(inf, 1)}
	b := []domain.LengthBucket{bucket(1000, 0), bucket(10000, 1), bucket(inf, 1)}
	pooled := rowsSpread(withBuckets(a, b), 5500)
	assert.InDelta(t, math.Sqrt((1e6+1e8)/2/(5500.0*5500)-1), pooled.CV, 1e-9)

	// A not-Ready pod's histogram is not read.
	rows := withBuckets(a, b)
	rows[1].Ready = false
	assert.InDelta(t, 0, rowsSpread(rows, 1000).CV, 1e-9)

	// Mass past the last bound has no upper bound.
	clamped := rowsSpread(withBuckets([]domain.LengthBucket{bucket(1000, 9), bucket(inf, 10)}), 800)
	assert.Equal(t, PromptSpread{CV: 3, Clamped: true}, clamped)
	// A cv above 3.
	over := rowsSpread(withBuckets([]domain.LengthBucket{bucket(100, 99), bucket(100000, 100), bucket(inf, 100)}), 1000)
	assert.Equal(t, PromptSpread{CV: 3, Clamped: true}, over)

	// Constant 3000-token prompts read about 1.3 in the 2000-5000 bucket.
	coarse := rowsSpread(withBuckets([]domain.LengthBucket{bucket(2000, 0), bucket(5000, 1), bucket(inf, 1)}), 3000)
	assert.InDelta(t, 1.33, coarse.CV, 0.01)
}

// With no histogram, cv 2.
func TestReadPromptSpread_Assumed(t *testing.T) {
	assumed := PromptSpread{CV: 2, Assumed: true}
	assert.Equal(t, assumed, rowsSpread(nil, 1000))
	assert.Equal(t, assumed, rowsSpread([]domain.ReplicaMetrics{{Ready: true}}, 1000))
	noTotal := []domain.ReplicaMetrics{{Ready: true, ComputedTokensBuckets: []domain.LengthBucket{bucket(1000, 1)}}}
	assert.Equal(t, assumed, rowsSpread(noTotal, 1000), "no +Inf bucket: no total to read a share of")
	zero := []domain.ReplicaMetrics{{Ready: true, ComputedTokensBuckets: []domain.LengthBucket{bucket(math.Inf(1), 0)}}}
	assert.Equal(t, assumed, rowsSpread(zero, 1000), "no request in the window")
}

// A pooled histogram holds what it was given: nil starts a new map, one
// passed in is added to, and a row with none, a NaN bound or a rate that is no
// reading adds nothing, so a pool of such rows reads as no histogram.
func TestPoolComputedBuckets(t *testing.T) {
	inf := math.Inf(1)
	serving := func(b ...domain.LengthBucket) domain.ReplicaMetrics {
		return domain.ReplicaMetrics{Ready: true, ComputedTokensBuckets: b}
	}
	pooled := PoolComputedBuckets(nil, []domain.ReplicaMetrics{serving(bucket(100, 1), bucket(inf, 2))})
	assert.Equal(t, map[float64]float64{100: 1, inf: 2}, pooled)
	pooled = PoolComputedBuckets(pooled, []domain.ReplicaMetrics{serving(bucket(inf, 3)), {Ready: true}})
	assert.Equal(t, map[float64]float64{100: 1, inf: 5}, pooled)

	junk := PoolComputedBuckets(nil, []domain.ReplicaMetrics{serving(
		bucket(math.NaN(), 1), bucket(100, math.NaN()), bucket(200, inf), bucket(300, -1))})
	assert.Empty(t, junk)
	assert.Equal(t, PromptSpread{CV: AssumedPromptCV, Assumed: true}, ReadPromptSpread(junk, 1000))
}

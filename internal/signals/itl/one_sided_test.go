package itl

import (
	"fmt"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// cappedRows is three replicas at their sequence cap (64 running on one
// rank, requests waiting, no preemption) at fill k, on the line ITL = a k +
// b, Ready an hour before start.
func cappedRows(k, a, b float64, start time.Time) []domain.ReplicaMetrics {
	out := make([]domain.ReplicaMetrics, 0, 3)
	for i := range 3 {
		out = append(out, domain.ReplicaMetrics{PodName: fmt.Sprintf("pod-%d", i), VariantName: "v", Ready: true,
			ReadySince: start.Add(-time.Hour), KvCacheUsage: k, KvUsageInstant: k, AvgITL: a*k + b,
			RunningRequests: 64, QueueLength: 5, PreemptionRateKnown: true})
	}
	return out
}

// With OneSidedAboveKSat a key with no learned B, taught only by replicas
// at their cap above DefaultKSat, prices at DefaultKSat at or above the true
// line: the line through the least ITL of those readings wins over the fit
// through DefaultBaselineSec, which prices under a true B above it. Without
// it, as before, the fit through the default prices under the truth.
func TestTracker_OneSidedAboveKSatPricesAtOrAboveTheTruth(t *testing.T) {
	start := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	const a, b = 0.01, 0.030
	truth := a*DefaultKSat + b
	price := func(oneSided bool) (Reading, float64) {
		tr := NewTracker()
		adm := Admission{MaxNumSeqs: 64, OneSidedAboveKSat: oneSided}
		var r Reading
		for c := range 6 {
			k := []float64{0.91, 0.92, 0.93}[c%3]
			r = tr.Observe("k", "v", cappedRows(k, a, b, start), adm, start.Add(time.Duration(c)*15*time.Second))
		}
		if r.Tier == "" {
			t.Fatalf("one-sided %v: no line", oneSided)
		}
		return r, r.Model.ITLAt(DefaultKSat)
	}
	r, on := price(true)
	if !r.PinnedAtLeastITL || on < truth {
		t.Errorf("one-sided: priced %.5f s against the truth %.5f s (pinned at the least ITL %v)", on, truth, r.PinnedAtLeastITL)
	}
	r, off := price(false)
	if r.PinnedAtLeastITL || !(off < truth) {
		t.Errorf("negative control, without it: priced %.5f s against the truth %.5f s; it should price under", off, truth)
	}
	t.Logf("truth %.5f s: one-sided %.5f s, through the default B %.5f s", truth, on, off)
}

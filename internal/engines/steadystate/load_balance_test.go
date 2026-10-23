package steadystate

import (
	"strings"
	"testing"

	promclient "github.com/prometheus/client_golang/prometheus"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
)

// loadSeries is the value of the one wva_load_imbalanced series for model m, and
// how many load-distribution series exist in all.
func loadSeries(t *testing.T, reg *promclient.Registry) (imbalanced float64, present bool, total int) {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), "wva_load_") {
			continue
		}
		total += len(mf.GetMetric())
		if mf.GetName() != constants.WVALoadImbalanced {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == constants.LabelModelName && l.GetValue() == "m" {
					imbalanced, present = m.GetGauge().GetValue(), true
				}
			}
		}
	}
	return imbalanced, present, total
}

// The measured case, end to end through runAnalyzers: one prefill replica
// queued while three idle warns once, on the second cycle, publishes
// wva_load_imbalanced=1, and leaves what the analyzer was given untouched.
// A model with no rows, or no longer reconciled, takes its series with it.
func TestRunAnalyzersReportsLoadImbalanceOnce(t *testing.T) {
	reg := promclient.NewRegistry()
	if err := metrics.InitMetrics(reg); err != nil {
		t.Fatal(err)
	}
	sat := &fakeAnalyzerWithResult{analyzerName: domain.SaturationAnalyzerName, result: &domain.AnalyzerResult{}}
	e := &Engine{saturationV2Analyzer: sat, started: true,
		analyzersSnapshot: []analyzerEntry{{name: domain.SaturationAnalyzerName, analyzer: sat}}}
	states := []domain.VariantReplicaState{
		{VariantName: "pre", Role: domain.RolePrefill, CurrentReplicas: 4},
		{VariantName: "dec", Role: domain.RoleDecode, CurrentReplicas: 1},
	}
	rows := []domain.ReplicaMetrics{
		{PodName: "pre-0", VariantName: "pre", KvCacheUsage: 0.20, QueueLength: 439, Ready: true},
		{PodName: "pre-1", VariantName: "pre", KvCacheUsage: 0.01, Ready: true},
		{PodName: "pre-2", VariantName: "pre", Ready: true},
		{PodName: "pre-3", VariantName: "pre", KvCacheUsage: 0.02, Ready: true},
		{PodName: "dec-0", VariantName: "dec", KvCacheUsage: 0.40, Ready: true},
	}
	policy := config.ScalingPolicy{ScaleUpThreshold: 0.85, ScaleDownBoundary: 0.70, QueueLengthThreshold: 5}
	ctx, logs := zapObserverCtx(t)
	for cycle := 1; cycle <= 3; cycle++ {
		if _, err := e.runAnalyzers(ctx, "m", "ns", rows, policy, states, nil, nil, nil, nil, 0); err != nil {
			t.Fatal(err)
		}
		v, present, _ := loadSeries(t, reg)
		want := 0.0
		if cycle >= 2 {
			want = 1
		}
		if !present || v != want {
			t.Errorf("cycle %d: wva_load_imbalanced = %v (present %v), want %v", cycle, v, present, want)
		}
	}
	warned := 0
	for _, entry := range logs.All() {
		if strings.HasPrefix(entry.Message, "WARNING: load imbalanced") {
			warned++
			fields := entry.ContextMap()
			if fields["role"] != domain.RolePrefill || fields["busiestPod"] != "pre-0" {
				t.Errorf("warning names role %v pod %v, want prefill and pre-0", fields["role"], fields["busiestPod"])
			}
		}
	}
	if warned != 1 {
		t.Errorf("%d imbalance warnings over three imbalanced cycles, want exactly 1", warned)
	}
	if rows[0].QueueLength != 439 || rows[0].KvCacheUsage != 0.20 {
		t.Error("observing the load changed the rows the analyzers read")
	}

	e.onNothingObserved("ns", "m")
	if _, present, total := loadSeries(t, reg); present || total != 0 {
		t.Errorf("a model with no rows kept %d load series", total)
	}

	if _, err := e.runAnalyzers(ctx, "m", "ns", rows, policy, states, nil, nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := loadSeries(t, reg); v != 0 {
		t.Error("a model back from no rows resumed its old streak")
	}
	e.pruneAnalyzerSeries(map[string]bool{"ns/other": true})
	if _, _, total := loadSeries(t, reg); total != 0 {
		t.Errorf("a model no longer reconciled kept %d load series", total)
	}
	if _, err := e.runAnalyzers(ctx, "m", "ns", rows, policy, states, nil, nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	e.evictAllAnalyzerSeries()
	if _, _, total := loadSeries(t, reg); total != 0 {
		t.Errorf("evicting every series kept %d load series", total)
	}
}

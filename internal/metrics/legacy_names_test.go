package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func gatherFamilies(t *testing.T, g prometheus.Gatherer) map[string]*dto.MetricFamily {
	t.Helper()
	families, err := g.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	out := map[string]*dto.MetricFamily{}
	for _, f := range families {
		out[f.GetName()] = f
	}
	return out
}

func TestWithLegacyNames_PublishesBothNamesWithOneValue(t *testing.T) {
	reg := prometheus.NewRegistry()
	r := withLegacyNames(reg, "")

	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "llmsm_test_gauge", Help: "A gauge."}, []string{"variant_name"})
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "llmsm_test_total", Help: "A counter."})
	histogram := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "llmsm_test_seconds", Help: "A histogram.", Buckets: []float64{1, 5}})
	r.MustRegister(gauge, counter, histogram)

	gauge.WithLabelValues("a").Set(3)
	gauge.WithLabelValues("b").Set(7)
	counter.Add(2)
	histogram.Observe(2)

	got := gatherFamilies(t, reg)

	if v := got["wva_test_total"].GetMetric()[0].GetCounter().GetValue(); v != 2 {
		t.Errorf("wva_test_total = %v, want 2", v)
	}
	if got["wva_test_total"].GetType() != dto.MetricType_COUNTER {
		t.Errorf("wva_test_total type = %v", got["wva_test_total"].GetType())
	}
	if h := got["wva_test_seconds"].GetMetric()[0].GetHistogram(); h.GetSampleCount() != 1 || h.GetSampleSum() != 2 || len(h.GetBucket()) != 2 {
		t.Errorf("wva_test_seconds = %v", h)
	}
	series := got["wva_test_gauge"].GetMetric()
	if len(series) != 2 {
		t.Fatalf("wva_test_gauge has %d series, want 2", len(series))
	}
	for _, m := range series {
		want := map[string]float64{"a": 3, "b": 7}[m.GetLabel()[0].GetValue()]
		if m.GetGauge().GetValue() != want {
			t.Errorf("series %v = %v, want %v", m.GetLabel(), m.GetGauge().GetValue(), want)
		}
	}
	if len(got["llmsm_test_gauge"].GetMetric()) != 2 {
		t.Errorf("the current name lost series: %v", got["llmsm_test_gauge"])
	}
	if help := got["wva_test_gauge"].GetHelp(); help != "Deprecated: use llmsm_test_gauge. A gauge." {
		t.Errorf("help = %q", help)
	}
}

func TestWithLegacyNames_FollowsChangesBetweenScrapes(t *testing.T) {
	reg := prometheus.NewRegistry()
	r := withLegacyNames(reg, "")
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "llmsm_test_gauge", Help: "A gauge."})
	r.MustRegister(gauge)

	for _, v := range []float64{1, 9} {
		gauge.Set(v)
		got := gatherFamilies(t, reg)["wva_test_gauge"].GetMetric()[0].GetGauge().GetValue()
		if got != v {
			t.Errorf("after Set(%v) the old name reads %v", v, got)
		}
	}
}

func TestWithLegacyNames_LeavesOtherPrefixesAlone(t *testing.T) {
	reg := prometheus.NewRegistry()
	r := withLegacyNames(reg, "")
	r.MustRegister(
		prometheus.NewGauge(prometheus.GaugeOpts{Name: "wva_already_old", Help: "x"}),
		prometheus.NewGauge(prometheus.GaugeOpts{Name: "controller_thing", Help: "x"}),
	)
	got := gatherFamilies(t, reg)
	if len(got) != 2 {
		t.Errorf("published %d families, want the 2 registered: %v", len(got), got)
	}
}

func TestWithLegacyNames_OffWhenAsked(t *testing.T) {
	reg := prometheus.NewRegistry()
	r := withLegacyNames(reg, "false")
	r.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "llmsm_test_gauge", Help: "x"}))
	got := gatherFamilies(t, reg)
	if _, ok := got["wva_test_gauge"]; ok || len(got) != 1 {
		t.Errorf("old name published while off: %v", got)
	}
}

func TestWithLegacyNames_RegisterErrorsStayTheCallersErrors(t *testing.T) {
	reg := prometheus.NewRegistry()
	r := withLegacyNames(reg, "")
	first := prometheus.NewGauge(prometheus.GaugeOpts{Name: "llmsm_test_gauge", Help: "x"})
	if err := r.Register(first); err != nil {
		t.Fatal(err)
	}
	dup := prometheus.NewGauge(prometheus.GaugeOpts{Name: "llmsm_test_gauge", Help: "x"})
	if err := r.Register(dup); err == nil {
		t.Fatal("a duplicate registered without an error")
	}
	if !r.Unregister(first) {
		t.Fatal("Unregister reported nothing removed")
	}
	if got := gatherFamilies(t, reg); len(got) != 0 {
		t.Errorf("published after Unregister: %v", got)
	}
}

// TestInitMetrics_LegacyNamesPublishNoFailure is the whole metric set through
// a real scrape: a gather error here fails every /metrics request.
func TestInitMetrics_LegacyNamesPublishNoFailure(t *testing.T) {
	reg := prometheus.NewRegistry()
	if err := InitMetrics(reg); err != nil {
		t.Fatalf("InitMetrics: %v", err)
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatalf("Gather: %v", err)
	}
}

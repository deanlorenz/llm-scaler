package steadystate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	promclient "github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source/prometheus"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/slo"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/registry"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/scalingpolicy"
)

func sloWiringEngine(t *testing.T) *Engine {
	t.Helper()
	ctx := context.Background()
	c := fake.NewClientBuilder().Build()
	sources := source.NewSourceRegistry()
	if err := sources.Register("prometheus", prometheus.NewPrometheusSource(ctx, nil, prometheus.DefaultPrometheusSourceConfig())); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(c, c, c.Scheme(), nil, sources, config.NewTestConfig(), allocation.NewNoOpLimiter("test"))
	e.Variants = registry.New(0)
	return e
}

// TestNewEngineGivesTheSLOAnalyzerARouterSource pins the wiring that decides
// whether an SLO is ever judged: built without the collector as its router
// source, the analyzer prices variants and leaves every objective unjudged,
// with nothing failing.
func TestNewEngineGivesTheSLOAnalyzerARouterSource(t *testing.T) {
	e := sloWiringEngine(t)
	for _, entry := range e.analyzers {
		if entry.name != domain.SLOAnalyzerName {
			continue
		}
		a, ok := entry.analyzer.(*slo.Analyzer)
		if !ok {
			t.Fatalf("the %q entry is a %T", entry.name, entry.analyzer)
		}
		if !a.Judging() {
			t.Fatal("the slo analyzer was built without a router latency source")
		}
		return
	}
	t.Fatal("NewEngine registered no slo analyzer")
}

// TestNewEngineRegistersTheRouterQueries pins that the router queries exist in
// the engine's registry: without them every refresh fails and the SLO is
// silently never judged.
func TestNewEngineRegistersTheRouterQueries(t *testing.T) {
	e := sloWiringEngine(t)
	ql := e.metricsRegistry.Get("prometheus").QueryList()
	for _, name := range []string{"slo_router_ttft_buckets", "slo_router_tpot_sum", "slo_router_tpot_count"} {
		if ql.Get(name) == nil {
			t.Errorf("query %q is not registered", name)
		}
	}
}

// TestOptimizeSweepsStaleSLOSeries pins the per-cycle sweep: the last model to
// stop publishing SLO series would otherwise keep them for ever, because the
// only other sweep runs inside a publish that never comes.
func TestOptimizeSweepsStaleSLOSeries(t *testing.T) {
	reg := promclient.NewRegistry()
	if err := metrics.InitMetrics(reg); err != nil {
		t.Fatal(err)
	}
	metrics.PublishSLO(metrics.SLOModelSnapshot{Namespace: "ns", ModelName: "m",
		Objectives: []metrics.SLOObjectiveReading{{Objective: constants.SLOObjectiveTTFT, Statistic: "p90",
			TargetSeconds: 3, Observed: 1, Attainment: 0.95, Requests: 300}},
	}, time.Now().Add(-11*time.Minute))

	count := func() int {
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, mf := range mfs {
			if mf.GetName() == constants.WVASLOTargetSeconds {
				return len(mf.GetMetric())
			}
		}
		return 0
	}
	if count() != 1 {
		t.Fatal("setup: the stale series was not published")
	}
	e := sloWiringEngine(t)
	if err := e.optimize(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("optimize: %v", err)
	}
	if n := count(); n != 0 {
		t.Fatalf("after a cycle with no model %d stale wva_slo_target_seconds series remain", n)
	}
}

// TestResolveModelPolicyReportsAHalfConfiguredSLO pins that the report comes
// from the policy resolution, the one place that sees a model whose policy sets
// targets but does not list the analyzer (which therefore never runs).
func TestResolveModelPolicyReportsAHalfConfiguredSLO(t *testing.T) {
	var lines []string
	logger := funcr.New(func(prefix, args string) { lines = append(lines, args) }, funcr.Options{})
	ctx := logr.NewContext(context.Background(), logger)
	e := &Engine{policies: scalingpolicy.NewChangeReporter()}
	targets := &config.SLOPolicy{TPOT: &config.SLOObjective{Target: config.SLODuration{Duration: 100 * time.Millisecond}}}
	e.resolveModelPolicy(ctx, map[string]config.ScalingPolicy{
		"default": {Analyzers: []config.AnalyzerScoreConfig{{Name: "saturation"}}, SLO: targets},
	}, "m", "ns", nil)
	for _, l := range lines {
		if strings.Contains(l, "SLO policy is half-configured") && strings.Contains(l, "nothing reads them") {
			return
		}
	}
	t.Fatalf("no half-configured report among %d log lines: %v", len(lines), lines)
}

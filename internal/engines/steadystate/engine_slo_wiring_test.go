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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	llmdVariantAutoscalingV1alpha1 "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/variant"
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

// sloKeeperSpy records how the engine's sweeps reached the slo analyzer.
type sloKeeperSpy struct {
	evictions int
	present   map[string][]string
}

func (s *sloKeeperSpy) NoteVariantsPresent(namespace, modelID string, names []string) {
	if s.present == nil {
		s.present = map[string][]string{}
	}
	s.present[namespace+"/"+modelID] = append(s.present[namespace+"/"+modelID], names...)
}

func (s *sloKeeperSpy) EvictStale(time.Time) slo.Evicted {
	s.evictions++
	return slo.Evicted{}
}

// TestNewEngineHoldsTheSLOAnalyzerItRegisters pins that the analyzer the sweeps
// reach is the one that runs: two instances would leave the running one's state
// unswept.
func TestNewEngineHoldsTheSLOAnalyzerItRegisters(t *testing.T) {
	e := sloWiringEngine(t)
	for _, entry := range e.analyzers {
		if entry.name == domain.SLOAnalyzerName {
			if entry.analyzer != e.sloAnalyzer.(domain.Analyzer) {
				t.Fatal("the engine sweeps a different slo analyzer than the one it registered")
			}
			return
		}
	}
	t.Fatal("NewEngine registered no slo analyzer")
}

// TestEvictStaleLearnedStateSweepsTheSLOAnalyzer pins that the slo analyzer's
// state is aged by the engine's once-per-cycle sweep. It used to sweep inside
// Analyze, which never runs again once the last model stops listing it.
func TestEvictStaleLearnedStateSweepsTheSLOAnalyzer(t *testing.T) {
	spy := &sloKeeperSpy{}
	e := &Engine{saturationV2Analyzer: plainAnalyzer{}, sloAnalyzer: spy}
	e.evictStaleLearnedState(context.Background())
	if spy.evictions != 1 {
		t.Fatalf("a sweep reached the slo analyzer %d times, want 1", spy.evictions)
	}
}

// TestNotePresentVariantsStampsTheSLOAnalyzer pins that a variant parked at
// zero, which no analyzer runs for, keeps its slo line as it keeps its
// saturation state.
func TestNotePresentVariantsStampsTheSLOAnalyzer(t *testing.T) {
	e := sloWiringEngine(t)
	spy := &sloKeeperSpy{}
	e.sloAnalyzer = spy
	e.notePresentVariants(context.Background(), map[string][]llmdVariantAutoscalingV1alpha1.VariantAutoscaling{
		"chat/m": {{
			ObjectMeta: metav1.ObjectMeta{Name: "chat-a", Namespace: "chat"},
			Spec:       llmdVariantAutoscalingV1alpha1.VariantAutoscalingSpec{ModelID: "m"},
		}},
	})
	if got := spy.present["chat/m"]; len(got) != 1 || got[0] != "chat-a" {
		t.Fatalf("the slo analyzer was told %v present for chat/m, want [chat-a]", got)
	}
}

// TestSLONamespacesReachTheCollectorEachCycle pins the gate on the replica
// queries only the slo analyzer reads: the namespaces it ran in this cycle are
// the ones collected with them next cycle, and a namespace it stopped running
// in loses them.
func TestSLONamespacesReachTheCollectorEachCycle(t *testing.T) {
	e := sloWiringEngine(t)
	e.noteSLONamespace("chat")
	e.handOverSLONamespaces()
	if !e.ReplicaMetricsCollector.CollectsSLOInputs("chat") || e.ReplicaMetricsCollector.CollectsSLOInputs("batch") {
		t.Fatal("the collector must fetch the slo inputs in chat, where the analyzer ran, and only there")
	}
	e.handOverSLONamespaces()
	if e.ReplicaMetricsCollector.CollectsSLOInputs("chat") {
		t.Fatal("a cycle with no slo model must stop the inputs it no longer reads")
	}

	// And a cycle hands its set over on the way out.
	e.noteSLONamespace("chat")
	e.optimizeV2(context.Background(), nil, nil)
	if !e.ReplicaMetricsCollector.CollectsSLOInputs("chat") {
		t.Fatal("optimizeV2 did not hand this cycle's slo namespaces to the collector")
	}
}

// TestRunningTheSLOAnalyzerNotesItsNamespace pins the producer side of that
// gate: a model whose policy lists slo marks its namespace, and one whose
// policy does not marks nothing.
func TestRunningTheSLOAnalyzerNotesItsNamespace(t *testing.T) {
	sat := &fakeAnalyzerWithResult{analyzerName: domain.SaturationAnalyzerName, result: &domain.AnalyzerResult{}}
	sloSpy := &fakeAnalyzerWithResult{analyzerName: domain.SLOAnalyzerName, result: &domain.AnalyzerResult{}}
	e := &Engine{saturationV2Analyzer: sat, started: true, analyzersSnapshot: []analyzerEntry{
		{name: domain.SaturationAnalyzerName, analyzer: sat}, {name: domain.SLOAnalyzerName, analyzer: sloSpy}}}
	plain := config.ScalingPolicy{ScaleUpThreshold: 0.85, ScaleDownBoundary: 0.70}
	withSLO := plain
	withSLO.Analyzers = []config.AnalyzerScoreConfig{{Name: domain.SaturationAnalyzerName}, {Name: domain.SLOAnalyzerName}}

	if _, err := e.runAnalyzersAndScore(context.Background(), "m", "batch", nil, plain, nil, nil, nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.runAnalyzersAndScore(context.Background(), "m", "chat", nil, withSLO, nil, nil, nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if !e.sloNamespaces["chat"] || e.sloNamespaces["batch"] {
		t.Fatalf("slo namespaces this cycle are %v, want only chat", e.sloNamespaces)
	}
}

// TestPruneAnalyzerSeriesDeletesADepartedModelsSLOSeries pins that a model
// whose VariantAutoscalings are gone loses its SLO series with its other
// analyzer series, not ten minutes later.
func TestPruneAnalyzerSeriesDeletesADepartedModelsSLOSeries(t *testing.T) {
	reg := promclient.NewRegistry()
	if err := metrics.InitMetrics(reg); err != nil {
		t.Fatal(err)
	}
	metrics.PublishSLO(metrics.SLOModelSnapshot{Namespace: "ns", ModelName: "gone",
		Objectives: []metrics.SLOObjectiveReading{{Objective: constants.SLOObjectiveTTFT, Statistic: "p90",
			TargetSeconds: 3, Observed: 1, Attainment: 0.95, Requests: 300}},
	}, time.Now())
	metrics.PublishSLO(metrics.SLOModelSnapshot{Namespace: "ns", ModelName: "kept",
		Objectives: []metrics.SLOObjectiveReading{{Objective: constants.SLOObjectiveTTFT, Statistic: "p90",
			TargetSeconds: 3, Observed: 1, Attainment: 0.95, Requests: 300}},
	}, time.Now())
	targets := func() map[string]bool {
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, mf := range mfs {
			if mf.GetName() != constants.WVASLOTargetSeconds {
				continue
			}
			for _, m := range mf.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == constants.LabelModelName {
						out[l.GetValue()] = true
					}
				}
			}
		}
		return out
	}
	if got := targets(); !got["gone"] || !got["kept"] {
		t.Fatalf("setup: published %v", got)
	}
	e := &Engine{metricsEmitter: metrics.NewMetricsEmitter(), lastAnalyzerSeries: map[string]analyzerSeries{
		"ns/gone": {namespace: "ns", modelID: "gone"},
		"ns/kept": {namespace: "ns", modelID: "kept"},
	}}
	e.pruneAnalyzerSeries(map[string]bool{"ns/kept": true})
	if got := targets(); got["gone"] || !got["kept"] {
		t.Fatalf("after the departed model's prune the SLO series are %v, want only kept", got)
	}
}

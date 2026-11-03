package steadystate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/prometheus/client_golang/prometheus"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/passive"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/registry"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/variant"
)

// The limiter decision, through decideV2: under a limiter an active model is
// allocated exactly as if its passive neighbour were absent at its real usage
// (which the limiter's used figure already holds), and the passive model's
// would-be growth comes from what is left. One replica's GPUs are free, and the
// passive model has the higher priority: in one pass it would take them.
func TestPassiveGrowthTakesNothingFromAnActiveModel(t *testing.T) {
	t.Cleanup(decision.DefaultHeadroom.Reset)
	pool := func() []*allocation.ResourceConstraints {
		// Two models at one 8-GPU replica each: 16 used, 8 free.
		return []*allocation.ResourceConstraints{{ProviderName: "gpu-limiter",
			Pools: map[string]allocation.ResourcePool{"A100": {Limit: 24, Used: 16}}}}
	}
	act := growingA100("a", "act", 8)
	pas := growingA100("p", "pas", 8)
	pas.Priority = 5
	pas.Passive = true
	added := func(reqs ...allocation.ModelScalingRequest) map[string]int {
		e := &Engine{Config: config.NewTestConfig()}
		out := map[string]int{}
		for _, d := range e.decideV2(context.Background(), allocation.NewGreedyByScoreOptimizer(), reqs, pool(), nil) {
			out[d.ModelID] += (d.TargetReplicas - d.CurrentReplicas) * d.GPUsPerReplica
		}
		return out
	}
	alone, beside := added(act), added(act, pas)
	if alone["act"] != 8 || beside["act"] != alone["act"] {
		t.Fatalf("the active model grew %d GPUs beside a passive one and %d alone, want 8 both", beside["act"], alone["act"])
	}
	if beside["pas"] != 0 {
		t.Fatalf("the passive model's would-be growth is %d GPUs, want 0: nothing is left", beside["pas"])
	}
}

// recordBorrowDraws draws nothing for a passive model: its would-be growth is
// never ordered, and a draw would hold borrowed capacity from the models that
// can grow. The same model active is the control.
func TestPassiveDrawsNoBorrowedCapacity(t *testing.T) {
	for _, tc := range []struct {
		passive bool
		want    int
	}{{false, 1}, {true, 0}} {
		t.Cleanup(decision.DefaultHeadroom.Reset)
		e := &Engine{Config: config.NewTestConfig()}
		e.borrowing.inFlight = allocation.NewBorrowInFlight()
		now := time.Unix(30_000, 0)
		requests := []allocation.ModelScalingRequest{recordRequest(2, 2)}
		optimizer := allocation.NewGreedyByScoreOptimizer()
		c := e.prepareBorrowing(context.Background(), recordQuota(8, now), requests, nil, now)
		optimizer.BorrowBlocked = e.borrowBlocked()
		decisions := e.decideV2(context.Background(), optimizer, requests, c, nil)
		if tc.passive {
			e.cyclePassive = map[string]bool{"a/m": true}
		}
		if draws := e.recordBorrowDraws(optimizer, decisions, now); len(draws) != tc.want {
			t.Errorf("passive=%v: %d draws, want %d", tc.passive, len(draws), tc.want)
		}
		if tc.passive && len(e.borrowing.inFlight.Entries()) != 0 {
			t.Errorf("passive: the record holds %+v, want nothing", e.borrowing.inFlight.Entries())
		}
	}
}

// observePassive resolves each variant's mode from the registry the scaler
// fills -- our trigger beside a foreign one is observe -- and publishes
// wva_passive_mode; a variant that leaves loses its series.
func TestObservePassiveThroughTheRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	if err := metrics.InitMetrics(reg); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewTestConfig()
	yes := true
	cfg.UpdateScalingPolicyConfig(map[string]config.ScalingPolicy{
		"m": {ModelID: "acme/chat", Namespace: "ns", Passive: &yes}})
	vars := registry.New(0)
	vars.Observe("ns", "chat-so", map[string]string{registry.ModelIDKey: "acme/chat"})
	vars.SetTarget("ns", "chat-so", registry.Target{Name: "chat-deploy", ForeignTriggers: 1})
	e := &Engine{Config: cfg, Variants: vars, Passive: &passive.Resolver{Config: cfg, Registry: vars}}
	va := variant.VariantAutoscaling{
		ObjectMeta: metav1.ObjectMeta{Name: "chat-so", Namespace: "ns"},
		Spec: variant.VariantAutoscalingSpec{ModelID: "acme/chat",
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "chat-deploy"}},
	}
	e.observePassive(logr.Discard(), []variant.VariantAutoscaling{va})
	if got := e.cyclePassiveVariants["ns/chat-deploy"]; got != passive.Observe {
		t.Fatalf("mode %q, want observe", got)
	}
	if !e.modelPassive("ns", "acme/chat") {
		t.Fatal("the model is not marked passive for the cycle")
	}
	modes := func() map[string]float64 {
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]float64{}
		for _, mf := range mfs {
			if mf.GetName() != constants.WVAPassiveMode {
				continue
			}
			for _, m := range mf.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == constants.LabelMode {
						out[l.GetValue()] = m.GetGauge().GetValue()
					}
				}
			}
		}
		return out
	}
	if got := modes(); got[constants.PassiveModeObserve] != 1 || got[constants.PassiveModeActive] != 0 || len(got) != 4 {
		t.Fatalf("wva_passive_mode = %v, want observe=1 and the other three 0", got)
	}
	e.observePassive(logr.Discard(), nil)
	if got := modes(); len(got) != 0 {
		t.Fatalf("wva_passive_mode after the variant left = %v, want no series", got)
	}
}

// A model that goes passive mid-transfer puts its share group in shadow. For
// the absence grace the transfer in flight is held -- the donor stays at its
// lowered target, the passive model at its own count -- and the marks stay (a
// failed read must not drop them); then the ledger goes, no target is left,
// and the marks are removed. Removing them restores the pods' own deletion
// cost: the one write the share makes after a model went passive.
func TestPassiveMidTransferUnmarksAfterTheGrace(t *testing.T) {
	f := newShareFleet()
	c := sharePods(t, f)
	se := newShareEngine(t, f, c, time.Unix(0, 0))
	if started := se.untilStarted(); started < 1 {
		t.Fatal("setup: no transfer started")
	}
	if len(markedPods(t, c)) == 0 {
		t.Fatal("setup: no donor pod marked")
	}
	se.e.cyclePassive = map[string]bool{"ns/B": true}
	o := se.cycle()
	if b, ok := o["ns/B-v"]; ok && b.Target != f.current["B"] {
		t.Fatalf("the passive model's target moved to %d, want its count %d", b.Target, f.current["B"])
	}
	if len(markedPods(t, c)) == 0 {
		t.Fatal("the marks were dropped at once; they are kept through the absence grace")
	}
	for range 5 { // 30 s cycles past the 2 min grace
		o = se.cycle()
	}
	if len(o) != 0 {
		t.Fatalf("targets after the grace: %+v, want none", o)
	}
	if n := len(markedPods(t, c)); n != 0 {
		t.Fatalf("%d donor pods still marked after the grace", n)
	}
}

// A held variant whose ScaledObject sets spec.fallback is warned about once:
// an unreadable count makes the scaler answer an error, and past
// failureThreshold KEDA sets fallback.replicas.
func TestHeldVariantWithFallbackIsWarnedOnce(t *testing.T) {
	warned := 0
	logger := funcr.New(func(_, args string) {
		if strings.Contains(args, "spec.fallback") {
			warned++
		}
	}, funcr.Options{})
	e := &Engine{}
	va := &variant.VariantAutoscaling{ObjectMeta: metav1.ObjectMeta{Name: "so", Namespace: "ns"},
		Spec: variant.VariantAutoscalingSpec{ModelID: "m"}}
	held := passiveVariantState{mode: passive.Hold, fallback: true}
	e.reportPassiveChange(logger, va, "d", passiveVariantState{}, held, registry.Target{HasFallback: true})
	e.reportPassiveChange(logger, va, "d", held, held, registry.Target{HasFallback: true})
	if warned != 1 {
		t.Fatalf("warned %d times over two cycles, want 1", warned)
	}
}

// A share group that goes to shadow because it holds a passive model says so
// on wva_utilization_share_passive_shadow.
func TestPassiveShadowGroupIsNamed(t *testing.T) {
	reg := prometheus.NewRegistry()
	if err := metrics.InitMetrics(reg); err != nil {
		t.Fatal(err)
	}
	f := newShareFleet()
	clock := time.Unix(0, 0)
	e := &Engine{Config: shadowConfig(t, activeShare), client: sharePods(t, f)}
	e.utilizationShare.now = func() time.Time { return clock }
	e.cyclePassive = map[string]bool{"ns/A": true}
	ctx, _ := observe(t)
	clock = clock.Add(30 * time.Second)
	e.evaluateUtilizationShare(ctx, f.requests(), fullQuota(), f.scaleTargets())
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == constants.WVAUtilizationSharePassiveShadow {
			for _, m := range mf.GetMetric() {
				if m.GetGauge().GetValue() == 1 {
					return
				}
			}
		}
	}
	t.Fatalf("no %s series at 1", constants.WVAUtilizationSharePassiveShadow)
}

package scalefromzero

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
)

// lentOutProvider is "chat" on a reclaimWithinCohort Never queue of nominal 8
// that lent 6 to a peer: guaranteed 2, and the cohort's free pool 0, so within
// nominal is 2, not 8. lent false is the control: the same 2 GPUs of room
// gone, nothing lent.
type lentOutProvider struct{ notLent bool }

// lentOutName is lentOutProvider's name.
const lentOutName = "lent-out-quota"

func (lentOutProvider) Name() string { return lentOutName }
func (p lentOutProvider) ComputeConstraints(context.Context, map[string]int, map[string]map[string]int) (*allocation.ResourceConstraints, error) {
	lent := 6
	if p.notLent {
		lent = 0
	}
	return &allocation.ResourceConstraints{
		ProviderName:   lentOutName,
		Pools:          map[string]allocation.ResourcePool{"H100": {Limit: config.QuotaUnlimited}},
		NamespacePools: map[string]map[string]allocation.ResourcePool{"chat": {"H100": {Limit: 2, Used: 2}}},
		NamespaceBorrow: map[string]map[string]allocation.NamespaceBorrow{
			"chat": {"H100": {Cap: config.QuotaUnlimited, WithinNominal: 2, NominalLentOut: lent}},
		},
		NamespaceCohorts: map[string][]string{"chat": {"c"}},
	}, nil
}

// B10, review T2: a wake is checked against guaranteed plus the free pool --
// within nominal -- never the whole nominal a peer holds and need not give
// back. The wake's outcome path names the refusal nominal-lent-out, and so
// does wva_model_scaling_blocked; a namespace that lent nothing is refused as
// no-capacity, with no such reason, and a model that later wakes clears it.
func TestWakeIntoLentNominalReportsNominalLentOut(t *testing.T) {
	ctx := context.Background()
	decision.DefaultGPUUsage.Reset()
	t.Cleanup(decision.DefaultGPUUsage.Reset)
	decision.PublishGPUUsage(map[string]int{"H100": 2}, nil)
	allocation.DefaultBorrowInFlight.Reset()
	registry := prometheus.NewRegistry()
	if err := metrics.InitMetrics(registry); err != nil {
		t.Fatal(err)
	}
	group := modelGroup{namespace: "chat", modelID: "m"}
	set := []Candidate{cand("d", "decode", "H100", 1, 1)}

	for _, tc := range []struct {
		name    string
		p       lentOutProvider
		outcome SelectionOutcome
		reason  string
	}{
		{"nominal lent out: nominal-lent-out, outcome and metric", lentOutProvider{}, OutcomeNominalLentOut, "nominal-lent-out"},
		{"control: nothing lent out: no-capacity, and the reason clears", lentOutProvider{notLent: true}, OutcomeNoCapacity, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{gpuLimiter: tc.p}
			if tc.reason == "" {
				// The same model refused for the lent nominal by an earlier tick.
				e.reportLentOutBlocked(group, true)
			}
			_, selected, outcome := e.wakeVerdict(ctx, group, set, coverage{})
			if len(selected) != 0 || outcome != tc.outcome {
				t.Fatalf("woke %v (%s), want nothing (%s)", names(selected), outcome, tc.outcome)
			}
			if got := wakeReasons(t, registry)["m"]; got != tc.reason {
				t.Fatalf("wva_model_scaling_blocked reason %q, want %q", got, tc.reason)
			}
		})
	}
}

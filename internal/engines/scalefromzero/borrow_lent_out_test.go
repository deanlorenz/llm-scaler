package scalefromzero

import (
	"context"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
)

// lentOutProvider is "chat" on a reclaimWithinCohort Never queue of nominal 8
// that lent 6 to a peer: guaranteed 2, and the cohort's free pool 0, so within
// nominal is 2, not 8.
type lentOutProvider struct{}

// lentOutName is lentOutProvider's name.
const lentOutName = "lent-out-quota"

func (lentOutProvider) Name() string { return lentOutName }
func (lentOutProvider) ComputeConstraints(context.Context, map[string]int, map[string]map[string]int) (*allocation.ResourceConstraints, error) {
	return &allocation.ResourceConstraints{
		ProviderName:   lentOutName,
		Pools:          map[string]allocation.ResourcePool{"H100": {Limit: config.QuotaUnlimited}},
		NamespacePools: map[string]map[string]allocation.ResourcePool{"chat": {"H100": {Limit: 2, Used: 2}}},
		NamespaceBorrow: map[string]map[string]allocation.NamespaceBorrow{
			"chat": {"H100": {Cap: config.QuotaUnlimited, WithinNominal: 2, NominalLentOut: 6}},
		},
		NamespaceCohorts: map[string][]string{"chat": {"c"}},
	}, nil
}

// B10: a wake is checked against guaranteed plus the free pool -- within
// nominal -- never the whole nominal a peer holds and need not give back; the
// refusal says so (nominal-lent-out) instead of waking into a gated pod.
func TestWakeIntoLentNominalReportsNominalLentOut(t *testing.T) {
	ctx := context.Background()
	decision.DefaultGPUUsage.Reset()
	t.Cleanup(decision.DefaultGPUUsage.Reset)
	decision.PublishGPUUsage(map[string]int{"H100": 2}, nil)

	e := &Engine{gpuLimiter: lentOutProvider{}}
	p := e.placementFor(ctx, "chat")
	if !p.nominalLentOut {
		t.Error("the placement does not say the nominal is lent out")
	}
	if allocation.FitsGPUBudget(p.constraints, "chat", map[string]int{"H100": 1}) {
		t.Error("a wake fits 6 GPUs of nominal a peer holds and need not give back")
	}
	if OutcomeNominalLentOut != "nominal-lent-out" {
		t.Errorf("outcome %q, want the nominal-lent-out reason", OutcomeNominalLentOut)
	}
}

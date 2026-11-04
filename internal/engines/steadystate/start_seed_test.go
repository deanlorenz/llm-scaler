package steadystate

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/registry"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/scalingpolicy"
	llmdVariantAutoscalingV1alpha1 "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/variant"
)

// startSeedEngine is an engine with an empty registry and a reporter.
func startSeedEngine() (*Engine, *registry.Registry) {
	reg := registry.New(0)
	return &Engine{Variants: reg, policies: scalingpolicy.NewChangeReporter()}, reg
}

// observeStartSeed registers ns/v with the given replicaStartSeconds, ""
// for none.
func observeStartSeed(reg *registry.Registry, raw string) {
	md := map[string]string{registry.ModelIDKey: "m"}
	if raw != "" {
		md[registry.ReplicaStartSecondsKey] = raw
	}
	reg.Observe("ns", "v", md)
}

// The seed travels trigger metadata -> registry -> the variant's state, set
// with its presence bit; a variant with none, or one the registry does not
// hold, is left unset.
func TestApplyStartSeeds_PutsTheSeedOnItsVariant(t *testing.T) {
	e, reg := startSeedEngine()
	observeStartSeed(reg, "120")
	reg.Observe("ns", "plain", map[string]string{registry.ModelIDKey: "m"})
	states := []domain.VariantReplicaState{{VariantName: "v"}, {VariantName: "plain"}, {VariantName: "unknown"}}

	e.applyStartSeeds(context.Background(), "ns", states)

	assert.True(t, states[0].StartSeedSet)
	assert.Equal(t, 120.0, states[0].StartSeedSeconds)
	for _, s := range states[1:] {
		assert.False(t, s.StartSeedSet, "%s has no seed", s.VariantName)
		assert.Zero(t, s.StartSeedSeconds, "%s has no seed", s.VariantName)
	}
}

// Another namespace's entry of the same name does not seed this one.
func TestApplyStartSeeds_IsKeyedByNamespace(t *testing.T) {
	e, reg := startSeedEngine()
	observeStartSeed(reg, "120")
	states := []domain.VariantReplicaState{{VariantName: "v"}}
	e.applyStartSeeds(context.Background(), "other", states)
	assert.False(t, states[0].StartSeedSet)
}

// A bad value leaves the variant unseeded and is logged once each time the
// value written changes: not every cycle, and again when a fixed value turns
// bad once more.
func TestApplyStartSeeds_ABadSeedIsIgnoredAndLoggedOncePerChange(t *testing.T) {
	var lines []string
	ctx := logr.NewContext(context.Background(),
		funcr.New(func(_, args string) { lines = append(lines, args) }, funcr.Options{}))
	e, reg := startSeedEngine()
	run := func(raw string) domain.VariantReplicaState {
		observeStartSeed(reg, raw)
		states := []domain.VariantReplicaState{{VariantName: "v"}}
		e.applyStartSeeds(ctx, "ns", states)
		return states[0]
	}

	for _, step := range []struct {
		raw   string
		set   bool
		lines int
	}{
		{raw: "garbage", lines: 1},
		{raw: "garbage", lines: 1}, // the next cycle: the same value, not logged again
		{raw: "garbage", lines: 1},
		{raw: "-5", lines: 2}, // changed, still bad
		{raw: "NaN", lines: 3},
		{raw: "0", lines: 4},
		{raw: "120", set: true, lines: 4}, // fixed: nothing to say
		{raw: "", lines: 4},               // removed: nothing to say
		{raw: "garbage", lines: 5},        // bad again after the fix: said again
		{raw: " 120", lines: 6},           // a stray space: ignored, and said
		{raw: "0.5", lines: 7},            // under a second
	} {
		st := run(step.raw)
		assert.Equal(t, step.set, st.StartSeedSet, "seed %q", step.raw)
		if !step.set {
			assert.Zero(t, st.StartSeedSeconds, "seed %q", step.raw)
		}
		require.Len(t, lines, step.lines, "after seed %q", step.raw)
	}
	assert.True(t, strings.Contains(lines[0], `"replicaStartSeconds"="garbage"`), lines[0])
	assert.True(t, strings.Contains(lines[0], `"variant"="v"`), lines[0])
	assert.True(t, strings.Contains(lines[0], `"maxSeconds"=3600`), lines[0])
}

// A bad seed never turns into a refused trigger: the rest of the variant's
// metadata is read, so the model keeps scaling.
func TestApplyStartSeeds_ABadSeedIsNotATriggerError(t *testing.T) {
	_, err := registry.ParseMeta(map[string]string{registry.ModelIDKey: "m", registry.ReplicaStartSecondsKey: "garbage"})
	assert.NoError(t, err)
}

// No registry, no seed, and no panic: a test-built engine has none.
func TestApplyStartSeeds_NoRegistry(t *testing.T) {
	states := []domain.VariantReplicaState{{VariantName: "v"}}
	assert.NotPanics(t, func() { (&Engine{}).applyStartSeeds(context.Background(), "ns", states) })
	assert.False(t, states[0].StartSeedSet)
}

// The seed reaches the analyzer input through prepareModelData, not only
// through applyStartSeeds called by hand: a registry entry seeding 120 s
// gives the variant's state the seed and its presence bit. Calling it
// before the states are built, or not at all, fails here.
func TestPrepareModelData_CarriesTheStartSeed(t *testing.T) {
	e, _ := engineConfigEngine(t)
	reg := registry.New(0)
	reg.Observe("ns", "v", map[string]string{registry.ModelIDKey: "m", registry.ReplicaStartSecondsKey: "120"})
	e.Variants, e.policies = reg, scalingpolicy.NewChangeReporter()
	d := deployWithArgs("--max-num-seqs=128")
	one := int32(1)
	d.Spec.Replicas = &one
	d.Status.Replicas, d.Status.ReadyReplicas = 1, 1
	k8s := fake.NewClientBuilder().WithObjects(d).Build()
	e.ReplicaMetricsCollector = collector.NewReplicaMetricsCollector(
		replicaSource{modelID: "m", pods: []string{"v" + podSuffix}}, k8s, nil, nil, variantOfPodLocator{})

	md, err := e.prepareModelData(context.Background(), "m",
		[]llmdVariantAutoscalingV1alpha1.VariantAutoscaling{parkedVA()}, k8s)
	require.NoError(t, err)
	require.NotNil(t, md, "precondition: the model has a replica row, so it is not skipped")
	require.Len(t, md.variantStates, 1)
	assert.True(t, md.variantStates[0].StartSeedSet, "the seed's presence bit")
	assert.Equal(t, 120.0, md.variantStates[0].StartSeedSeconds)
}

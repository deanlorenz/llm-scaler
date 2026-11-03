package saturation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// Issue #438: the derived estimate for a variant at zero replicas is one
// engine rank's figure, and a pod of several data-parallel ranks runs that
// many engines. With SwitchZeroReplicaDataParallel on it is multiplied by the
// ranks the flags state, as computeK2 multiplies a live replica's by the
// ranks it reports. The model runs 1000 in / 500 out with equal lengths under
// 8192 batched tokens, so one rank is S x 1250.

// zeroReplicaDPOff turns SwitchZeroReplicaDataParallel off.
func zeroReplicaDPOff(a *SaturationAnalyzer) { a.zeroReplicaDP = false }

// dpParams is an engine of dp data-parallel ranks of tp GPUs each, with
// no cap in its flags, whose scale target declares the cap declared (0 for
// none).
func dpParams(dp, tp int, declared int64) *capacity.EngineParams {
	p := declaredParams(declared)
	p.DataParallelSize, p.TensorParallelSize = dp, tp
	return p
}

// dpEstimate runs one cycle through Analyze with a live decode variant on
// H100 and variant-z at zero replicas in role on gpus A100s, its record
// derived from params and changed by rec, beside the records in also. It
// returns the deployment-derived zero-replica-capacity-estimate line and the
// per-replica capacity Analyze published for variant-z.
func dpEstimate(t *testing.T, params *capacity.EngineParams, role string, gpus int,
	rec func(*capacity.Record), also map[string]capacity.Record, edit func(*SaturationAnalyzer)) (map[string]any, float64) {
	t.Helper()
	ctx, logs := observedCtx(t)
	store := capacity.NewStore()
	r := capacity.Record{
		AcceleratorName: "A100", GpuCount: gpus, EngineParams: params,
		EffectiveCapacity: 1, LearnedFrom: "deployment",
	}
	if rec != nil {
		rec(&r)
	}
	store.Update("test-ns", "test-model", zeroVariant, r, time.Now())
	for name, other := range also {
		store.Update("test-ns", "test-model", name, other, time.Now())
	}
	a := NewSaturationAnalyzer(store)
	if edit != nil {
		edit(a)
	}
	rm := makeReplicaMetrics("pod-1", "variant-a", 50_000, 1_000_000, 0, 1000, 500)
	in := makeAnalyzerInput([]domain.ReplicaMetrics{rm}, []domain.VariantReplicaState{
		{VariantName: "variant-a", Role: domain.RoleDecode, AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
		{VariantName: zeroVariant, Role: role, AcceleratorName: "A100", CurrentReplicas: 0, GPUsPerReplica: gpus},
	})
	result, err := a.Analyze(ctx, in)
	require.NoError(t, err)
	var line map[string]any
	for _, e := range logs.FilterMessage("zero-replica-capacity-estimate").All() {
		if f := e.ContextMap(); f["variant"] == zeroVariant && f["source"] == derivedEstimate {
			line = f
		}
	}
	require.NotNil(t, line, "no deployment-derived estimate for variant-z")
	for _, vc := range result.VariantCapacities {
		if vc.VariantName == zeroVariant {
			return line, vc.PerReplicaCapacity
		}
	}
	require.FailNow(t, "Analyze published no capacity for variant-z")
	return nil, 0
}

// A decode pod of 8 ranks that declares 64 per rank is priced at all eight
// from zero: 8 x 64 x 1250 = 640,000, not one rank's 80,000. The cap the
// line names stays per rank.
func TestZeroReplicaDataParallel_AnEightRankDecodeIsPricedAtEveryRank(t *testing.T) {
	f, perReplica := dpEstimate(t, dpParams(8, 1, 64), domain.RoleDecode, 8, nil, nil, nil)
	assert.EqualValues(t, 640_000, f["derivedCapacity"])
	assert.EqualValues(t, 640_000, f["perReplicaCapacity"])
	assert.EqualValues(t, 640_000, perReplica)
	assert.EqualValues(t, 64, f["batchCap"])
	assert.Equal(t, batchCapDeclared, f["batchCapSource"])
	assert.EqualValues(t, 8, f["engineRanks"])
}

// With no cap declared, the whole-node 8-GPU pod of 8 ranks is priced at the
// default per rank: 8 x 256 x 1250 = 2,560,000. Four ranks of two GPUs on the
// same node are 4 x 320,000: the ranks multiply, not the GPUs.
func TestZeroReplicaDataParallel_AWholeNodePodWithNoDeclaredCap(t *testing.T) {
	f, perReplica := dpEstimate(t, dpParams(8, 1, 0), domain.RoleDecode, 8, nil, nil, nil)
	assert.EqualValues(t, 2_560_000, f["derivedCapacity"])
	assert.EqualValues(t, 2_560_000, perReplica)
	assert.EqualValues(t, 256, f["batchCap"])
	assert.Equal(t, batchCapDefault, f["batchCapSource"])
	assert.EqualValues(t, 8, f["engineRanks"])

	f, perReplica = dpEstimate(t, dpParams(4, 2, 0), domain.RoleDecode, 8, nil, nil, nil)
	assert.EqualValues(t, 1_280_000, f["derivedCapacity"])
	assert.EqualValues(t, 1_280_000, perReplica)
	assert.EqualValues(t, 4, f["engineRanks"])

	// An aggregated variant scales as decode does.
	f, _ = dpEstimate(t, dpParams(8, 1, 64), domain.RoleBoth, 8, nil, nil, nil)
	assert.EqualValues(t, 640_000, f["derivedCapacity"])
}

// Off, every rank count prices as one rank, as before #438.
func TestZeroReplicaDataParallel_TheSwitchPicksTheRanks(t *testing.T) {
	f, perReplica := dpEstimate(t, dpParams(8, 1, 64), domain.RoleDecode, 8, nil, nil, zeroReplicaDPOff)
	assert.EqualValues(t, 80_000, f["derivedCapacity"])
	assert.EqualValues(t, 80_000, perReplica)
	assert.EqualValues(t, 1, f["engineRanks"])

	f, perReplica = dpEstimate(t, dpParams(8, 1, 0), domain.RoleDecode, 8, nil, nil, zeroReplicaDPOff)
	assert.EqualValues(t, 320_000, f["derivedCapacity"])
	assert.EqualValues(t, 320_000, perReplica)
}

// The clamps the path applies bound the pod's figure as they bound one
// rank's: what the variant was last measured at, its own k1 (a carried KV
// total, the sum over ranks), and a compatible variant's live figure. Each
// sits between one rank's 320,000 and the pod's 2,560,000, so off it does
// not bind and on it does.
func TestZeroReplicaDataParallel_TheClampsStillBound(t *testing.T) {
	sibling := capacity.Record{
		AcceleratorName: "A100", GpuCount: 8, EngineParams: dpParams(8, 1, 0),
		EffectiveCapacity: 900_000, LearnedFrom: capacity.LearnedFromLive,
	}
	for _, tc := range []struct {
		name  string
		rec   func(*capacity.Record)
		also  map[string]capacity.Record
		bound int64
		by    string
	}{
		{"prior-live", func(r *capacity.Record) { r.PriorLiveCapacity = 500_000 }, nil, 500_000, "prior-live"},
		{"own-k1", func(r *capacity.Record) { r.TotalKvCapacityTokens = 1_000_000 }, nil, 800_000, "own-k1"},
		{"compatible", nil, map[string]capacity.Record{"variant-s": sibling}, 900_000, "compatible-variant-live"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, perReplica := dpEstimate(t, dpParams(8, 1, 0), domain.RoleDecode, 8, tc.rec, tc.also, nil)
			assert.EqualValues(t, 2_560_000, f["derivedCapacity"])
			assert.EqualValues(t, tc.bound, perReplica)
			assert.Equal(t, tc.by, f["boundedBy"])

			f, perReplica = dpEstimate(t, dpParams(8, 1, 0), domain.RoleDecode, 8, tc.rec, tc.also, zeroReplicaDPOff)
			assert.EqualValues(t, 320_000, perReplica)
			assert.Equal(t, "derived", f["boundedBy"])
		})
	}
}

// Controls, the same with the switch on or off: one rank (stated, or not
// stated at all) prices as before, and a prefill variant keeps one rank's
// figure, as it keeps the flags' cap (zeroReplicaRanks).
func TestZeroReplicaDataParallel_OneRankAndPrefillAreUnchanged(t *testing.T) {
	for _, edit := range []func(*SaturationAnalyzer){nil, zeroReplicaDPOff} {
		for _, dp := range []int{0, 1} {
			f, perReplica := dpEstimate(t, dpParams(dp, 1, 0), domain.RoleDecode, 1, nil, nil, edit)
			assert.EqualValues(t, 320_000, f["derivedCapacity"])
			assert.EqualValues(t, 320_000, perReplica)
			assert.EqualValues(t, 1, f["engineRanks"])
		}
		f, perReplica := dpEstimate(t, dpParams(8, 1, 0), domain.RolePrefill, 8, nil, nil, edit)
		assert.EqualValues(t, 320_000, f["derivedCapacity"])
		assert.EqualValues(t, 320_000, perReplica)
		assert.EqualValues(t, 1, f["engineRanks"])
	}
}

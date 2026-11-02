package saturation

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// Issue #427: the estimate for a variant at zero replicas reads the cap the
// scale target states -- the flags, else the llm-d.ai/max-num-seqs
// annotation -- as the derived k2 does once a pod runs. The model runs 1000
// in / 500 out with equal lengths, under 8192 batched tokens, so the budget
// allows 2730 sequences and the cap binds: the estimate is S x 1250.

// estimateCapacityFromParams is the derived k2, with equal output lengths,
// at the cap the flags state, else the parser's default
// (EngineParams.BatchCap): the figure the zero-replica estimate priced before
// #427, and the formula the tests pin. 0 for nil params or no budget.
func estimateCapacityFromParams(params *capacity.EngineParams, avgInput, avgOutput float64) int64 {
	if params == nil || params.EffectiveMaxBatchedTokens <= 0 {
		return 0
	}
	return estimateCapacityAtCapSpread(params, params.BatchCap(), avgInput, avgOutput, 1)
}

// zeroReplicaCapOff turns SwitchBatchCapZeroReplica off.
func zeroReplicaCapOff(a *SaturationAnalyzer) { a.batchCapZeroReplica = false }

// flagParams is an engine whose flags state perRank, and whose scale target
// declares declared.
func flagParams(perRank, declared int64) *capacity.EngineParams {
	p := deploymentParams()
	p.MaxNumSeqs, p.MaxNumSeqsSet, p.DeclaredBatchCap = perRank, true, declared
	return p
}

// A variant that declares 64 is priced at 64 sequences from zero, as its
// first replica will be: 64 x 1250 = 80,000, not 256 x 1250 = 320,000.
func TestBatchCapZeroReplica_TheEstimateIsPricedAtTheDeclaredCap(t *testing.T) {
	f := zeroReplicaEstimateOf(t, declaredParams(64), 1, nil)
	assert.EqualValues(t, 80_000, f["derivedCapacity"])
	assert.EqualValues(t, 80_000, f["perReplicaCapacity"])
	assert.EqualValues(t, 64, f["batchCap"])
	assert.Equal(t, batchCapDeclared, f["batchCapSource"])
}

// A declared cap above 256 raises the estimate, as it raises the derived k2:
// 512 x 1250 = 640,000. The annotation is the operator's statement of the
// cap the engine runs.
func TestBatchCapZeroReplica_ADeclaredCapAbove256RaisesTheEstimate(t *testing.T) {
	f := zeroReplicaEstimateOf(t, declaredParams(512), 1, nil)
	assert.EqualValues(t, 640_000, f["derivedCapacity"])
	assert.EqualValues(t, 512, f["batchCap"])
	assert.Equal(t, batchCapDeclared, f["batchCapSource"])
}

// The flags' cap wins over the declared one either way: 128 x 1250 =
// 160,000. With nothing stated, the default prices it, and the line says so.
func TestBatchCapZeroReplica_TheFlagsWinAndTheDefaultIsNamed(t *testing.T) {
	for _, edit := range []func(*SaturationAnalyzer){nil, zeroReplicaCapOff} {
		f := zeroReplicaEstimateOf(t, flagParams(128, 64), 1, edit)
		assert.EqualValues(t, 160_000, f["derivedCapacity"])
		assert.EqualValues(t, 128, f["batchCap"])
		assert.Equal(t, batchCapFromFlags, f["batchCapSource"])
	}
	f := zeroReplicaEstimateOf(t, deploymentParams(), 1, nil)
	assert.EqualValues(t, 320_000, f["derivedCapacity"])
	assert.EqualValues(t, 256, f["batchCap"])
	assert.Equal(t, batchCapDefault, f["batchCapSource"])
}

// Off, the estimate prices at the flags' cap or the parser's default, as
// before #427, whatever is declared; and the declared cap's own switch
// (SwitchBatchCapDeclared) turns it off here too, as it does for P3.
func TestBatchCapZeroReplica_TheSwitchPicksTheSource(t *testing.T) {
	f := zeroReplicaEstimateOf(t, declaredParams(64), 1, zeroReplicaCapOff)
	assert.EqualValues(t, 320_000, f["derivedCapacity"])
	assert.EqualValues(t, 256, f["batchCap"])
	assert.Equal(t, batchCapDefault, f["batchCapSource"])

	f = zeroReplicaEstimateOf(t, declaredParams(64), 1, func(a *SaturationAnalyzer) { a.batchCapDeclared = false })
	assert.EqualValues(t, 320_000, f["derivedCapacity"])
	assert.Equal(t, batchCapDefault, f["batchCapSource"])
}

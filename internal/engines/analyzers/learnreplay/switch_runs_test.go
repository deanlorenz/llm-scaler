package learnreplay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/saturation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/slo"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/throughput"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learnrec"
)

// The switch runs of the golden rule (analyzer-learner.md 2.5), over every
// committed golden, for this branch's switches: all off, each alone, all on.
//
//   - All on is the committed goldens (the golden check holds that too).
//   - All off is the base commit's goldens: an output no switch names is the
//     committed file, and an output a switch names is the base's, by digest
//     (switchBase).
//   - Each switch alone moves, from all off, only the outputs it names.
//
// A PR that adds a switch adds it to replaySwitches, and the base digests of
// the outputs it names. An earlier PR's switch whose seam is still in the
// code moves to earlierSeams, and is replayed off alone from all on; the PR
// that deletes a seam deletes its row. Every name SetSwitchesForReplay
// accepts has a row in one of the two.

// replaySwitches is each replay switch of this PR, saturation's or slo's
// (switchPackage), and the golden outputs (fixture/file) it is named to move.
//
// #275 prices an aggregated variant's ITL line by request shape, in two
// switches. Its base is main after #432 (67790e633): #432's switch is an earlier
// seam here (earlierSeams), on in every run below. Measured, each alone
// against all off:
//
//   - SwitchITLAggregatedShapeTerm moves acceptance/learned-state.csv and
//     engine/learned-state.csv, one row each export (two per file): model-01's
//     |both line now carries the shape it was fitted at (ITLLineShapes, g
//     257.14 at the fixture's shape), with no prefill reading (sigmaOK false:
//     the recordings carry no running-request count or completion rate on
//     the aggregated rows) and no point (a point needs one). Nothing else
//     moves: with no prefill reading the reported figure is #432's, on the
//     same case (the shape never moves past the tolerance in either fixture).
//   - SwitchITLAggregatedSizes moves nothing: it lifts only a figure with a
//     prefill reading and a GPS check that agreed, and no fixture has
//     either for an aggregated variant. Its unit tests hold it (heldBy), as
//     the shape term's cases, c, the prefill correction and the cap at a
//     measured rate are held by theirs (itl_aggregated_shape_test.go).
//
// Both on moves nothing past the shape term's two files: no interaction.
//
// #438 prices a vLLM data-parallel decode or aggregated variant at zero
// replicas at every engine rank its leader pod runs, not one: the derived
// figure, and the stored budget it falls back to. It moves no golden: no
// recorded scale target states a data-parallel size above 1, so every
// zero-replica estimate the fixtures price is one rank's either way. Its unit
// tests hold it (heldBy).
//
// Earlier PRs' switches (learner PRs 2, 3 and 4a, #396's bands, #399's
// batch cap, #415's mu key, #392's first k2 reading, #420's batch-cap
// readers, #274's ITL per accepted token, #276's output spread, #427's
// zero-replica batch cap, #431's forget of the slo line, #277's resident
// prefix-cache hits, #432's aggregated line) are not here: "all off" is this
// PR's switches, and an earlier PR's stay at their constructor default, on,
// as the base goldens were written (analyzer-learner.md, 2.5).
//
// Since learner PR 4b-1 the rows above (#275's two switches, #438's) are
// earlier seams (earlierSeams), and this PR's are the throughput analyzer's
// two: its ITL line on the shared tracker (throughput.SwitchITLSteadyGate)
// and the GPS-mismatch clear dropped
// (throughput.SwitchITLKeepsWindowOnGPSMismatch). Its base is f7b4f7e47
// (4b-0). The fixtures that run the throughput analyzer are the in-process
// three (acceptance, engine, mu-paths); no cluster recording does yet.
// Measured, each alone against all off:
//
//   - The steady gate moves the throughput rows of results.csv and
//     variant-capacities.csv in all three, and, through the composite, the
//     engine fixture's composite-holds, composite-release and
//     composite-variants. Its line needs 10 readings in the window and a
//     settled replica's, where the parent priced the first cycle's rows: the
//     first cycles of each variant have no throughput capacity.
//   - The GPS-mismatch switch alone moves the throughput rows of
//     variant-capacities.csv in all three: the in-process rows trip the check,
//     and kept, the window reaches a full fit where the clear had emptied it.
var replaySwitches = map[string][]string{
	throughput.SwitchITLSteadyGate: {
		"acceptance/results.csv", "acceptance/variant-capacities.csv",
		"engine/composite-holds.csv", "engine/composite-release.csv", "engine/composite-variants.csv",
		"engine/results.csv", "engine/variant-capacities.csv",
		"mu-paths/results.csv", "mu-paths/variant-capacities.csv",
	},
	throughput.SwitchITLKeepsWindowOnGPSMismatch: {
		"acceptance/variant-capacities.csv", "engine/variant-capacities.csv", "mu-paths/variant-capacities.csv",
	},
}

// learner4aSeams is learner PR 4a's switches, earlier seams now (merged
// into earlierSeams): the ITL line, and the slo analyzer's line, on the
// settle tracker, six switches each, with the outputs 4a named each to
// move. The restart wait moves the in-process runs and
// ols-sweep-slo-restart; the move itself the in-process runs on the cycles
// after their clock jumps a day (settle.Rejoined); the other four move
// nothing and are held by their unit tests (heldBy).
var learner4aSeams = map[string][]string{
	// Since #432 the move reaches the aggregated line of the acceptance and
	// engine fixtures too: off, model-01's variant-01 does not settle in
	// time for its first fit at 09:32:00, so it publishes no |both line that
	// cycle, variant-02 borrows none, and their reported service rates and
	// the learned-state export lose them, as the slo analyzer's line does
	// under slo.SwitchITLReadsSettle.
	saturation.SwitchITLReadsSettle: {
		"acceptance/learned-state.csv", "acceptance/lines.csv", "acceptance/variant-capacities.csv",
		"engine/learned-state.csv", "engine/lines.csv", "engine/variant-capacities.csv",
	},
	saturation.SwitchITLWaitsAfterRestart: {
		"acceptance/learned-state.csv", "acceptance/lines.csv", "acceptance/variant-capacities.csv",
		"engine/composite-variants.csv", "engine/learned-state.csv", "engine/lines.csv", "engine/variant-capacities.csv",
		"mu-paths/learned-state.csv", "mu-paths/lines.csv", "mu-paths/variant-capacities.csv",
		"ols-sweep-slo-restart/composite-results.csv", "ols-sweep-slo-restart/lines.csv",
		"ols-sweep-slo-restart/results.csv", "ols-sweep-slo-restart/variant-capacities.csv",
	},
	saturation.SwitchITLKeepsSettleThroughMissingRow: nil,
	saturation.SwitchITLReadsFinitePodMean:           nil,
	saturation.SwitchITLRestartUnderName:             nil,
	saturation.SwitchITLIdleOnEvidence:               nil,
	slo.SwitchITLReadsSettle: {
		"acceptance/slo-published.csv", "acceptance/variant-capacities.csv",
		"engine/slo-published.csv", "engine/variant-capacities.csv",
	},
	slo.SwitchITLWaitsAfterRestart: {
		"acceptance/slo-published.csv", "acceptance/variant-capacities.csv",
		"engine/slo-published.csv", "engine/variant-capacities.csv",
		"mu-paths/slo-published.csv", "mu-paths/variant-capacities.csv",
		"ols-sweep-slo-restart/slo-published.csv", "ols-sweep-slo-restart/variant-capacities.csv",
	},
	slo.SwitchITLKeepsSettleThroughMissingRow: nil,
	slo.SwitchITLReadsFinitePodMean:           nil,
	slo.SwitchITLRestartUnderName:             nil,
	slo.SwitchITLIdleOnEvidence:               nil,
}

// switchPackage is the package whose hook accepts the switch name.
func switchPackage(name string) string {
	if slices.Contains(slo.ReplaySwitchNames(), name) {
		return PackageSLO
	}
	if slices.Contains(throughput.ReplaySwitchNames(), name) {
		return PackageThroughput
	}
	return PackageSaturation
}

// earlierSeams is each switch an earlier learner PR added whose seam the
// code still has, and the golden outputs that PR named it to move. Each
// stays on, at its constructor default, in this PR's runs; off alone, from
// all on, it may move only what it named. A seam's row goes with the seam,
// in the PR, or cleanup PR, that deletes it with zero golden diff. Learner
// PR 4a's rows are learner4aSeams, merged in.
var earlierSeams = withSeams(learner4aSeams, map[string][]string{
	// #275 (two switches) and #438, this file's own rows before learner PR
	// 4b-1 (see replaySwitches).
	saturation.SwitchZeroReplicaDataParallel: nil,
	saturation.SwitchITLAggregatedShapeTerm:  {"acceptance/learned-state.csv", "engine/learned-state.csv"},
	saturation.SwitchITLAggregatedSizes:      nil,
	saturation.SwitchK2FirstReadingWaits: {
		"e1-tuned/store-records.csv", "e1-tuned/variant-capacities.csv", "e1/variant-capacities.csv",
		"mu-paths/learned-state.csv", "mu-paths/store-records.csv", "mu-paths/variant-capacities.csv",
		"pd-restart/composite-results.csv", "pd-restart/composite-roles.csv", "pd-restart/composite-variants.csv",
		"pd-restart/store-records.csv", "pd-restart/variant-capacities.csv", "pd-restart/warm-pool-pressure.csv",
	},
	saturation.SwitchK2ReadsSettle:   nil,
	saturation.SwitchMuKeyBucketHeld: nil,
	saturation.SwitchMuMarginClamp:   nil,
	saturation.SwitchMuSwapFreeze:    nil,
	saturation.SwitchK2WaitsAfterRestart: {
		"mu-paths/learned-state.csv", "mu-paths/store-records.csv", "mu-paths/variant-capacities.csv",
	},
	saturation.SwitchMuRepeatIsUse:        nil,
	saturation.SwitchMuUnknownAgeSafe:     nil,
	saturation.SwitchMuLatchRestoresSafe:  nil,
	saturation.SwitchIdleBand:             nil,
	saturation.SwitchK2KeyBucketHeld:      nil,
	saturation.SwitchBatchCapDeclared:     nil,
	saturation.SwitchBatchCapLearned:      nil,
	saturation.SwitchBatchCapITLAdmission: nil,
	saturation.SwitchBatchCapDecodeView:   nil,
	saturation.SwitchBatchCapDerivedMu:    nil,
	slo.SwitchBatchCapAdmission:           nil,
	slo.SwitchBatchCapPerRank:             nil,
	saturation.SwitchITLPerToken:          nil,
	// #427's zero-replica estimate at the declared batch cap.
	saturation.SwitchBatchCapZeroReplica: nil,
	// #276's spread of output lengths in the KV per request.
	saturation.SwitchKVPerRequestOutputDispersion: nil,
	// Issue #431 routes the forget-learned-state annotation to the slo
	// analyzer's line, one switch, recorded as an event of its own
	// (slo.ForgetVariant). It moves no golden, measured: the acceptance and
	// engine recordings were taken again for it, and each carries one
	// slo.ForgetVariant (and nothing else new), with no output moved with the
	// switch on or off. Their forget lands at 09:31:30, on cycle 7, before the
	// slo line's first fit on cycle 9, so it drops a raw window and the
	// accelerator memo (Dropped 2, no intercept); two cycles later the clock
	// jumps a day and that window would have aged out anyway. No cluster
	// recording forgets. Its unit tests hold it (heldBy).
	slo.SwitchITLForgetReachesTheLine: nil,
	// #277 prices a resident request at the KV it is measured to hold,
	// between the prefix-cache hit rate's discount and none. It moves the
	// derived mu, and the decode demand the floor prices from it, in the two
	// fixtures whose derived mu the KV binds rather than the batch cap.
	//
	// Since #275 it moves the shape an aggregated line is fitted at too
	// (ITLLineShapes in the acceptance and engine exports): g is priced from
	// the KV a request holds, which #277 bounds (g 257.14 on, 281.25 off).
	saturation.SwitchKVPerRequestHitsResident: {
		"acceptance/learned-state.csv",
		"e1-tuned/results.csv", "e1-tuned/variant-capacities.csv",
		"e1/results.csv", "e1/variant-capacities.csv",
		"engine/learned-state.csv",
	},
	// #432 fits an ITL line for an aggregated (unlabeled, RoleBoth) variant,
	// as for decode, and reports the mu it derives without pricing it; its
	// line is shared only with other aggregated variants (a physics key
	// ending in |both). It moves the acceptance and engine fixtures, the two
	// whose models run aggregated variants (model-01's variant-01 and
	// variant-02, model-02's variant-03); no other fixture has one. As #432
	// measured it, all off against all on:
	//
	//   - lines.csv: model-01's variant-01 publishes a line under its |both
	//     key on its first fit (09:32:00), and variant-02, on the same engine
	//     configuration, borrows it on three cycles. No decode line moves.
	//   - learned-state.csv: that line, variant-01's intercept and its held
	//     slope join the two exports (three rows each); since #275 the line's
	//     shape (ITLLineShapes) too, which #275's switches need it on for.
	//   - variant-capacities.csv: 28 of 180 rows (acceptance) and 28 of 156
	//     (engine), all on the three aggregated variants: the ITL window, the
	//     line and a reported rate of 10.88/s, source derived-report-only.
	//     No demand, capacity, reason or borrow record moves.
	saturation.SwitchITLFitsAggregated: {
		"acceptance/learned-state.csv", "acceptance/lines.csv", "acceptance/variant-capacities.csv",
		"engine/learned-state.csv", "engine/lines.csv", "engine/variant-capacities.csv",
	},
})

// withSeams is rows with more's rows added; a name in both is a row given
// twice, and panics.
func withSeams(more, rows map[string][]string) map[string][]string {
	for name, outs := range more {
		if _, dup := rows[name]; dup {
			panic("earlierSeams: " + name + " has two rows")
		}
		rows[name] = outs
	}
	return rows
}

// heldBy is, for a switch that names no output, the unit tests that hold it:
// no committed golden moves under it, so the runs below see nothing of it,
// and a switch whose off path were dead would pass them. Each test named
// must exist (by its func declaration, in the package directory given).
// The guard checks only that; each named test must also fail with its
// switch off (each of 4a's runs the switch on and off and asserts both). A
// test that holds the same on either side is a control, not a hold, and is
// not listed here.
var heldBy = map[string][]heldTest{
	// Issue #275's lift: an aggregated figure sizes once the GPS check has
	// agreed. Both run the switch on and fail with it off: the first checks
	// the lifted rate against a run with it off, the second needs the lift
	// before the shape moves.
	saturation.SwitchITLAggregatedSizes: {
		{"../saturation", "TestAggregatedShape_ALiftedLineSizesAtTheCorrectedRate"},
		{"../saturation", "TestAggregatedShape_AnOldLineDoesNotPriceAHeavierShape"},
	},
	// Issue #438's data-parallel ranks at zero replicas.
	saturation.SwitchZeroReplicaDataParallel: {
		{"../saturation", "TestZeroReplicaDataParallel_AnEightRankDecodeIsPricedAtEveryRank"},
		{"../saturation", "TestZeroReplicaDataParallel_AWholeNodePodWithNoDeclaredCap"},
		{"../saturation", "TestZeroReplicaDataParallel_TheClampsStillBound"},
		{"../saturation", "TestZeroReplicaDataParallel_AGroupIsPricedAtItsLeadersRanks"},
		{"../saturation", "TestZeroReplicaDataParallel_TheColdStartBudgetIsEveryRanks"},
		{"../saturation", "TestZeroReplicaDataParallel_UnknownPodsNeedTheGPUsToMatch"},
	},
	// Issue #431's forget of the slo line.
	slo.SwitchITLForgetReachesTheLine: {
		{"../slo", "TestForget_TheAnnotationForgetsTheLine"},
		{"../slo", "TestForget_DropsTheAcceleratorMemo"},
		{"../slo", "TestForget_ReportsEachForgottenKey"},
	},
	saturation.SwitchITLKeepsSettleThroughMissingRow: {
		{"../saturation", "TestITLSettle_OneMissingRowKeepsTheSettle"},
	},
	saturation.SwitchITLReadsFinitePodMean: {
		{"../saturation", "TestITLSettle_APodWithAnAbsentRankIsNoReading"},
	},
	slo.SwitchITLKeepsSettleThroughMissingRow: {
		{"../slo", "TestSettle_OneMissingRowKeepsTheSettle"},
	},
	slo.SwitchITLReadsFinitePodMean: {
		{"../slo", "TestSettle_APodWithAnAbsentRankIsNoReading"},
	},
	saturation.SwitchITLRestartUnderName: {
		{"../saturation", "TestITLSettle_ANewReadyTimeUnderTheSameNameStartsTheSettle"},
	},
	saturation.SwitchITLIdleOnEvidence: {
		{"../saturation", "TestITLSettle_AScrapeThatReadNothingIsNotIdle"},
	},
	slo.SwitchITLRestartUnderName: {
		{"../slo", "TestSettle_ANewReadyTimeUnderTheSameNameStartsTheSettle"},
	},
	slo.SwitchITLIdleOnEvidence: {
		{"../slo", "TestSettle_AScrapeThatReadNothingIsNotIdle"},
	},
	saturation.SwitchBatchCapZeroReplica: {
		{"../saturation", "TestBatchCapZeroReplica_TheEstimateIsPricedAtTheDeclaredCap"},
		{"../saturation", "TestBatchCapZeroReplica_ADeclaredCapAbove256RaisesTheEstimate"},
		{"../saturation", "TestBatchCapZeroReplica_TheFlagsWinAndTheDefaultIsNamed"},
		{"../saturation", "TestBatchCapZeroReplica_TheSwitchPicksTheSource"},
		{"../saturation", "TestBatchCapZeroReplica_APrefillVariantKeepsTheFlagsOrTheDefault"},
		{"../saturation", "TestBatchCapZeroReplica_ALearnedCapIsNeverRead"},
	},
	saturation.SwitchITLPerToken: {
		{"../saturation", "TestITLPerToken_TheLineIsFittedPerAcceptedToken"},
		{"../saturation", "TestITLPerToken_AMissingCounterUnderSpeculativeFlagsWithholdsTheReading"},
		{"../saturation", "TestITLPerToken_AStepTimeLineIsNotLentToASpeculatingVariant"},
		{"../saturation", "TestITLPerToken_AStepTimeSnapshotIsNotRestoredForASpeculatingVariant"},
		{"../saturation", "TestITLPerToken_AnAcceptedCountAboveTheDraftLengthIsWithheld"},
		{"../saturation", "TestITLPerToken_AWithheldReadingIsLoggedOncePerChange"},
	},
	saturation.SwitchKVPerRequestOutputDispersion: {
		{"../saturation", "TestKVOutputDispersion_TheDerivedK2HoldsTheSpread"},
		{"../saturation", "TestKVOutputDispersion_TheDerivedMuFitsFewerSequences"},
		{"../saturation", "TestKVOutputDispersion_AZeroReplicaVariantIsPricedWithTheSpread"},
		{"../saturation", "TestKVOutputDispersion_AZeroReplicaEstimateIsRequestWeighted"},
	},
	saturation.SwitchBatchCapITLAdmission: {
		{"../saturation", "TestBatchCapReaders_TheITLWindowTakesAReplicaAtItsDeclaredCap"},
		{"../saturation", "TestBatchCapReaders_ALearnedCapIsNotAnITLCap"},
		{"../saturation", "TestBatchCapReaders_TheITLAdmissionSwitchPicksTheSource"},
		{"../saturation", "TestBatchCapReaders_ADeclaredCapAbove256"},
	},
	saturation.SwitchBatchCapDecodeView: {
		{"../saturation", "TestBatchCapReaders_APDDecodeAtItsDeclaredCapKeepsTheFullQueue"},
		{"../saturation", "TestBatchCapReaders_TheDecodeViewSwitchPicksTheSource"},
		{"../saturation", "TestBatchCapReaders_ALearnedCapIsNotADecodeViewCap"},
	},
	saturation.SwitchBatchCapDerivedMu: {
		{"../saturation", "TestBatchCapReaders_TheDerivedMuIsCappedAtTheDeclaredCap"},
		{"../saturation", "TestBatchCapReaders_TheDerivedMuSwitchPicksTheSource"},
		{"../saturation", "TestBatchCapReaders_ALearnedCapLowersTheDerivedMu"},
		{"../saturation", "TestBatchCapReaders_ADeclaredCapAbove256"},
	},
	slo.SwitchBatchCapAdmission: {
		{"../slo", "TestBatchCap_TheLineTakesAReplicaAtItsDeclaredCap"},
		{"../slo", "TestBatchCap_TheAdmissionSwitchPicksTheSource"},
	},
	slo.SwitchBatchCapPerRank: {
		{"../slo", "TestBatchCap_AReplicaIsPricedAtItsDeclaredCap"},
		{"../slo", "TestBatchCap_ThePerRankSwitchPicksTheSource"},
		{"../slo", "TestBatchCap_TheLogNamesThePricedCap"},
		{"../slo", "TestBatchCap_ADeclaredCapAbove256RaisesThePrice"},
	},
	saturation.SwitchIdleBand: {
		{"../saturation", "TestIdleBand_ARunStartsUnderTheLineAndEndsAtTheTop"},
		{"../saturation", "TestIdleBand_TheSwitchOffHoldsEveryReadingToTheLine"},
	},
	saturation.SwitchK2KeyBucketHeld: {
		{"../saturation", "TestK2KeyBucket_TheSmoothedLengthLeavesPastTheMargin"},
		{"../saturation", "TestK2KeyBucket_ASwapMovesItTheSameWayBothWays"},
		{"../saturation", "TestK2KeyBucket_OneReadingNeverMovesIt"},
		{"../saturation", "TestK2KeyBucket_APendingSwapHoldsTheBucket"},
		{"../saturation", "TestK2KeyBucket_AMovedKeyIsPricedFromTheBucketItLeft"},
		{"../saturation", "TestK2KeyBucket_TheSwitchPicksTheLength"},
	},
	saturation.SwitchMuKeyBucketHeld: {
		{"../saturation", "TestMuKeyBucket_TheSwitchPicksTheLength"},
		{"../saturation", "TestMuKeyBucket_TheKeysShareABucket"},
		{"../saturation", "TestMuKeyBucket_AMovedKeyIsCarriedLowerOnly"},
		{"../saturation", "TestMuKeyBucket_TheCarryPricesThroughAnalyze"},
	},
	saturation.SwitchMuSwapFreeze: {
		{"../saturation", "TestMuKeyBucket_APendingSwapFreezesTheWindow"},
		{"../saturation", "TestMuKeyBucket_APendingSwapLeavesPrefillWriting"},
	},
	saturation.SwitchMuMarginClamp: {
		{"../saturation", "TestMuKeyBucket_AMarginMoveNeverRaisesTheWindow"},
		{"../saturation", "TestMuKeyBucket_TheClampWritesNothingToAnEmptyWindow"},
	},
	saturation.SwitchK2ReadsSettle: {
		{"../saturation", "TestSettleView_TheTrackerSwitchMovesTheAbsenceRule"},
		{"../saturation", "TestSettleView_TheTrackerSwitchKeepsTheSettleThroughAZeroReadyTime"},
	},
	saturation.SwitchMuRepeatIsUse: {
		{"../saturation", "TestMuRepeat_IsUseNotAWrite"},
		{"../saturation", "TestMuRepeat_AFrozenSeriesLetsTheEpisodeEnd"},
		{"../saturation", "TestMuRepeat_OverDerivedDoesNotKeepTheMeasurementFresh"},
	},
	saturation.SwitchMuUnknownAgeSafe: {
		{"../saturation", "TestMuUnknownAge_UnderDerivedKeepsTheLowHold"},
		{"../saturation", "TestMuUnknownAge_NoTimeAtAllIsHeld"},
	},
	saturation.SwitchBatchCapDeclared: {
		{"../saturation", "TestBatchCapSwitch_DeclaredOffPricesTheDefault"},
	},
	saturation.SwitchBatchCapLearned: {
		{"../saturation", "TestBatchCapSwitch_LearnedOffLearnsNothing"},
		{"../saturation", "TestBatchCap_SwitchesOffReproduceTheIssue"},
	},
	saturation.SwitchMuLatchRestoresSafe: {
		{"../saturation", "TestMuLatch_ARestoredMeasurementOverDerivedComesBackLatched"},
		{"../saturation", "TestMuLatch_TheRestoredLatchIsKnownAfterItsFirstChoice"},
		{"../saturation", "TestMuLatch_AWindowRestoredAfterARefusedReadingComesBackLatched"},
		{"../saturation", "TestMuLatch_AWindowBroughtBackFromTheStashComesBackLatched"},
		{"../saturation", "TestMuLatch_AWindowRestoredAsAPriorAfterARefusedReadingComesBackLatched"},
	},
}

// Every switch the saturation and slo hooks accept has a row: this PR's, or
// an earlier PR's seam still in the code. A seam with no row is a switch no
// run drives.
func TestGoldens_EverySwitchHasARow(t *testing.T) {
	names := append(append(saturation.ReplaySwitchNames(), slo.ReplaySwitchNames()...), throughput.ReplaySwitchNames()...)
	for _, name := range names {
		_, mine := replaySwitches[name]
		_, earlier := earlierSeams[name]
		require.True(t, mine || earlier, "%s is accepted by SetSwitchesForReplay and is in neither replaySwitches nor earlierSeams", name)
	}
	for name := range mergedRows() {
		require.Contains(t, names, name, "%s's seam is gone: delete its row", name)
	}
}

// heldTest is a test function, and the package directory that declares it.
type heldTest struct{ dir, name string }

// A switch that names no golden output names the unit tests that hold it,
// and they exist.
func TestGoldens_ASwitchNoGoldenMovesIsHeldByItsTests(t *testing.T) {
	for name, outs := range mergedRows() {
		if len(outs) > 0 {
			continue
		}
		tests := heldBy[name]
		require.NotEmpty(t, tests, "%s moves no golden: name the unit tests that hold it in heldBy", name)
		for _, h := range tests {
			files, err := filepath.Glob(filepath.Join(h.dir, "*_test.go"))
			require.NoError(t, err)
			found := false
			for _, f := range files {
				b, err := os.ReadFile(f)
				require.NoError(t, err)
				if strings.Contains(string(b), "func "+h.name+"(t *testing.T)") {
					found = true
					break
				}
			}
			require.True(t, found, "%s is held by %s, which %s does not declare", name, h.name, h.dir)
		}
	}
}

// mergedRows is replaySwitches and earlierSeams together.
func mergedRows() map[string][]string {
	out := maps.Clone(replaySwitches)
	maps.Copy(out, earlierSeams)
	return out
}

// switchBase is the sha256 of each output a switch of this PR names, as the
// base commit's goldens hold it (f7b4f7e47, learner PR 4b-0).
var switchBase = map[string]string{
	"acceptance/results.csv":            "948487bf37d359a8bd3d7967fa4394a2558409844030ab153ea661132e535915",
	"acceptance/variant-capacities.csv": "b4932d51ae2e081430e2161eb6f3c3bdd9a839f84ad77f035fc4ad47ce325346",
	"engine/composite-holds.csv":        "c26c788b73e0a292e70789ed60520fd9c83254c0dc3c294f29283c297d518dc7",
	"engine/composite-release.csv":      "4fc0b13906d8d1fdb039493b3cc6182cb7cce1b3af61bdfb2975d8b23e9a6fe5",
	"engine/composite-variants.csv":     "43bb85189c577d3c09508de664d37c55c5302fa22cb41f03f1d581722b3f078f",
	"engine/results.csv":                "61718c5eee24d49ae90051c0c42ba862587dc9ee2436e04f467593bc1d1455f4",
	"engine/variant-capacities.csv":     "c97cb118dda1a6abb54296e45650437cb7f8d86e78970645e5116362547a0f05",
	"mu-paths/results.csv":              "f87ec1d3d61babdba014fc786e2a4e52e2b947bcdce15ab6f20cdcfa619c450b",
	"mu-paths/variant-capacities.csv":   "62b4c7881d656e45126691ef86e4af91e35925a7e361c69b02076f008a48ec94",
}

// The slow tests of this package run in parallel with each other (each
// marked "reads the goldens, writes only to its own copies"): each replays
// with analyzers of its own and writes only under t.TempDir. The one test
// that swaps the registered engine replayer
// (TestEngineGoldens_NoEngineRegisteredIsAProblem) is not
// parallel, so it has restored it before any of them starts. Run in turn,
// the package took about 330 s with coverage, past the CI job's 10 min on a
// runner half as fast; this test alone took 139 s of it.
func TestGoldens_TheSwitchRuns(t *testing.T) {
	t.Parallel() // reads the goldens, writes nowhere
	m, err := ReadManifest(goldenDir)
	require.NoError(t, err)
	named := map[string]bool{}
	for _, outs := range replaySwitches {
		for _, o := range outs {
			named[o] = true
			require.Contains(t, switchBase, o, "an output a switch names needs its base digest")
		}
	}
	set := func(on ...string) map[string]bool {
		sw := map[string]bool{}
		for name := range replaySwitches {
			sw[name] = slices.Contains(on, name)
		}
		return sw
	}
	allOn := func() map[string]bool { return set(slices.Collect(maps.Keys(replaySwitches))...) }

	// Every run the rule asks for, replayed up front: all off, each of this
	// PR's switches alone, all on, and each earlier seam off alone from all
	// on. Each run is independent of the others (its own analyzers, store
	// and settle tracker), so the replays run in parallel, one per fixture
	// per run, bounded by GOMAXPROCS; the comparisons below then read them
	// in the order the rule states.
	mine := slices.Sorted(maps.Keys(replaySwitches))
	earlier := slices.Sorted(maps.Keys(earlierSeams))
	sets := make([]map[string]bool, 0, len(mine)+len(earlier)+2)
	sets = append(sets, set())
	for _, name := range mine {
		sets = append(sets, set(name))
	}
	sets = append(sets, allOn())
	for _, name := range earlier {
		sw := allOn()
		sw[name] = false
		sets = append(sets, sw)
	}
	runs := replaySets(t, m, sets)
	off, alone, on, earlierOff := runs[0], runs[1:1+len(mine)], runs[1+len(mine)], runs[2+len(mine):]

	// moved is the outputs that differ between two runs, sorted, each
	// logged with the replay's one-line summary of how.
	moved := func(name string, from, to map[string][]byte) []string {
		var out []string
		for f, b := range to {
			d, err := compareOutput(f, from[f], b)
			require.NoError(t, err)
			if d != nil {
				out = append(out, f)
				t.Logf("%s: %s", name, d)
			}
		}
		slices.Sort(out)
		return out
	}

	for f, b := range off {
		if named[f] {
			sum := sha256.Sum256(b)
			require.Equal(t, switchBase[f], hex.EncodeToString(sum[:]),
				"all off: %s is not the base commit's golden", f)
			continue
		}
		committed, err := os.ReadFile(filepath.Join(goldenDir, filepath.FromSlash(f)))
		require.NoError(t, err)
		d, err := compareOutput(f, committed, b)
		require.NoError(t, err)
		require.Nil(t, d, "all off: %s moved, and no switch names it", f)
	}
	for i, name := range mine {
		got := moved(name, off, alone[i])
		// Exactly what it names: a named output that does not move is an off
		// path that went dead.
		require.ElementsMatch(t, replaySwitches[name], got, "%s alone must move exactly the outputs it names", name)
		t.Logf("%s alone moves %d outputs: %v", name, len(got), got)
	}
	for f, b := range on {
		committed, err := os.ReadFile(filepath.Join(goldenDir, filepath.FromSlash(f)))
		require.NoError(t, err)
		d, err := compareOutput(f, committed, b)
		require.NoError(t, err)
		require.Nil(t, d, "all on: %s is not the committed golden", f)
	}
	for i, name := range earlier {
		got := moved(name, on, earlierOff[i])
		require.ElementsMatch(t, earlierSeams[name], got, "%s off alone must move exactly the outputs its PR named", name)
		t.Logf("%s off alone moves %d outputs: %v", name, len(got), got)
	}
}

// replaySets replays every fixture of m once per switch set, and returns,
// per set, its outputs by fixture/file. Each fixture's recording is read
// once; the replays run on GOMAXPROCS workers, since each builds its own
// analyzers and touches no state another shares.
func replaySets(t *testing.T, m *Manifest, sets []map[string]bool) []map[string][]byte {
	t.Helper()
	recs := make([][]byte, len(m.Goldens))
	for i, g := range m.Goldens {
		gz, err := os.ReadFile(filepath.Join(goldenDir, filepath.FromSlash(g.Recording)))
		require.NoError(t, err)
		recs[i], err = gunzip(gz)
		require.NoError(t, err)
	}
	type job struct{ set, golden int }
	type result struct {
		job
		outs map[string][]byte
		err  error
	}
	jobs := make(chan job)
	results := make(chan result)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				outs, err := replayGolden(recs[j.golden], m.Goldens[j.golden].Name, sets[j.set])
				results <- result{job: j, outs: outs, err: err}
			}
		}()
	}
	go func() {
		for s := range sets {
			for g := range m.Goldens {
				jobs <- job{set: s, golden: g}
			}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	out := make([]map[string][]byte, len(sets))
	for i := range out {
		out[i] = map[string][]byte{}
	}
	var errs []error
	for r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		for _, f := range m.Goldens[r.golden].Outputs {
			out[r.set][f] = r.outs[path.Base(f)]
		}
	}
	require.NoError(t, errors.Join(errs...))
	return out
}

// replayGolden replays one fixture's recording with the switches set, and
// returns its outputs by file.
func replayGolden(rec []byte, name string, sw map[string]bool) (map[string][]byte, error) {
	byPkg := map[string]map[string]bool{}
	for n, on := range sw {
		pkg := switchPackage(n)
		if byPkg[pkg] == nil {
			byPkg[pkg] = map[string]bool{}
		}
		byPkg[pkg][n] = on
	}
	// A run without the throughput analyzer has none of its switches: off
	// and on are the same run there.
	if _, ok := byPkg[PackageThroughput]; ok {
		read, err := learnrec.Read(rec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		var start learnrec.Start
		if err := read.Events[0].DecodeData(&start); err != nil {
			return nil, fmt.Errorf("%s: start: %w", name, err)
		}
		if !start.Throughput {
			delete(byPkg, PackageThroughput)
		}
	}
	rep, err := Replay(context.Background(), rec, Options{Mode: Normal, Switches: byPkg})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if f := rep.Failed(); len(f) > 0 {
		return nil, fmt.Errorf("%s: the replay failed: %v", name, f)
	}
	return Outputs(rep.Recording)
}

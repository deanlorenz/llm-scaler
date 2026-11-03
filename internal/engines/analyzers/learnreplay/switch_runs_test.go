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
// #432 fits an ITL line for an aggregated (unlabeled, RoleBoth) variant, as
// for decode, and prices its replicas from it; its line is shared only with
// other aggregated variants (a physics key ending in |both). It moves the
// acceptance and engine fixtures, the two whose models run aggregated
// variants (model-01's variant-01 and variant-02, model-02's variant-03); no
// other fixture has one. Measured, all off against all on:
//
//   - lines.csv: model-01's variant-01 publishes a line under its |both key
//     on its first fit (09:32:00), and variant-02, on the same engine
//     configuration, borrows it on three cycles. No decode line moves.
//   - learned-state.csv: that line, variant-01's intercept and its held
//     slope join the two exports (three rows each).
//   - variant-capacities.csv: 28 of 180 rows (acceptance) and 28 of 156
//     (engine), all on the three aggregated variants. ServiceRate reads the
//     ITL window on all 28 and its sample count on 16; on six rows of
//     variant-01 and variant-02 it has a line (A 0.020, B 0.006,
//     ITL 0.0196 s at the pricing point) and a derived rate of 10.88/s. On
//     variant-02's three borrowing cycles the record names the borrow
//     (Borrowed: itl-line from variant-01). No demand, capacity or reason
//     moves: TotalDemand, PerReplicaCapacity and Reason are unchanged on
//     every row.
//   - engine/composite-variants.csv: variant-02's three borrow rows, as
//     above.
//
// Earlier PRs' rows: #277's switch (kv-per-request-hits-resident) went to
// earlierSeams; off alone from all on it still moves only e1 and e1-tuned.
//
// The forget-learned-state annotation, the GPS check and the learner report
// role move no golden; their unit tests hold them in the saturation package.
var replaySwitches = map[string][]string{
	saturation.SwitchITLFitsAggregated: {
		"acceptance/learned-state.csv", "acceptance/lines.csv", "acceptance/variant-capacities.csv",
		"engine/composite-variants.csv", "engine/learned-state.csv", "engine/lines.csv",
		"engine/variant-capacities.csv",
	},
}

// kvHitsResidentSeam is #277's row, an earlier seam now (merged into
// earlierSeams).
//
// #277 prices a resident request, where the running requests are read, at
// the KV they are measured to hold, between the hit rate's discount and none;
// with no reading, at the discount as before. It moves the derived mu, and
// the decode demand the floor prices from it, in the two fixtures whose
// derived mu the KV binds rather than the batch cap: e1 and e1-tuned, both
// cluster recordings with a hit rate of about 1.5%. Every rate moved falls
// (ServiceRate.Rate on 153 and 158 rows, by 0.02% to 0.54%), as the change
// says it must: a reading can take the discount back, never add to it. Of
// the derived-mu lines with the switch on, e1 priced 19 inside the bounds,
// 275 at the upper bound, 180 at the lower (as before), 457 with no reading
// (as before) and 208 for a replica with no hit (as before); e1-tuned 19,
// 288, 150, 466 and 208. The band's count gate takes no reading that moved
// a golden: the outputs are the ones the fill gate alone gave.
//
// The decode demand rises on most cycles it moves (70 of 107 in e1, 57 of
// 93 in e1-tuned, by up to 0.49%) and falls on the rest, by at most 0.08%.
// Traced cycle by cycle with the switch off and on, every fall (37 and 36)
// is the floor's trust blend, and none the backlog cap, which binds on none
// of them: the floor moves want towards the resident demand by trust, and
// trust grows with the arrival demand lambda x PerReplica / mu, which a
// lower mu raises (throughput_floor.go,
// want -= trust x (want - max(resident, tokens))).
// In e1, ns-01's decode on the second floor line: mu 6.3771 -> 6.3678 at an
// arrival of 12/s, flooredTo 683929.0 -> 683923.9, between the resident
// 683453 and the 684954 before the floor, with one request queued and none
// capped.
//
// The acceptance, engine and mu-paths fixtures carry a hit rate of 0.1 and
// read at the upper bound on every priced line, but move nothing: every
// derived mu they price is capped at their batch cap of 64 before the KV
// binds.
//
// Earlier PRs' switches (learner PRs 2, 3 and 4a, #396's bands, #399's
// batch cap, #415's mu key, #392's first k2 reading, #420's batch-cap
// readers, #274's ITL per accepted token, #276's output spread, #427's
// zero-replica batch cap, #431's forget of the slo line, #277's resident
// KV) are not in replaySwitches: "all off" is this PR's switches, and an
// earlier PR's stay at their constructor default, on, as the base goldens
// were written (analyzer-learner.md, 2.5).
//
// #277's reading applies to an aggregated variant's derived mu too, with no
// change of its own: fleetResidentKVPerRequest reads every Ready generating
// replica (generatesOutput, every role but prefill), and the derived mu of
// every role priced from a line takes it.
var kvHitsResidentSeam = map[string][]string{
	saturation.SwitchKVPerRequestHitsResident: {
		"e1-tuned/results.csv", "e1-tuned/variant-capacities.csv",
		"e1/results.csv", "e1/variant-capacities.csv",
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
	// cycle, variant-02 borrows none, and their service rates and the
	// learned-state export lose them, as the slo analyzer's line does under
	// slo.SwitchITLReadsSettle.
	saturation.SwitchITLReadsSettle: {
		"acceptance/learned-state.csv", "acceptance/lines.csv", "acceptance/variant-capacities.csv",
		"engine/composite-variants.csv", "engine/learned-state.csv", "engine/lines.csv",
		"engine/variant-capacities.csv",
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
	return PackageSaturation
}

// earlierSeams is each switch an earlier learner PR added whose seam the
// code still has, and the golden outputs that PR named it to move. Each
// stays on, at its constructor default, in this PR's runs; off alone, from
// all on, it may move only what it named. A seam's row goes with the seam,
// in the PR, or cleanup PR, that deletes it with zero golden diff. Learner
// PR 4a's rows are learner4aSeams, merged in.
var earlierSeams = withSeams(kvHitsResidentSeam, withSeams(learner4aSeams, map[string][]string{
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
}))

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
	names := append(saturation.ReplaySwitchNames(), slo.ReplaySwitchNames()...)
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
// base commit's goldens hold it (64f4747b0, main after #446; the seven are
// unchanged since 0da4f9ac2).
var switchBase = map[string]string{
	"acceptance/learned-state.csv":      "a6eececb06d24586d9b1325423e2f362c75038b4d3aeb20e50bf9409d7ce82cf",
	"acceptance/lines.csv":              "085ad326a18aa646144b6fd11718ae04ceda58ab77c03b7fca62a5f01ad155a6",
	"acceptance/variant-capacities.csv": "30f5279ca4c19b6e50fb1567b8e018172423e5756782535a9a94411591ffb63a",
	"engine/composite-variants.csv":     "43bb85189c577d3c09508de664d37c55c5302fa22cb41f03f1d581722b3f078f",
	"engine/learned-state.csv":          "a6eececb06d24586d9b1325423e2f362c75038b4d3aeb20e50bf9409d7ce82cf",
	"engine/lines.csv":                  "085ad326a18aa646144b6fd11718ae04ceda58ab77c03b7fca62a5f01ad155a6",
	"engine/variant-capacities.csv":     "4765d306858eb2dc564bc93cf352c5947ec85f3ca9d503d48629a98e11cab755",
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
	rep, err := Replay(context.Background(), rec, Options{Mode: Normal, Switches: byPkg})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if f := rep.Failed(); len(f) > 0 {
		return nil, fmt.Errorf("%s: the replay failed: %v", name, f)
	}
	return Outputs(rep.Recording)
}

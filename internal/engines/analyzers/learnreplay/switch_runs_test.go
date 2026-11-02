package learnreplay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
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
// Learner PR 4a moves the ITL line, and the slo analyzer's line, onto the
// settle tracker, four switches each.
//
//   - The wait after a controller restart moves the goldens whose load
//     reaches the line within its settle of the controller start: the
//     in-process runs, and the ols-sweep-slo-restart recording, whose
//     controller came back under load. Each line refits steadyFor later.
//   - The move itself moves the in-process runs on the cycles after their
//     clock jumps a day: 25 hours with no cycle at all is, to the tracker,
//     an absence past its tolerance (settle.Rejoined), and the replicas
//     settle again; the line's own trace counted an absence only by cycles
//     that went without a replica, and saw none. In the acceptance and
//     engine runs that is one full fit the line no longer publishes (10:32
//     the next day), and in the slo line four cycles priced from a pinned
//     fit, not a full one. The cluster recordings are held.
//   - The absence rule and the Finite-only reading move nothing: no
//     recording misses a replica's row for a cycle, and every recorded Pod
//     is one rank. Neither do the tracker's two rules with switches of
//     their own, a restart under the same name and idle on evidence: no
//     recording has a Pod's Ready time change with no row that is not Ready
//     between, or a row that read nothing. Their unit tests hold all four
//     (heldBy).
//
// The acceptance and engine recordings were taken again for this PR, with
// the generator's Ready time fixed for the day: the earlier ones gave each
// replica a new Ready time on every cycle, an engine restart each time to
// the tracker, and no line of theirs ever settled. Their base digests below
// are the all-off replays of the new recordings.
//
// Earlier PRs' switches (learner PRs 2 and 3, #396's bands, #399's batch
// cap, #415's mu key, #392's first k2 reading, #420's batch-cap readers,
// #274's ITL per accepted token, #276's output spread) are not here: "all
// off" is this PR's switches, and an earlier PR's stay at their constructor
// default, on, as the base goldens were written (analyzer-learner.md, 2.5).
var replaySwitches = map[string][]string{
	saturation.SwitchITLReadsSettle: {"acceptance/lines.csv", "engine/lines.csv"},
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
// in the PR, or cleanup PR, that deletes it with zero golden diff.
var earlierSeams = map[string][]string{
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
	// #276's spread of output lengths in the KV per request.
	saturation.SwitchKVPerRequestOutputDispersion: nil,
}

// heldBy is, for a switch that names no output, the unit tests that hold it:
// no committed golden moves under it, so the runs below see nothing of it,
// and a switch whose off path were dead would pass them. Each test named
// must exist (by its func declaration, in the package directory given).
var heldBy = map[string][]heldTest{
	saturation.SwitchITLKeepsSettleThroughMissingRow: {
		{"../saturation", "TestITLSettle_OneMissingRowKeepsTheSettle"},
		{"../saturation", "TestITLSettle_AnAbsencePastTheToleranceStartsTheSettleAgain"},
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

// switchBase is the sha256 of each output a switch names, as the base
// commit's goldens hold it (939af5e36, main after #437), and for the
// acceptance and engine goldens, taken again here, their all-off replays.
var switchBase = map[string]string{
	"acceptance/learned-state.csv":                 "63cbd711d50f36db5dbdfc06787571bf1c04a9e4e8fdb83c5d5b3c86438d0aa0",
	"acceptance/lines.csv":                         "09ed3da7b44df960f33e4cb6c3cdbb6c6fc4d6ad4d8f3c970999582e124b239c",
	"acceptance/slo-published.csv":                 "2276ef61bd4b7203d9d06c26192046b2429ad12b5e5ef668c776a46a15ba36f8",
	"acceptance/variant-capacities.csv":            "a7ad07abe1eb85bcaf3786d10d9822785a2ec073cd0dee0c02f78eea2dee9c73",
	"engine/composite-variants.csv":                "f0d66647fc3b2d02a4c2f9d79616ebfb7123a4a31746644079f720522d68d64d",
	"engine/learned-state.csv":                     "63cbd711d50f36db5dbdfc06787571bf1c04a9e4e8fdb83c5d5b3c86438d0aa0",
	"engine/lines.csv":                             "09ed3da7b44df960f33e4cb6c3cdbb6c6fc4d6ad4d8f3c970999582e124b239c",
	"engine/slo-published.csv":                     "ed09c2bd28e3055a9b8744d87c588c2231e64906516f28d8b93b7e908e04e912",
	"engine/variant-capacities.csv":                "052ea4872813f6401a7536a2a359e2cbcbb4dcedfc12a315dba9f9c93411fd79",
	"mu-paths/learned-state.csv":                   "4a7667542a86f5fb12a683a01eea3426c51b668ddd0e7aab18ed133608a1a8e9",
	"mu-paths/lines.csv":                           "b293056a8e459d7d6fd05755aa8fa5e7920f4f68355a29a381fb1b789121f396",
	"mu-paths/slo-published.csv":                   "761496fb84fd97d26bb873e52051eded6bd7ca82d36dfe59bca597f905aad6f1",
	"mu-paths/variant-capacities.csv":              "d8fdaadd8f890d8b3bba0e925bbd9ccca03b650911222f3bd9b9f79f7531cdba",
	"ols-sweep-slo-restart/composite-results.csv":  "661c30baab0ade02fcd3cc9de75c8c4916ebdf52f4382a67f285cc8142c9ba8c",
	"ols-sweep-slo-restart/lines.csv":              "322e9075099cd49f4480ac06e85a9febbef96ba71af0f056448e1d366c07cb1f",
	"ols-sweep-slo-restart/results.csv":            "38f2ca37c1246dff5cb3d713cbf73e10e189345e24bbe3c757a90d01137755b4",
	"ols-sweep-slo-restart/slo-published.csv":      "7629254bdea8f46e51e241e9d6bf7644914dbbb5c69fc310f424643cc41cba87",
	"ols-sweep-slo-restart/variant-capacities.csv": "9aa0b55d9a5742e32dc1ede8df641bbc1374429cb6c25680e666d465f257fb08",
}

func TestGoldens_TheSwitchRuns(t *testing.T) {
	m, err := ReadManifest(goldenDir)
	require.NoError(t, err)
	named := map[string]bool{}
	for _, outs := range replaySwitches {
		for _, o := range outs {
			named[o] = true
			require.Contains(t, switchBase, o, "an output a switch names needs its base digest")
		}
	}
	// run replays every fixture with the switches set, and returns its
	// outputs by fixture/file.
	run := func(sw map[string]bool) map[string][]byte {
		out := map[string][]byte{}
		for _, g := range m.Goldens {
			gz, err := os.ReadFile(filepath.Join(goldenDir, filepath.FromSlash(g.Recording)))
			require.NoError(t, err)
			rec, err := gunzip(gz)
			require.NoError(t, err)
			byPkg := map[string]map[string]bool{}
			for name, on := range sw {
				pkg := switchPackage(name)
				if byPkg[pkg] == nil {
					byPkg[pkg] = map[string]bool{}
				}
				byPkg[pkg][name] = on
			}
			rep, err := Replay(context.Background(), rec, Options{Mode: Normal, Switches: byPkg})
			require.NoError(t, err)
			require.Empty(t, rep.Failed(), "%s: the replay failed", g.Name)
			outs, err := Outputs(rep.Recording)
			require.NoError(t, err)
			for _, f := range g.Outputs {
				out[f] = outs[path.Base(f)]
			}
		}
		return out
	}
	set := func(on ...string) map[string]bool {
		sw := map[string]bool{}
		for name := range replaySwitches {
			sw[name] = slices.Contains(on, name)
		}
		return sw
	}
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

	off := run(set())
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
	for _, name := range slices.Sorted(maps.Keys(replaySwitches)) {
		got := moved(name, off, run(set(name)))
		// Exactly what it names: a named output that does not move is an off
		// path that went dead.
		require.ElementsMatch(t, replaySwitches[name], got, "%s alone must move exactly the outputs it names", name)
		t.Logf("%s alone moves %d outputs: %v", name, len(got), got)
	}
	on := run(set(slices.Collect(maps.Keys(replaySwitches))...))
	for f, b := range on {
		committed, err := os.ReadFile(filepath.Join(goldenDir, filepath.FromSlash(f)))
		require.NoError(t, err)
		d, err := compareOutput(f, committed, b)
		require.NoError(t, err)
		require.Nil(t, d, "all on: %s is not the committed golden", f)
	}
	for _, name := range slices.Sorted(maps.Keys(earlierSeams)) {
		sw := set(slices.Collect(maps.Keys(replaySwitches))...)
		sw[name] = false
		got := moved(name, on, run(sw))
		require.ElementsMatch(t, earlierSeams[name], got, "%s off alone must move exactly the outputs its PR named", name)
		t.Logf("%s off alone moves %d outputs: %v", name, len(got), got)
	}
}

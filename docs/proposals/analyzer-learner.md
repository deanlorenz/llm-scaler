# Shared rules for the analyzers' learners

- **Review:** Revision 6 was reviewed in five rounds by four reviewers; all
  four: good enough (round 5). Revision 7 is under review.
- **Status:** revision 7. PR 1 (the settle tracker) is implemented on
  `feat/learner-settle-tracker`; the later PRs are proposed. Revision 6 merged as
  #286. Revision 7 re-checks it against main 3515c6ccd, after #282, #283,
  #288, #293, #294, #298, #302, #303, #306, #329, #331 and #361 merged, and
  describes main as it is now. Line numbers are main's at that commit.
  [Shipped behaviour each PR must keep](#shipped-behaviour-each-pr-must-keep)
  lists what those PRs fixed that a later step could undo.
- **Since revision 7:** #345 step 1 landed: one analyzer registry, and one
  `AnalyzerInput` per model built before any `Analyze` (`buildAnalyzerInput`).
  It runs after `observeLoadBalance`, as the scale-target load did. PR 1's
  settle tracker must observe before the balance tracker reads (4.2), so it
  goes at the top of `runAnalyzers`, ahead of both (decided on #345; the PR 1
  row says so). The engine now reaches
  saturation's capabilities through typed fields resolved at registration
  (`saturationCapabilities`), not by assertion at each call; a wrapper around
  the analyzer would still hide them (2.4). `runV2AnalysisOnly` is now
  `buildAnalyzerInput` followed by `runSaturation`.
- **PR 1's decisions.** The brownout trade (4.3): a run of unknown rows keeps
  the settle, and the PR does not end it after 2 min; an idle run that a
  brownout interrupts for more than the tolerance is no return of load
  (`TestABrownoutKeepsTheSettle`). A brownout that drops the rows altogether
  (a failed collection, or one that returns no series) keeps it too: the
  engine holds the model's sightings on such a cycle
  (`TestSettle_AFailedCollectionKeepsTheSighting`,
  `TestSettle_OptimizeV2HoldsOnEveryPathWithNoRows`), for up to 5 min
  without an observation (or the tolerance, if longer), after which the
  model is forgotten. A hold leaves idle runs to the tolerance, as missing
  rows do (`TestHoldLeavesIdleRunsToTheTolerance`). The absence tolerance
  (4.2) is 1 min or 2.5 optimize intervals, whichever is longer, so a long
  interval does not make every cycle an absence
  (`TestTrackerEndsTheGraceAtLongIntervals`). The re-lend (4.2): the warm pool's lend
  time rides on each bridge row (`ReplicaMetrics.LentSince`), and a new one
  under the same key is a lend event (`TestTrackerRestartsTheGraceOfAPodLentAgain`).
  Checked in the code, not on a run: a lend does not touch the Pod's
  readiness, so ReadySince does not change on a re-lend, and a re-lend to
  the same variant keeps the key.
- **PR 2's decisions.** Where an LWS group's Ready time comes from (4.3):
  the collector builds one row per group, the leader's; a worker's instance
  is dropped before its row is built (`isLWSWorker`), and the row carries
  the latest Ready time over the leader and its live, Ready workers,
  matched by the LWS set-name and group-index labels (`groupReadyTimes`,
  `replicaReadySince`; `TestLWSGroupReadyTimeIsTheLatestOfItsPods`). Under
  LWS's default `restartPolicy`, `RecreateGroupOnPodRestart`, any worker's
  restart deletes the leader, so the recreated leader has a new Ready time
  and the group waits again. Under `restartPolicy: None` a worker-only
  restart leaves the leader Ready, and the restarted worker's Ready time
  makes the group wait. A worker missing or not Ready adds nothing, so it
  never makes the group's time zero, and the group waits from the worker's
  own once it is Ready. The time is not monotonic: a worker that drops while
  it holds the latest time moves the group back to the next latest Pod's,
  which the tracker reads as a restart, so the group waits from the drop and
  again from the return (cautious, accepted). A not-Ready leader still makes
  the group's Ready time zero. Checked in the code (the collector and lws
  v0.8.0's pod controller), not on a run. k2's joined
  test reads the tracker, keeping #288's rule, behind two replay switches:
  `k2-reads-the-settle-tracker` (a replica unseen past the tracker's
  absence tolerance waits again, where the memo held it 2 min; and a Ready
  row whose Ready time reads zero keeps the settle, where the memo waited
  twice; no committed golden moves with it alone) and `k2-waits-after-controller-restart`
  (every replica waits once after a restart; moves mu-paths only). An
  analyzer given no view, as unit tests build one, observes a tracker of
  its own and takes its first cycle's replicas as already watched; a
  controller restart is the engine's tracker's first cycle. `learn` holds
  `Report`, `Forgotten` and the list `Learners` (`k2`); k2's
  `learner-write` band is 2 % of the last logged value, not measured on a
  run. The counter `k2-unsettled-window` is required since the
  flow-control-off recording landed as the `pd-restart` golden.
  Forget through `learn.Learners` drops the same entries, counted the same,
  so it is behaviour-neutral and has no switch; the clause "when no key
  remains, the variant's learner series are deleted" has nothing to apply
  to in PR 2, which adds no learner series, and waits for the first PR that
  publishes one. The switch runs are a committed test
  (`TestGoldens_TheSwitchRuns`).
- **PR 3's decisions.** Mu (rows 5 and 6) has no joined test of its own: a
  sample is written only beside a k2 P1-obs write, so it inherits k2's test
  on the tracker, and with it the wait after a controller restart (PR 2's
  `k2-waits-after-controller-restart`; no switch of mu's). Each admitted
  reading carries k2's stamp (how long the replica had been settled, and
  which settle test passed it), and mu's `learner-write` shows it
  (`TestMuLearner_AMeasuredMuWaitsOutThePostRestartSettleAndLogsItsFirstSample`).
  The fold across pods stays the mean. Three named changes, each behind a
  replay switch, none of which moves a committed golden, so each names the
  unit tests that hold it (`heldBy`). `mu-exact-repeat-is-use`: a repeat
  past the spacing touches the use clock only, so it does not keep the
  episode (write clock) alive, and is judged before the episode, so a frozen
  series past `EpisodeGap` starts no new window with its frozen value. Its
  effect on freshness (agreement clock) is by side: over the derived figure
  it does not refresh it, so the measurement yields to the lower figure;
  under it, or with no derived figure, it still counts as agreement, since a
  real low rate that repeats would otherwise give way to the higher derived
  figure after the 30 min hold. The prefill bound (row 7) keeps the old
  rule, a write. A window installed by a restore, or brought back from the
  stash, marks any choice memo made before it unknown, so the latch rule
  below sees it. `mu-unknown-age-resolves-safe`: over the
  derived figure an unknown age is still stale; under it the low hold counts
  from the window's own last write, never earlier than its newest sample, so
  it holds at least as long as a known sample time would, and with no write
  time it holds. `mu-latch-restores-safe-side`: the latch is restored, not
  persisted (the snapshot is PR 5's); a window on record with no choice memo
  (restored, or brought back) has an unknown latch, which resolves latched
  over the derived figure and unlatched under it, and its first choice is
  evidence. Before the fold was touched, two prefill pods in one spacing pin
  row 7 (`prefill_bound_pin_test.go`): since #395 the bound folds by pod
  name, so the sample is the pods' mean and the bound the max across
  minutes. Mu's `learner-write` band is 5 %, over the 4 % noise that flipped
  the choice, not measured on a run; a source flip (measured, derived,
  served-siblings, neighbour, borrowed-line, lower-bound) always logs, with
  `previousSource`. `throughput-demand-floor` names `muKey` and `muSource`
  of the role's variant nearest the term's mu, and `startEstimateSeconds`
  with `startSource` `measured`, or `none` where no start is measured: the
  floor is given no seed today, so `seed` waits for PR 5 to wire one. The
  closed-loop probe and its fleet simulator land in
  `D/closed_loop_probe_test.go`; its known-failing case is one slow replica
  at 0.55 of its siblings, inside the 2x served bound with no full sibling,
  which seals the fleet over-ordered for good (28 replicas against a need of
  18), and the test asserts the probe sees it. Mu is not on
  `learn.Learners` yet: Forget waits for PRs 4a and 4b, with the SLO and
  throughput lines. The mu counters are re-measured on the flow-control-off
  P/D recording when it lands.
- **PR 4a's decisions.** The ITL line (rows 1 and 13: saturation's and the
  slo analyzer's) reads a replica's settle from the tracker, behind six
  switches each (`itl-…`, and `slo-itl-…` in the slo analyzer):
  `reads-the-settle-tracker` (the move), `restart-under-its-name-settles`
  (a new Ready time under the same Pod name is a restart, `settle.Restarted`,
  with or without a not-Ready row between), `idle-needs-evidence` (a scrape
  that read nothing is not idle), `waits-after-controller-restart`,
  `keeps-the-settle-through-a-missing-row` and `reads-the-finite-pod-mean`
  (a Pod with an Absent rank is no reading). A settle starts at the latest
  of the tracker's `Since`, the last return of load after `idleRows` idle
  rows (`ReturnedAfter`), and the first Ready row after one that was not
  (`ReadyAgain`, now in `settlerow` and shared with k2). The key stamps the
  cycle it first read a start, so a joiner waits from the analyzer's clock
  as before, and takes a start only when it is later than the one it holds:
  the tracker keeps rows for 5 min and its last eight returns, and a start
  that falls back when one ages out is no event (review round 1 found the
  line settling again 5 min after a readiness blip). A start first read on
  the key's own first cycle (a controller restart, or a key made anew by a
  forget or new engine arguments) is a settle only with the wait, and then
  runs from the tracker's start, so a key made anew does not make
  long-settled replicas wait; on the key's own trace (the move off) the
  wait reads as "every replica on the key's first cycle waits", which only
  a replay runs. An analyzer with no engine tracker never waits. The
  acceptance and engine goldens were taken again: their generator stamped
  a Ready time 30 min before each cycle, a restart on every cycle to the
  tracker, and no line of theirs settled; it now keeps a Ready time for the
  day. The move then moves only those two goldens' lines on the cycles
  after a day-long clock jump: 25 hours without a cycle is an absence past
  the tolerance (`settle.Rejoined`), where the line's own trace counted
  absences only by cycles that missed a replica. The wait after a restart
  moves the in-process goldens and `ols-sweep-slo-restart`, where the first
  saturation line comes 75 s later (14:07:17 against 14:06:02) and the
  floor-held decode demand is 32 to 37 % lower for those five cycles. The
  other four switches move no golden and are held by their unit tests.
  `learn.ITL` joins `learn.Learners` (forget neutral: the same windows and
  intercepts, counted once); mu stays off it for 4b. The ITL
  `learner-write` band is 5 % on A or B, and a change of tier always logs.
  The closed-loop probe through a restart, on a decode fleet stepping from
  2.5 to 12 requests/s 15 s after the restart: snapshot off, under need
  for 3m30s against 15 s without the restart, inside the 5 min bound, and
  settles at need; the ITL wait adds nothing to that, k2's and mu's minute
  is what the step waits on. Snapshot on: as without the restart. A 120 s
  stabilization changes nothing in the probe: its fleet orders at need on
  occupancy once nothing queues, so the restart's gap shows on the way up,
  where stabilization does not enter. The probe does not reproduce the
  unmasked case 4.4 names, so no known-failing row was added for it; on
  the recorded restart the dip is 75 s, so a scale-down stabilization
  under about 90 s would let it through (configuration.md says so).
- **Problem:** the analyzers learn about fifteen quantities. Each one accepts
  readings, fits, forgets and logs in its own way. The same defects were fixed
  in one learner after another, because no learner could reuse another's fix.
- **Decision:**
  1. Build the test harness first, and make it deterministic: PR 0 carries the
     clock fix, the presence bits and the recorder. Every later step is judged
     by it.
  2. Share one **settle tracker** (the engine writes it), one **vocabulary**,
     and one **set of observability rules**. Do not share a loop that drives
     the learners, a direction enum, or a robust-fit library.
  3. Each behaviour change ships behind its own switch, on in production, so a
     replay proves that nothing else moved.
- **Shape:** eight PRs (0a, 0b, 1, 2, 3, 4a, 4b, 5). Each says what an operator
  sees and which recording it needs.

## Revision notes

Four reviews each of revisions 2 to 5 (architect, statistician, SRE,
skeptic). Round 1 is summarised at the end. A dash means the row was not a
blocker that round, and nothing in it changed.

| blocker | round 2 | round 3 | round 4 | round 5 (lows only) | where |
| --- | --- | --- | --- | --- | --- |
| The harness ran on two clocks | Every wall-clock read in the analyzers moves onto one injected clock **in PR 0**. Two replays at different wall times must give identical output | `store.go`'s five wall-clock sites are listed, and the engine's calls into the store take the same clock | - | - | [2.2](#22-one-clock) |
| The recorder sees too little | The recorder lives **inside** each analyzer; a test fails when an exported method goes unrecorded | It records at the **input boundary**: each variant's `capacity.Record` and the SLO analyzer's router latency go into the `Analyze` event, the engine's store mutations are events in the same stream, and `*capacity.Store` is in the coverage test. Nested exported calls record once. The test proves recording, not only classification | **The store events record what the engine supplies, not what the store ends up holding.** A live record carries analyzer state (`EffectiveCapacity`, `PriorLiveCapacity`, the carried k1), so `LoadFromScaleTarget` records the parsed `EngineParams`, accelerator, GPU count, `RollingOut` and `now`, and replay runs the one shared store method on its own store. `EvictStale` records the timeout and `now`; replay recomputes what goes. Each `Analyze` result is recorded, and 0b proves a replay at the recording's commit reproduces it byte for byte. `Judging()` is in `start`. The `With...` builders are a coverage bucket of their own | The exported values method (`FileScaleTarget`) is the recorded one; `LoadFromScaleTarget` parses, calls it and records nothing (a parsing-wrapper bucket). An `Analyze` that errs is still recorded, with the error in place of the result | [2.4](#24-the-recorder) |
| A mid-run snapshot is not the analyzer's state | - | A recording starts only at a controller start, and keeps its head at the cap. No mid-run snapshot. The claim that `ExportLearnedState` touches `LastUsed` was false and is removed | The cap is per recording, with a per-volume ceiling that holds two, so the recording after a container restart has room | The volume's `sizeLimit` is that per-volume ceiling plus headroom, not one cap (the deferred row now says so) | [2.4](#24-the-recorder) |
| Goldens without a stated rule | Change behind a switch; off equals the parent, on moves only named outputs | One switch per named change, set on by the constructor. Committed goldens are the all-on run; the off-run compares against the base commit's goldens. Runs: all off, each alone, all on. New recordings land with base goldens first. One golden file per output. The check re-runs on PR edits | The full store-record check runs in the all-off run only; the other runs compare the engine-supplied fields, and the analyzer-written store fields are a golden output. "All off" means this PR's switches. Replay runs every recorded analyzer together, or checks only the fields one analyzer reads. Each package exposes its switches through one exported test hook, a seam in the coverage test | The whole-record check runs only in the fidelity run (replay at the recording's own commit); all off compares the analyzer-written fields through their golden against `$BASE_SHA`, since a reused recording holds an earlier PR's k2. Elsewhere the engine-supplied fields are compared by the store's own rule (`EngineParams` by `ArgsFingerprint` and `Image`, hardware by `sameHardware`) and only where both records share `LearnedFrom`. `EvictStale`'s removed set fails only the fidelity run; elsewhere it is reported | [2.4](#24-the-recorder), [2.5](#25-replay-and-the-golden-rule) |
| Presence and idle | A known bit per gauge; idle is a two-gauge rule | ITL presence is three-state. Idle needs **positive** evidence, so two failed queries do not restart the settle fleet-wide | Idle evidence is role-aware: a prefill pod never has an ITL, so its idle reads the request or prompt-token rate. KV means peak KV. A listed row with unknown gauges is a sighting, and an idle run spans at most 1 min (revision 7: per gap, see [4.3](#43-what-the-tracker-owns)) | Prefill idle reads the request rate for both entering and leaving idle (the prompt-token rate is absent on SGLang). The brownout trade is stated: an idle-and-return inside a long brownout keeps its settle (cost side); PR 1 may end the settle after more than 2 min of unknown rows, and the section 8 brownout case pins the choice | [4.3](#43-what-the-tracker-owns) |
| Property tests | Twelve classes, thresholds enumerated, findings mapped | A known-failures ledger; a mutant per mapping row; estimands declared, so section 5's estimators can pass; time base is an input family | A section-5 estimator that over-prices when one pod is genuinely slow declares where it starts to, and the case is in the ledger, owned by PR 3 | - | [2.6](#26-property-tests) |
| Settle tracker ownership | The saturation analyzer owns the one tracker | The **engine** owns it and observes rows at the top of `runAnalyzers`, before the balance tracker and every analyzer. The throughput analyzer takes the view at registration | The engine's observation is a recorded event with the engine's own clock read. A rollout is detected from the rows' `Revision`, not from `capacity.Record.RollingOut`, which is filed later in the cycle | - | [4.2](#42-one-owner), [4.3](#43-what-the-tracker-owns) |
| PR 4a was called neutral | - | Its two behaviour changes are named, each behind a switch, tested by the restart suite with a negative control | 4.4 states what an operator sees during the ITL wait (the floor is silent for about 2.75 min, the SLO side, masked by the 300 s scale-down stabilization). 4a runs the closed-loop probe through a restart, and the missing-row case runs through ITL | The masking holds "as long as the controller is back within about 2 min" | [4.4](#44-the-cost-after-a-controller-restart), [7](#7-migration) |

Also in round 4: the start-time and B last-admitted stamp is due in PR 5 or the
first PR that publishes either age. 6.2 no longer contradicts the deferred
Forget row. The status line says which branch each cited line is from.

Also in round 3: the closed-loop probe is a fleet simulator and moves to PR 3,
its first user. PR 0 splits into 0a and 0b. Counters that are zero by
construction move to the PR that makes them non-zero. PR 1's named change is
proved by a unit test on the balance tracker. Hysteresis covers the k2 key and
two-sided learners. Smaller items are in
[Deferred to the implementing PRs](#deferred-to-the-implementing-prs).

Round 2 also wrote in: `restored: true` is a field, not a source value.
`learner-write` is at most one line per key per cycle, against the last logged
value, with a heartbeat. Per-key series deletion is gone. One start line per
model. Start time has two risky sides. PR 4 is split. Each PR names its
recording.

## Why

The two-namespace runs of October 2026 (issue #270, its rerun, E1 and E2) and
the reviews of #273 and #282 found six kinds of defect:

| defect class | where it hit |
| --- | --- |
| **A transient admitted as a reading.** A replica that just joined, the first minute of a load step, a slow fill. | ITL intercept learned from a ramp (#273). k2 taken at 8 % KV behind a 160-request step (#282). k2 recorded mid-fill on long outputs (#282). A replica just Ready counted as idle room (#282). A filling replica's token rate taken as mu, 11 % high (#282 round 4). |
| **The first sample adopted raw.** | A fresh pod's 0.0995 req/s became the bucket's mu (E1, #282). k2 prices its first admitted reading at once. The start-time estimator's first measurement replaces the 70 s default. |
| **A stale value never refreshed.** | A measured mu outranked a derived one for the rest of a run (#282). The 6 ms default intercept stayed on every multi-replica fleet (#273). |
| **A missing reading read as a value.** | k == 0 meant "not collected" and was traced as zero load (#273). A NaN KV read as 0, and so as idle (#283 review). |
| **One reading dominates.** | Least squares pulled by three joined-pod readings out of sixteen (#273: now dropped by state, then a k-weighted mean). The saturated throughput sample was the hottest replica (#282: now the mean across replicas, bounded by the served rate). One slow replica set the fleet's mu (#282 round 4: now yields to its full siblings). |
| **A hard edge with no hysteresis.** The most frequent class. | The k2 key boundary swung capacity 39 % at constant load. Demand fell 48 % for a 2 % rise in lambda. Mu flipped between measured and derived 11 times in 20 cycles under 4 % noise. `backlogGrew` swung demand 1.54x on one request (fixed on #282 by e7d9f0ff1). |

The joined-replica test now exists four times, with four sets of rules (see
[Settled replicas](#4-settled-replicas)): the case for one settle tracker. The
last class no shared code fixes. Only a test that perturbs the inputs finds it,
so the harness comes first.

**Errors are asymmetric.** A capacity that is too high breaks the SLO. One that
is too low costs money. [The k2 design](../plans/analyzers/k2-capacity-model.md)
draws its rule from that: nothing outside admission raises k2. No floor, no
age-out, no reset. That rule stays.

**What this answers for an operator,** after PR 5:

| question | answered by |
| --- | --- |
| Why did a light model run at 0.95 utilisation? | `throughput-demand-floor` names `muKey` and `muSource` (PR 3); mu's `learner-write` shows the first sample, its pod and its joined age. The `[1m]` peak after a swap belongs to no learner and stays open |
| Why did a fleet hold 3 replicas? | `heldAtFleet`/`heldWhy` (exist), `muSource`, `startEstimateSeconds` and `startSource` (PR 3), `k2-decision`'s P-code |
| Why did the second GPU transfer wait? | **Not answered here.** The share planner's holds are out of scope; they belong to the utilization-share work (#272 and its follow-ups) |

## 1. Inventory

D = `internal/engines/analyzers/saturation`. "Persisted" means it is in the
learned-state snapshot (`D/learned_snapshot.go`, ConfigMap `wva-learned-state`,
off by default).

Since #298 most admission and settle figures below are operator policy keys:
`analyzerTuning` and `loadImbalance` in a scaling policy
(`internal/config/analyzer_tuning.go`, documented in
[scaling policy](../reference/scaling-policy.md#advanced-tuning-analyzertuning-and-loadimbalance)).
The numbers in this document are their defaults.

| # | quantity | where | admission | fit | memory and forgetting | persisted |
| - | --- | --- | --- | --- | --- | --- |
| 1 | ITL line `ITL(k) = A·k + B`, saturation analyzer | `internal/signals/itl/`, wired in `D/analyze.go` | Ready, not a bridge, finite ITL > 0, k > 0. k inside the policy KV band (`analyzerTuning.itlBand`), or above it when the replica's batch is at its sequence cap (`max_num_seqs`, lowered by `--max-num-active-seqs`, #331) with preemption at most `itlBand.capMaxPreemptionRate` (`internal/signals/itl/admission.go`, #293). Each reading goes to the main window, the main and steady windows, or nowhere (see [Admission shapes](#33-admission-shapes)). A reading taken while its replica settles (joined, or load returned after idle, under `steadyGate.steadyFor`, 2 min) is stamped and kept out of the pinned fit | OLS on the steady window at ≥ 10 rows spread ≥ 0.30. Otherwise A = k-weighted mean of `ITL − B` over unstamped readings, B pinned. Fewer than `steadyGate.minSettledSamples` (3) such readings, or fewer than 10 in the window at all (light or overloaded load ages them out): the last A is held (`SlopeHeld`), up to `BucketRetention` after its fit and under the same args; a held line is not republished to row 2. A < 0 refused | Main 20 rows / 30 min. Steady 40 rows, carried `steadyGate.carry` (5 min) past the main window. Swept 24 h unseen. Args change stashes B | B, its stash, and the held slope (`ITLSlopes`, #329): without it a restart under light load had no line and sized by KV occupancy alone (issue #316) |
| 2 | Shared and borrowed ITL line | `D/analyzer.go` `publishLine`/`borrowLine` | `EnableLearnedStateReuse` (off), same fingerprint | none | Ages on last write. May hold a fleet, never order one | yes |
| 3 | k2, KV at saturation | `D/replica_capacity.go` `k2Chain`, `D/k2_settle.go`, `D/keys.go` | Queue ≥ threshold; downstream not saturated; not a bridge; not mid-rollout; under the physical ceiling. Joined under 1 min, counted from the later of the Pod's Ready time and the first sight of that Ready time (`noteReady`, `D/k2_settle.go:197`, #288): refused. When queue × footprint exceeds what the busiest rank holds, also: rate ≥ `k2Settle.rateFraction` (0.7) of mu on record **and** residency ≥ 0.7 of k2 on record, **or** residency within ±`k2Settle.maxRise` (25 %) of the reading 1 min back with the queue standing on every cycle. On decode of a P/D model, the queue k2 reads is the engine's own deferred queue, rewritten before k2 reads it (`D/pd_decode_queue.go`, #294, #306) | Mean of a 10-sample window. Admitted readings raise it as well as lower it. The first admitted reading prices at once | Key: the fleet's output length for decode and aggregated, its bucket held by a band (smoothed over 10 min, 3 % past the boundary, or 20 % past it for 2 min; #396); prefill's own average. 24 h write gap starts a new episode. Evicted 7 d after last read | yes, with args tags and stash |
| 4 | Borrowed k2 (lower bound) | `D/k2_borrow.go` | Only when lower than the variant's own k2. KV total within 3 % | none | Held 30 min, then relaxed **upward** over 6 h of serving time. Re-arms on an args change or a figure ≥ 20 % lower | yes |
| 5 | Measured mu | `D/throughput_floor.go`, `D/throughput_admission.go` | Only on a P1-obs k2 write, so it inherits k2's joined test. RequestRate > 0. Not filling: generated tokens at most `admission.maxTokensOverCompletions` (1.15x) completions × output length. Not under half the highest rate a Ready replica of the variant served, its own included (`servedBounds`). Not under `muChoice.minMeasuredOfDerived` (0.5) of derived, unless the same pod read that low on every saturated cycle for 2 min | One sample a minute. Within the minute a reading rewrites the last sample: max per pod, **mean** across pods (`meanOf`, `D/throughput_floor.go`, #282): a fleet serves the sum of its replicas' rates | See [the mu choice](#the-mu-choice) below. Evicted 7 d after last read | window yes, choice no |
| 6 | Derived mu | `D/mu_from_itl.go` `deriveMu` | none: a formula on #1 per replica | none | As fresh as the line | no |
| 7 | Prefill throughput bound | `D/prefill_bound.go` | `EnablePrefillThroughputBound`. Saturated, serving rows. No joined test, no rate check, no band | Window max. It writes through #5's fold with pod `""`, so it kept its max only because every prefill pod folds under one empty key | **Only rises.** The prior max is read before the write and put back with `RaiseLast`, so it survives the 24 h episode reset. Touched on every read, so never ages while pricing. Cleared only by an args change or a forget | with #5 |
| 8 | Fleet shape and shape-change hold | `D/shape_change.go`, `D/fleet_shape.go` | Tolerance band on the router's prompt length | Rate-weighted mean | Hold up to 5 min | no |
| 9 | Decode-saturation memory | `D/roles.go` `roleSaturated` | One full decode pod gates prefill learning (the #282 fix that changed this was reverted, 7ab00fa8c). Bridges count | none | 1 min | no |
| 10 | Idle room | `D/idle_room.go` | Decode only. Idle on every reading for 2 min, no gap > 1 min, not a bridge. A run starts under the idle line and goes on under 1.4 times it (#396) | none | Run resets on a non-idle reading | no |
| 11 | Floor arrival rate, trust and backlog memory | `D/throughput_floor.go`, `internal/signals/floor/` | none | Blend from 0.5 to 0.8 of resident demand. A queue counts as grown past a noise band over a minute. An engine's own queue is capped at what it drains (mu × T) **only while it stands**; while the role's backlog grows it is charged in full (round 4) | Last cycle's backlog per role; `backlogGrew` remembers 1 min, so the full charge outlives growth by up to a minute (an over-order) | no |
| 12 | Replica start time | `D/start_est.go` | Each pod once, not a bridge, 0–3600 s. A sample over 3x the estimate is refused until 3 such, counted per variant **per rejected sample**: three outlier pods in one cycle exhaust the guard, and one pod repeating passes on its fourth cycle | EWMA α 0.3. The first sample replaces the 70 s default (`DefaultReplicaStartSeconds`, `D/start_est.go:191`). The ScaledObject's `replicaStartSeconds` is reserved and read by nothing today ([trigger reference](../reference/scaledobject-trigger.md)) | Seen pods not persisted: after a restart every running pod is folded again. The floor reads it as a min per role, share pacing as a max | yes |
| 13 | SLO analyzer line | `internal/engines/analyzers/slo/` | Its own ITL tracker, same rules as #1 | Same as #1 | 24 h unseen | no |
| 14 | Share need history | `internal/engines/allocation/utilization_share_ledger.go` `RecordNeeds`, `PeakNeed`, `PeakKnown` | A peak needs two readings in a row. After a restart a full swing window before a peak is known | Mean over the swing window. Step detector `ShareStepSpread` 0.25 | Trimmed to the window | yes, `wva-utilization-share-history` |
| 15 | Share release time | same file, `MeasuredRelease` | Completed releases | 90th percentile of recent releases, needs ≥ 3 | Recent releases only | no |
| 16 | ITL line, throughput analyzer | `internal/engines/analyzers/throughput/analyzer.go` `resolveITLModel` | The policy KV band only (`ITLBand()`, the same `analyzerTuning.itlBand` keys as row 1, `throughput/analyzer.go:270-275`). No cap admission, no steady gate | OLS, else B pinned to `lastFittedB` | Per variant | no |

#### The mu choice

A measured mu and a derived mu are both offered every cycle, and
`measuredYieldsToDerived` picks one. It keeps the measured window either way.

| rule | when |
| --- | --- |
| Yields to derived **at once** | the measurement is under derived and a Ready replica (its own included) serves more than `muChoice.maxRatio` (2x) it |
| Yields to the full siblings (`siblingYield`, #282) | the measurement is under `admission.siblingMuTolerance` (0.8) of the lower median served by every Ready replica with a full cache, queued ones included. A yield opens only when at least one of them is unqueued. Logged as `saturated-throughput-yields-to-siblings`, bucket `served-siblings`. Remembered in `muChoice` until the measurement comes within 0.8 or the window starts over |
| Yields when stale | freshness is the last saturated reading that **agreed** with it within 2x. Above derived: after `muChoice.freshFor` (5 min). Below derived: after `muChoice.lowHoldFor` (30 min) |
| Latch | out at `muChoice.maxRatio` (2.0), back at `muChoice.returnRatio` (1.6) |

**Restored to the safe side (PR 3):** the latch is not persisted. Before PR
3, after a restart a measured mu between 1.6x and 2x **above** derived came
back unlatched and priced from the **higher** measured figure, the SLO side.
A latch with no evidence behind it now restores latched over the derived
figure and unlatched under it ([3.5](#35-hysteresis);
`mu-latch-restores-safe-side`). A one-replica variant with no line has
nothing to yield to.

Also learned and not tabled: the hot-rank hysteresis memo and the share swing,
which stabilize the controller's own decisions rather than learn from replicas.

The snapshot carries #1 (B, its stash and the held slope `ITLSlopes`),
#2–#5 and #12, plus the accelerators, the forgotten set, both stashes, and
`K2BorrowedSince` and `PriorK2`, which are written beside `LowerBounds` so a
rollback build still reads them. A restore never makes a figure younger, and an
unknown age resolves to the safe side ([3.4](#34-which-side-breaks-the-slo)).

Row 16 has none of #273's fixes (steady gate, settling stamp, k > 0 guard) and
none of row 1's cap admission. It reads the same policy band as row 1. It
moves in PR 4b.

## 2. Harness first (PR 0)

### 2.1 What the recorded runs can and cannot replay

The E1, E2 and #270-rerun logs were the planned fixtures. They cannot carry
most of the migration:

- **No instant KV, no ReadySince, no warm-pool flag** on any line. The
  instant-KV paths and the Ready-time rule never run in replay.
- **k is the one-minute peak** in 973 of 1257 reconstructed E1 rows. Only 39
  are raw.
- **No per-replica generation or completion rate.** No measured mu is ever
  written, so a mu replay compares nothing.
- **OLS is never reached.** B is the 6 ms default on every cycle, so "same A
  and B" says nothing about B, its stash or the carry.
- E2's replay CSV holds one namespace. The rerun has none. `D/testdata/k2-fill/`
  is simulated, and `TestE1Replay_*` is about 20 hand-copied rows.

What they can test: the ITL pinned tier, on reconstructed k.

No recorded run covers: **P/D**, **whole-node 8-GPU and DP ranks**,
**multi-node LWS**, **SGLang**, **EPP flow control off** (where P1-obs fires
most, so k2's main path), **shape changes**, **controller restarts and the
snapshot**, **warm-pool bridges**, and **borrowed lines** (reuse on). All three
runs are one model, decode only, one shape, 1 GPU per replica.

PR 0 has five parts: one clock, presence bits, the recorder, replay with
goldens, and property tests. It ships as two PRs. **0a** is the clock and the
presence bits, with their unit tests and negative controls. **0b** is the
recorder, replay, goldens, the CI check, the property tests and their ledger,
and the first recording, taken from a build with 0a merged. 0a changes no
output except as [2.2](#22-one-clock) states; 0b changes nothing in
production.

### 2.2 One clock

A replay drives `a.now()` from the recording. Every other read of wall time
then stamps replay time, so goldens differ from run to run, and an episode
reset or an eviction depends on when the replay ran. Each site below takes
`now` from its caller, or from the one clock injected into the analyzer.

| site | what reads wall time today |
| --- | --- |
| `internal/signals/capacity/rolling_average.go` (the k2 and mu windows) | `NewRollingAverage`, `Add`, `Observe`, `Touch` (the use clock, also called by the prefill bound), `Stale`, `WriteGapExceeds`, `WriteAge`. Each gains an `...At` form |
| `internal/signals/capacity/store.go` (the capacity store) | five sites: `LearnedAt` in `Update` (`:121`) and in `LoadFromScaleTarget` (`:289`); `IsStale` (`:187`); the `PriorLiveAt` window in `LoadFromScaleTarget` (`:268`); `EvictStale` (`:339`). Each takes `now` from its caller |
| `internal/engines/steadystate/engine_v2.go` (the engine's store calls) | `LoadFromScaleTarget` (`:75`) and `EvictStale` (`:606`) pass the same injected clock the analyzers use; the engine holds it |
| `D/analyze.go` | `AnalyzedAt` (also excluded from goldens) |
| `D/analyzer.go` | the accelerator memo touch; the pending-restore sweep `alive(a.now(), time.Now())` and `pruneWindows(time.Now())` |
| `D/roles.go` | the accelerator memo |
| `D/learned_snapshot.go` | the snapshot writer and restore stamps |
| `internal/engines/analyzers/throughput/analyzer.go` | `now := time.Now()` inline in `Analyze`, no seam at all |
| `internal/engines/analyzers/slo/` | already has a `now` field; wired to the same clock |

The snapshot compares persisted wall stamps on restore, so in production the
injected clock **is** wall time. In replay it is the recorded clock.

**Acceptance:**

- The same recording replayed twice, with the wall clock 30 days apart, gives
  byte-identical outputs.
- A test fails on any `time.Now()` in these packages outside the injected clock.
- Negative control: a fake clock that jumps past `EpisodeGap` resets the k2
  window. It fails on the parent, where the window stamps wall time.
- One clock reading per cycle stamps every `Add` of that cycle at the same
  instant, where today each call reads its own `time.Now()`. A comparison at
  the one-minute mu spacing or the 1 min joined lookback can move by one
  cycle. That is 0a's only output change; a unit test pins it at the
  boundary.

Tests can already age a window with `ObservedAt` (`sweep_keying_test.go`,
`eviction_regression_test.go`). What they cannot do is age it **on the same
fake clock** that `a.now()` uses. That is the defect.

### 2.3 Presence bits

Three bits exist (`internal/domain/replica_metrics.go`):
`KvUsageInstantKnown`, `CapacityQueueKnown` (#294, the engine's own queue)
and `PreemptionRateKnown` (#293). The collector turns NaN, Inf and absent
series into 0 (`collector/extract.go`). PR 0a reuses the three bits and adds a
`...Known` bit beside each other gauge a learner reads: peak KV, ITL
(`AvgITL`), request rate, generation-token rate, prompt-token rate, and
`RunningRequests`. The engine-queue bit is `CapacityQueueKnown`; 0a does not
add a second one. A bit is true when the series was present, finite and inside
its block's range. A read zero is known (no work), except for ITL: ITL is
`ITLFinite` only for a finite value above 0; a NaN is `ITLEmpty`; ±Inf, a
value at or below 0, or a failed query is `ITLAbsent`. A not-Ready Pod clears
only its ITL bits. Ranks merge by AND; for ITL, Finite needs one Finite rank
and none Absent; Empty needs every rank Empty and none that generated tokens.
The Pod's mean is generation-weighted over its Finite ranks only (#341 4a's
named change, made in the collector ahead of 4a): an Empty rank that generated
tokens (max_tokens=1) has no interval, so it neither dilutes the mean nor
blocks Finite. The per-pod query divides by inter-token intervals instead
(generated tokens less one per request). The merge does not weigh by
intervals: that would need tokens less the request rate, which counts finished
requests and bursts on a drain, so a busy rank would drop out of the mean for a
cycle, and a slow rank dropping out reads ITL low. Token weights are smooth.
Where they differ from the per-interval mean they favour the rank serving
short requests, usually the slower one, so the Pod reads higher: the safe side,
though not a guarantee (a hot rank of long requests reads slightly low). The k
paired with
that ITL is still capacity-weighted over every rank, Empty ones included, so a
pod with a busy Empty rank pairs the Finite ranks' ITL with a lower k. That
fits ITL(k) high and mu low, the side that over-orders; it is left as is and
must not be "fixed" by putting the Empty ranks back into the ITL mean. A
prefill-role Pod reads Empty permanently, so readers gate by role.

`RunningRequestsKnown` is a prerequisite of #277 (reading
`num_requests_running` as resident load). `RunningRequests` is "zero when the
metric is unavailable" today, so without the bit a missing series would read
as 0 resident. #277 reads it as that: where the running requests are read, the derived mu
prices a resident request at the KV they hold, between the hit rate's discount
and none; a row without the bit is no reading, and with no reading the
discount stands (behind the switch `kv-per-request-hits-resident`).

**ITL is three-state:** finite, empty window, or absent. ITL is
`rate(sum[1m]) / rate(count[1m])`
(`internal/collector/registration/queueing_model.go`), so an idle replica
reads 0/0 = NaN with the series present ("NaN is the ordinary case here",
`recent_window_extract_test.go`). A failed or timed-out query reads absent. A
single bool would merge the idle signal with a failed query, so ITL carries
`AvgITLKnown` and `AvgITLEmpty`.

- The recorder writes the bits. It never encodes "missing" as NaN:
  `encoding/json` refuses NaN and would drop the whole line.
- 0a's test: the collector sets each new bit on a present, NaN, Inf and absent
  series, and ITL's three states on a live, an empty and a failed query. The
  three existing bits keep their tests.
- Negative control, in PR 1 (the first PR with a tracker to read the bits):
  #273's A4 case (k == 0 unsettles a replica) and 273-r3 #2 (a missed scrape
  restarts a settle) fail on the parent.
- Until a learner moves onto the tracker it ignores the bits, so this part
  changes no output.

### 2.4 The recorder

A wrapper around `domain.Analyzer` is ruled out. The engine reaches six
capabilities by type assertion (`variantForgetter`, `staleHistoryEvictor`,
`learnedStater`, `acceleratorStabiliser`, `presenceNoter`, `sloStateKeeper`).
A wrapper hides them: with the flag on, forget, eviction, snapshot restore and
export, and accelerator stabilisation would silently stop.

So the recorder is a field inside each analyzer (`a.rec`, nil when off). The
saturation, SLO and throughput analyzers each get one, and so does the shared
`*capacity.Store`. All write to one sink with one `seq`.

The recorder owns its schema. 0b lands before #345 step 1 creates
`AnalyzerInput`, so the `Analyze` event mirrors the input shape that
engine-structure stage 3 plans: the input, each variant's capacity record,
`VariantMetadata`, and the merged policy including `analyzerTuning`.
Recordings survive step 1, and step 1's "zero golden change" is measured on
them.
The store's scale-target split is the seam stage 3 moves into the input
builder.

**It records at the input boundary,** not only at the analyzer's methods. An
input is anything an analyzer reads that it did not compute. Two inputs reach
the analyzers without passing an analyzer method:

- **The capacity store.** `NewSaturationAnalyzer(capacityStore)` and
  `slo.New(replicaCollector, capacityStore)` share one `*capacity.Store`
  (`steadystate/engine.go:334-335, 404`). The engine writes it before every
  `Analyze`: `LoadFromScaleTarget` per variant (`engine_v2.go:75`) files
  `EngineParams`, `NumGpuBlocks`, `EffectiveCapacity`, `RollingOut` and
  `PriorLiveCapacity`, and `EvictStale` (`:606`) sweeps it. Pricing reads it:
  `deriveMu` and `MaxNumSeqs`, the line fingerprint that keys sharing and
  borrowing, the borrow compatibility test, the per-replica capacity
  fallback, restore install (`installRestored` leaves a restore pending while
  the record has no `EngineParams`), and the args tags that decide a stash.
  Replayed without it, every `engineParamsFor` returns nil: no fingerprint, no
  reuse, no stash, every restore pending, and a rollout admitted that the run
  refused. The goldens would describe a controller that never ran.
- **Router latency.** The SLO analyzer calls
  `a.router.CollectRouterLatency(...)` inside `Analyze`
  (`slo/analyzer.go:382`), a Prometheus query.

So the store's recorder logs each **engine-side** mutation in the same
stream, as **what the engine supplied**, never as the record the store ended
up holding. For a live variant the filed record is partly analyzer state: with
the same arguments and hardware the live record is kept and only
`EngineParams` and `RollingOut` are swapped (`store.go:245-255`); on an
arguments change `PriorLiveCapacity` and `PriorLiveAt` come from the run's
last `EffectiveCapacity` and `LearnedAt` (`:264-266`); and the carried k1 is
the run's measured `TotalKvCapacityTokens` (`:276-278`). All of these were
written by the saturation analyzer's `Update` (`replica_capacity.go:371`). A
replay that copied them back would price every stored-figure reader (the
zero-replica estimate, the sibling clamp, the compatible lookup) from the
run's k2, not its own, and a PR that moves k2 would report those outputs as
unmoved.

- **`LoadFromScaleTarget`** records the parsed `EngineParams`, the
  accelerator, the GPU count, `RollingOut` and `now`. The function splits
  into parsing the scale target and one store method that takes those values;
  production and replay call the same method, and replay calls it on its own
  store, so the kept live record and `PriorLiveCapacity` come from the
  replay's own `Update`s. Replay lives outside `capacity`, so that method is
  exported (for example `FileScaleTarget(namespace, model, variant,
  accelerator, gpuCount, params, rolling, now)`), and **it** is the recorded
  method. `LoadFromScaleTarget` parses and then calls it, records nothing of
  its own, and the coverage test lists it as a parsing wrapper. The
  accelerator in this event is itself an analyzer answer (the engine passes
  `StableAcceleratorFor`'s result, `engine_v2.go:72-76`), so replay applies
  the recorded value and does not recompute it.
- **`EvictStale`** records the timeout and `now`. Replay recomputes what it
  removes; what the run removed is recorded as a check, not applied. A
  mismatch fails only the fidelity run; in every other run it is reported,
  because `LearnedAt` follows the replay's own `Update`s.
- The store's other mutators, `Update` (`replica_capacity.go:371`) and
  `Delete` (`forget_variant.go:76`), are called by the saturation analyzer,
  so replay reproduces them from the analyzer events; recording them too
  would apply them twice.

The `Analyze` event carries each variant's `capacity.Record` as read at the
top of `Analyze`, and the SLO event carries the router latency. Replay checks
its own store against those records before each call:

| run | store check before each `Analyze` |
| --- | --- |
| fidelity (0b's acceptance: replay at the recording's own commit) | the whole record. A mismatch fails the replay |
| every replay run (all off, each alone, all on) | the engine-supplied fields, by the store method's own rule, not by field equality: `EngineParams` by `ArgsFingerprint` plus `Image`, the hardware (accelerator, GPU count) by `sameHardware`, and `RollingOut` exactly; and only where the replay's record and the recorded one share `LearnedFrom`. A mismatch fails the replay |

The analyzer-written fields (`EffectiveCapacity`, `TotalKvCapacityTokens`,
`PriorLiveCapacity`, `PriorLiveAt`, `LearnedFrom`) are a golden output, one
file under [2.5](#25-replay-and-the-golden-rule), so a named k2 change shows
where it reaches the store. All off compares them through that golden file
against `$BASE_SHA`, as it does every other output, and not against the
recording: a recording is reused across PRs, so it holds the fields as the
recording's commit computed them, while all off reproduces the base, which
already carries every earlier PR's switches (PR 3's all off reproduces PR 2's
k2).

Why the engine-supplied check is scoped: on a live record with the same
arguments and hardware, `LoadFromScaleTarget` keeps the record
(`store.go:250-258`). The accelerator and GPU count are then the ones the
saturation analyzer's `Update` filed (`replica_capacity.go:371-374`), and
`EngineParams` is carried forward, swapped only on an image or `RollingOut`
change, so it can differ from this cycle's parse in fields the fingerprint
ignores. A switch that changes when, or whether, a variant first files live
moves these fields with no defect present. Where `LearnedFrom` differs, they
are covered by the golden output instead. Latency is served from the
recording; replay never queries Prometheus.

**A replay runs every recorded analyzer together,** in `seq` order. The SLO
analyzer's `Analyze` runs after the saturation analyzer's `Update` has
rewritten the record, so an SLO-only replay has nothing to reproduce those
writes. A replay of one analyzer alone therefore checks only the record fields
that analyzer reads, listed beside its recorder (the SLO analyzer reads only
`EngineParams`, `slo/analyzer.go:433`).

**Each result is recorded.** The `Analyze` event also records its
`AnalyzerResult` (minus `AnalyzedAt`), and each store event what it returned.
An `Analyze` event is written whether or not it errs, with the error in place
of the result: `runV2AnalysisOnly` returns on a saturation error
(`engine_v2.go:101-104`) after the engine has observed the tracker, and that
cycle's settle observation needs its rows.
Recording inputs only, a replay that misses an input still gives stable
goldens; the recorded result is what catches it. 0b's acceptance: a replay at
the recording's own commit reproduces every recorded result byte for byte. A
recording that fails this is not committed as a fixture.

**Nested calls record once.** `Analyze` calls `a.StableAcceleratorFor` per
variant and per replica, and the throughput `Analyze` calls its own
`Observe` (`throughput/analyzer.go:254`). Each exported method records and
then calls an unexported implementation; internal callers use the
implementation. A replay therefore applies each event once.

**What it records,** one recording per controller start, all models and
analyzers in call order (borrowed lines cross models), each event with `seq`
and `now`:

| event | content |
| --- | --- |
| `start` (first line) | cold start, or the restored document as `RestoreLearnedState` received it; the store's records; the configuration the `With...` builders set (`WithHorizons`, `WithOptimizationInterval`, `WithStateTimeout`); whether the SLO analyzer had a router (`Judging()`), so its replay builds a recorded-latency router exactly when the run had one. Replay refuses a recording without it |
| `Analyze` | the full `AnalyzerInput`: every `ReplicaMetrics` field with its presence bits (instant and peak KV, ReadySince, `Revision`, the warm-pool flag, per-replica generation, completion and prompt rates, queue, ITL, KV capacity); per-variant GPU count, role, accelerator (`VariantStates`); each variant's `capacity.Record`; the scheduler queue; the arrival rate; the policy, with its merged `analyzerTuning` and `loadImbalance`, every cycle; for the SLO analyzer, the router latency; and the `AnalyzerResult` it returned, minus `AnalyzedAt`, or its error. Written whether or not it errs. The rows are recorded **raw**, as `Analyze` received them: the P/D decode deferred-queue rewrite (`decodeCapacityView`, `D/pd_decode_queue.go:63-71`, `QueueLength` rewritten at 69) changes `MaxRankQueueLength` and `QueueLength` inside the analyzer, before k2 reads them, and is skipped when `!CapacityQueueKnown` or at the batch cap, so replay must run it again |
| store mutations, engine-side | the exported values method behind `LoadFromScaleTarget` (the parsed `EngineParams`, accelerator, GPU count, `RollingOut`, `now`), `EvictStale` (the timeout and `now`; what it removed, as a check that fails only the fidelity run) |
| settle observation, engine-side (from PR 1) | the model and the engine's own `now`, read where the engine observes the tracker; the rows are the next `Analyze` event's ([4.2](#42-one-owner)) |
| `RestoreLearnedState`, `ExportLearnedState` | the document in or out |
| `ForgetVariant`, `ForgetLearnedState`; SLO `ForgetVariant` (#431) | the variant, key and cause (annotation, model gone) |
| `EvictStaleHistory`, `NoteVariantsPresent`, SLO `BeginCycle`, `EvictStale`, `NoteVariantsPresent`, throughput `Observe` (engine-called only) | the arguments |
| `HoldLowerBounds` | the flag |
| `StableAcceleratorFor` | the arguments and the answer, when the engine calls it (it moves the memo) |
| `TakeRestoreOutcomes` | that it ran: it drains a map no output reads |
| borrowed lines | `publishLine` and `borrowLine` across models, as they happen |

A note on `LastUsed`: `ExportLearnedState` copies under `a.mu` and touches
nothing (`learned_snapshot.go:467`). The only `LastUsed = time.Now()` is in
`notePendingPresent` (`:1392`), reached from `NoteVariantsPresent`, which is
recorded. Revision 3 said otherwise; it was wrong.

**Coverage test,** in two parts, run over `*SaturationAnalyzer`,
`*slo.Analyzer`, `*ThroughputAnalyzer` and `*capacity.Store`:

1. By reflection, the exported method set equals the union of six reviewed
   buckets, and a new method fails until it is classified:
   - the recorded methods (for the store, the exported values method and
     `EvictStale`);
   - parsing wrappers, which call a recorded method and record nothing of
     their own (`LoadFromScaleTarget`);
   - pure readers (`Name`, `Horizons`, `ForgottenToken`, `BorrowTickGap`,
     `StateTimeout`, `Judging`, `Get`, `FindCompatible`, `FindClampBound`,
     `IsStale`, `VariantState`);
   - configuration builders, captured in `start` (`WithHorizons`,
     `WithOptimizationInterval`, `WithStateTimeout`);
   - for the store, the mutators named as analyzer-internal with the analyzer
     event that reproduces each (`Update`, `Delete`);
   - test seams: the one switch hook per package
     ([2.5](#25-replay-and-the-golden-rule)). A test asserts no production
     path calls it.
2. A table test calls each recorded method once with a recorder and asserts
   exactly one event of its kind. One `Analyze` emits exactly one `Analyze`
   event, whatever it calls inside.

A third test asserts the engine's six type assertions still hold with
recording on.

**A recording starts only at a controller start,** cold or restored. There is
no mid-run snapshot: the learned-state document is not the analyzer's state.
It omits `decodeSaturatedAt`, the hot-rank memo, `fleetShape`, the ITL
windows, `itlArgs`, `throughputHeld`, `boundsHeld`, `startSeenPods`,
`startOutliers`, the mu-choice latch and the settle tracker, and restoring it
stages windows rather than installing them. A whole-state debug dump (a
field-coverage test over each analyzer's struct and a dump, load, dump
round trip) would allow one, and is a PR of its own if a recording ever needs
to start mid-run. None of the PRs here needs it.

**Where recordings go:**

| item | rule |
| --- | --- |
| default | off |
| volume | a separate `emptyDir` named by a recorder overlay (to be added under `config/`). Never `/tmp`, the controller's 64Mi `emptyDir`: the kubelet evicts the pod when it fills |
| size | about 1–1.5 KB per replica per cycle. At a 15 s cycle: 8 replicas fill 64Mi in about 33 h, 50 replicas in about 6 h; 100 replicas write about 0.5 MB/min |
| cap | a byte cap **per recording** (default 256Mi), and a per-volume ceiling that holds two recordings at the cap. Files are named by start time. At the cap the recorder keeps the head, stops writing, and counts and logs the lines it drops. A controller restart starts a new recording; a container restart (crash, OOM) keeps the `emptyDir`, so the recording after it has room of its own (PR 5's restart fixture is that recording). At the volume ceiling the oldest closed recording is deleted first. No recording starts mid-run |
| external sink | instead of the volume, a file a log shipper reads. Never the controller log: the kubelet rotates it, and it interleaves |
| collection | `testing.md` says how to copy the files out (`kubectl cp`) before teardown; the benchmark harness does it on every exit path |
| content | inputs, plus each `Analyze` result as a fidelity check. No prompts, payloads or headers: the collector never sees them |
| before commit | an export step maps namespace, model, pod, variant and `Metadata` values to stable pseudonyms, drops cluster names, and gzips. A committed fixture stays under a stated size limit |

### 2.5 Replay and the golden rule

A replay drives the analyzers from a recording and writes its outputs to golden
CSV committed beside the fixture, **one file per output**, so a file diff
names the output.

**What it compares:** analyzer outputs only. Role demand; variant capacities
with their sources and keys; priced figures; per-key window contents; the
learner reports. Required and spare capacity come from the engine's threshold
post-step, not from `Analyze`, so an analyzer replay does not compare them.
`AnalyzedAt` is excluded. 0b records the analyzer boundary only. An
engine-level golden layer (the composite, its holds and their reasons,
`ReleaseBound`/`OtherFrames`, the warm-pool supply and pressure, the
optimizer's decisions) is #345's and outside this plan: #345 step 0 adds its
own recorder events (composite, optimizer and warm-pool inputs) to 0b's sink
and builds its goldens on them. Status: the composite and the warm pool's
figures are recorded and replayed (`engine.Composite`, the `engine` golden;
[testing](../developer-guide/testing.md#the-engine-layer)); the optimizer's
decisions get their own PR, before #345 step 3.

**The rule.**

- **One switch per named change.** Each is an unexported field that the
  constructor sets **on**, so production runs the new behaviour in the PR that
  adds it. It is a test seam, never a feature flag: no option, flag or
  ConfigMap key reaches it.
- **One exported hook per package.** The replay drives the saturation, SLO
  and throughput analyzers and the store together, four packages, and a test
  in one package cannot set another's unexported field. So each package
  exposes its switches to the replay through one exported test hook (for
  example `SetSwitchesForReplay(map[string]bool)`). The coverage test
  ([2.4](#24-the-recorder)) lists the hook as a seam, and a test asserts no
  production path calls it.
- **A switch is not a policy key.** Switches are internal, exist for replay,
  and are deleted with their seam. Policy keys (`analyzerTuning`,
  `loadImbalance`) are for operators and are documented in
  [scaling policy](../reference/scaling-policy.md). A switch never reads a
  policy key, and no policy key turns a switch off. A PR that moves a
  threshold an operator key sets changes the key's default or meaning, not a
  constant: see [4.3](#43-what-the-tracker-owns).
- **The committed goldens are the all-on run.**
- **The replay runs all off, each switch on alone, and all on.** "All off"
  means **this PR's** switches off. A seam an earlier PR left in the package
  stays at its constructor default (on), because the base goldens are the
  base's all-on run.

| run | must hold |
| --- | --- |
| all off | identical to the **base commit's** goldens (`git show $BASE_SHA:<golden>`), every cycle of every output, for the whole recording. A divergence is a bug, and the PR does not merge with one |
| each on alone | the replay reports, per output and per key, the first diverging cycle, the number of changed rows, and the sign and size of the change. Only the outputs that change names may move |
| all on | equals the goldens the PR commits. An output that moves here and in no single-switch run is an interaction, and the PR names it |

- **A new recording lands with goldens generated at the base,** in its own
  commit before the change, so the all-off run has something to compare
  against.
- **A seam is deleted,** with zero golden diff, by the next PR that touches the
  same package, or by a cleanup PR. It never rides in an unrelated learner's
  diff.

The replay is open loop: recorded inputs do not depend on outputs, so later
cycles stay comparable after a divergence. What the fleet would then have done
is the closed-loop probe's question.

**Goldens change only with a named change.** The PR body has a `goldens:`
section listing each output it expects to move and in which direction, and
pastes the replay's per-output summary (never the CSV). CI fails when a golden
file changes and that section is missing, or when an output moves that the
section does not name. The check also runs on the PR's `edited` event, so
correcting the `goldens:` section re-runs it. Goldens are written only by the
`make` target, never by hand.

**Per-path counters must be above zero.** P1-obs admitted, mu samples written,
mu yields (served, sibling, stale), OLS cycles, pinned cycles, instant-KV
rows, input rows with a known ReadySince, and from PR 1 on the settle
tracker's verdicts per reason. A replay that touched none of a PR's paths
fails rather than passes. A counter counts only where its path can run on the
PR's base: a counter that is zero by construction there is not coverage, and is
required from the PR that makes it non-zero. Joined refusals with a known
ReadySince are not such a counter: the collector reports ReadySince, and k2 and
mu read it through `noteReady` since #288, so it is required from 0b.

**The closed-loop probe** is new work, not a commit of an existing test. The
probes #282's model reviews wrote hold the fleet fixed: they compute the
implied replicas and never add one. A closed-loop probe needs a fleet
simulator: the order becomes replicas after the start time S, a new replica
is Ready after the settle, load splits equally across Ready replicas or by a
recorded imbalance, and the queue grows by λ minus what was served. It lands
in **PR 3**, the first PR that needs it, as `D/closed_loop_probe_test.go`,
with one known-failing case as its negative control (one slow pod's confirmed
low rate, #282 round 3 H1). Holds, latches, the sibling yield and the
self-sealing low mu are closed-loop behaviours; a PR that changes one runs the
probe and pastes the result. PRs 3, 4a, 4b and 5 must; 4a runs it through a
controller restart ([4.4](#44-the-cost-after-a-controller-restart)).

### 2.6 Property tests

Each class is a synthetic case, run against every learner it applies to.
Each learner **declares its estimand**: the floor's is the arithmetic mean of
mu_i (fleet capacity is their sum), A's the k-weighted ratio, k2's per
replica. A class has no target without it.

**Time base is an input family, not a class.** Inputs come from a simulated
per-second path, then pass through rate[1m], max_over_time[1m] and instant
sampling at a 15 s cycle; consecutive one-minute windows share 45 s. Every
class below runs on it as well as on direct inputs.

| class | what it asserts |
| --- | --- |
| Transient | a joiner, a load step, a slow fill and a filling token rate are each refused or stamped, per event |
| First sample | one reading does not set a figure that a second, identifying reading would move by more than the band |
| Stale value | from identifying data **at N ≥ 2**, the figure converges (catches A3: B stuck at 6 ms) |
| Missing and duplicated values | a missing gauge (presence false), a single missed row, and two rows for one pod each leave the figure where it was. A series doubled inside PromQL reaches the analyzer as one value at 2x: that is the heterogeneity case here, and a collector test |
| Contamination **paired with** heterogeneity | a **state-flagged** contaminant is dropped, and a genuinely slower replica with no flag moves the estimate by its weight under the declared estimand. A learner using a value estimator allowed by [section 5](#5-fits-exclude-by-state-then-fit-untrimmed) (half the served max, `siblingMuTolerance` of the siblings' lower median) runs the heterogeneity case with the deviation it declares, on the side it declares. Where that side is the risky one (half the served max; the sibling yield), the learner declares the deviation at which it starts to over-price, and the case goes into the known-failures ledger with PR 3 as its owner. Mu's fold across pods is the mean (#282), the declared estimand, so one slow pod at N in {4, 16} (for example {1, 1, 1, 0.5}) is a **passing** case: 0b commits it as a guard, not a ledger row |
| Redistribution invariance | the same fleet load moved between replicas leaves fleet demand and learned figures within a declared tolerance |
| N-dependence | under iid noise, an estimate's expectation does not depend on N in {1, 2, 4, 16} beyond a declared bias (catches the max across replicas, +13.6 % at 16) |
| Known truth | with a true mu and k2, the priced capacity is at most truth × (1 + ε) under every family, idle replicas included. At the documented target the steady order equals λ / (mu · target) |
| Scale | block size, GPUs per replica, output length and cycle interval varied one at a time: priced replicas are invariant, and each figure scales by its unit |
| Liveness | from clean identifying data each learner reaches truth within a stated time, **per topology**: N ≥ 2, P/D, flow control on and off, 8-GPU DP ranks |
| Monotonicity | along a trajectory, not only between two static inputs: demand does not fall as lambda or the queue rises, and stays bounded by what the fleet can drain |
| Edges by enumeration | the operating point is put on **each** threshold below, with noise of the gauges' real autocorrelation. Between declared latched switches the output is Lipschitz in the input, and it reverses at most once per excursion, an excursion being one crossing of the threshold by the **noise-free** generator path |

**Known-failures ledger.** Several classes fail on today's code by design: known
truth on the time-base family (A5, until #393 cleared it), first sample on k2 and
start time, N-dependence on the prefill bound's max, and edges on the k2 key
boundary. 0b commits a ledger of (class, learner, finding,
measured result, the PR that clears it or "open"). A listed case runs and must
still fail; a listed case that starts passing fails the build until its row is
removed. A PR that fixes one removes its row. Nothing is skipped and no
tolerance is widened to pass a defect.

**Each mapping row is demonstrated.** For each row of
[2.7](#27-past-findings-against-the-test-classes), 0b includes a mutant that
reintroduces the finding: the pre-fix commit's function, or a toy learner with
the defect. The class must fail on the mutant, and the PR pastes the failure.
A class with no failing mutant does not count as catching anything.

Thresholds the enumeration covers (each PR adds any it introduces). Where a
threshold is an operator policy key, the enumeration runs at its default and at
least one non-default value an operator may set (for example `idleRows: 5`,
`steadyFor: 10m`, `idleAfterReady: 5m`); property tests on defaults alone do
not cover a key.

| learner | thresholds |
| --- | --- |
| ITL | OLS at 10 rows and spread 0.30; slope held under `minSettledSamples`; `steadyFor`; `idleRows`; `carry`; the KV band edges; the sequence cap and `itlBand.capMaxPreemptionRate`; `steadyGate.maxKDrift` (0.10); 20 / 40 rows; 30 min window |
| k2 | queue threshold; joined 1 min; `rateFraction` of mu and of k2; `maxRise` at 1 min back; window 10; episode gap 24 h; eviction 7 d; the key boundaries |
| borrowed k2 | 3 % KV; 30 min hold; 6 h relaxation; 20 % re-arm |
| mu | half of served; `minMeasuredOfDerived`; 2 min confirm; `maxTokensOverCompletions`; `siblingMuTolerance`; the sibling yield's "full cache" line; the siblings' lower median's 50 % mass point; 2x stale agreement; `freshFor` / `lowHoldFor`; latch `maxRatio` / `returnRatio` |
| floor | the trust blend between `floorTrust.lagFraction` (0.5) and `floorTrust.trustFraction` (0.8); growth band max(`backlogGrowth.minRequests` (2), `backlogGrowth.fraction` (0.10) of the backlog) over 1 min; the standing-queue cap |
| shape, idle, decode memory | shape tolerance band and 5 min hold; idle 2 min with 1 min gap; the 0.30 idle line (`loadImbalance.idleKvUsage`, saturation's idle test: `D/roles.go:123`, `D/idle_room.go:88`); `loadImbalance.idleAfterReady`; decode memory 1 min |
| start time | 3x outlier, 3 confirmations, 0–3600 s, α 0.3 |
| share (#14, #15) | step spread 0.25; p90 at ≥ 3 releases (listed for later; see 2.7) |

### 2.7 Past findings against the test classes

| finding | caught by |
| --- | --- |
| GPS ratio first raw; pinned B from unsteady readings; k2 at 8 % behind a step | first sample, transient |
| Full fit never runs at N ≥ 2, B stays 6 ms (#273 A3) | stale value at N ≥ 2 |
| k == 0 unsettles; router queue without engine metrics | missing values |
| Instant k against a rate[1m] ITL (#273 A5, caught since #393) | known truth, on the time-base family |
| Sum of per-pod one-minute peaks | redistribution invariance, on the time-base family |
| Settle refuses one cycle of a fill (#282 F1) | transient, on the time-base family |
| k2 key flips on the output-length boundary; mu key flips on the output-length boundary; measured/derived flips near 2x; the 0.30 idle line; `backlogGrew` | edges by enumeration (the operating point on the boundary, or the ratio built at 2) |
| Headroom counted twice (+17.6 %); idle room counted twice; the rules together always pick the higher mu | known truth |
| KV and throughput units mixed | scale |
| Max across replicas (+13.6 % at 16) | N-dependence |
| Prefill double scrape | heterogeneity at the analyzer (one value at 2x); the doubling itself is a collector test |
| Demand falls when λ lags a step; local queue priced as drainable | monotonicity along a trajectory |
| Weighted median flapping between modes | edges, on a **bimodal** fleet |
| IQM breaks at 25 % contamination | **flagged** contamination swept to 50 %; unflagged contamination only up to a section-5 estimator's declared breakdown, so the sweep does not reward the median |
| A genuinely slow replica trimmed away (#273 r3, #282 r3) | contamination paired with heterogeneity (the old "one reading dominates" test rewarded the trim) |
| No floor under flow control; prefill never learns (#144) | liveness per topology |
| Imbalance audit (load moved between replicas) | redistribution invariance |
| Settle holes: fall, rate escape, restart, idle return, lend | transient, one case per event |
| Restart fix stubbed | per-path counter "joined refusals with a known ReadySince", required from 0b (the Ready time is on since #288) |
| "For good" self-sealing low mu | closed-loop probe (PR 3) |
| k2 adopts its first reading (#392) | first sample |
| Speculative decoding prices the ITL step as a token (#274) | known truth |
| KV per request assumes one output length (#276) | known truth |

**Where each row stands after 0b.** The code table is `findings` in
`D/property_mutants_test.go`, and a test holds it to the mutants. Each
finding above is in exactly one state:

- **Caught:** 30 findings. For each, a mutant puts the defect back, and its
  class fails on that mutant. For 21 a mutant runs the production learner
  (a policy key, rows, or a stub); 9 are caught only by a toy, and the code
  table marks them "caught (toy)". "Restart fix stubbed" is caught: its
  mutant stubs the Ready time through the analyzer's `readySince` seam, the
  restart case fails on it, and the per-path counter "joined refusals with a
  known ReadySince" drops to zero. "Prefill never learns (#144)" is caught
  too: behind a full decode the prefill bound learns a mu within 3 min, and
  with `enablePrefillThroughputBound` off, its default, prefill learns
  none. "Instant k against a rate[1m] ITL (#273 A5)" is caught since #393:
  the ITL line pairs each rate[1m] ITL with the KV fill averaged over the
  same minute, and a rows mutant that withholds that average fails known
  truth on the time base on 15 of 100 seeds. "k2 key flips on the
  output-length boundary" and "The 0.30 idle line" are caught since #396:
  a stub mutant turns each band's replay switch off, and its edge case
  fails (13 of 50 seeds, and 98 of 100). "k2 adopts its first reading" is caught since
  #392: a stub mutant turns the hold's replay switch off, and
  first-sample/k2/second-reading fails (20 of 20 seeds). "Speculative
  decoding prices the ITL step as a token" is caught since #274: ten vLLM
  replicas each accepting 0.5-2 draft tokens a step read ITL per step, and
  with the correction's replay switch off
  known-truth/itl/speculative-accepted-tokens fails (20 of 20 seeds).  "KV per request
  assumes one output length" is caught since #276: a bimodal workload with
  lengths on vLLM's bucket bounds, read through the histogram's bound on
  E[O^2], and with the spread's replay switch off known-truth/k2/output-spread
  and known-truth/mu/output-spread each fail (19 of 20 seeds). The replay counts that counter and the others of 2.5, and the golden
  check fails when a counter is zero over every committed golden.
- **Still escaping:** the rest, listed below. A row is escaping either because
  no mutant reproduces it, or because today's code already fails its class,
  so the defect is a known-failures ledger row (`D/testdata/properties/known-failures.yaml`)
  rather than something a mutant can put back.

**Still escaping:**

| finding | why |
| --- | --- |
| k == 0 unsettles (#273 A4) | a settle-tracker defect; its negative control is PR 1's (2.3) |
| Router queue without engine metrics | the missing-values case passes, and no row edit reproduces the pre-fix reading |
| Demand falls when λ lags a step | the trajectory passes; no pre-fix floor is reproduced |
| No floor under flow control (#144) | a ledger row for k2; no case drives mu or the floor under flow control |
| Settle holes: fall, rate escape, idle return, lend | need the settle tracker (PR 1) |
| "For good" self-sealing low mu | the closed-loop probe (PR 3) |
| Share swing and cancel (#272 F1/F2 and the d3 reviews) | the share ledger (#14, #15) is not recorded or replayed. Out of scope for PR 0 |
| Alert debounce and sustain (#283 M2/M3) | not a learner |
| Shape dependence of the ITL line (#275) | no generator models it |
| A self-sealing error in a PR that does not touch a hold | the probe runs only when a PR names a closed-loop change |

### 2.8 Recordings each PR needs

None exists yet: no recorder has run. No recorded run has ever reached OLS.
#283 and #288 have merged, so a run recorded on main carries ReadySince; none
of the older runs does.

| PR | recording needed | counters that must be above zero | synthetic stand-in |
| --- | --- | --- | --- |
| 0a | none: unit tests and negative controls | — | — |
| 0b | the E1 scenario re-run with the recorder on, from a build with 0a merged; and one recording, or a replay of it, under a non-default `analyzerTuning` (at least `idleRows`, `steadyFor` and `loadImbalance.idleAfterReady` off their defaults). 0b's goldens commit with them | instant-KV rows, input rows with a known ReadySince, joined refusals with a known ReadySince | — |
| 1 | the 0b recordings, the non-default one included | the settle tracker's verdicts per reason | the settle suite (section 8); the balance tracker's unit tests, a re-lend within 1 min among them |
| 2 | flow control off; a P/D run; a pod restarted under the same name | P1-obs admitted, settle refusals per reason, joined refusals with a known ReadySince | restart suite: same-name restart, LWS group with mixed Ready times, bridge lend, node clock 90 s behind, controller restart. 8-GPU DP: `D/dp_ranks_test.go` |
| 3 | flow control off (mu is written only beside P1-obs) | mu samples written, mu yields | closed-loop probe (lands here) |
| 4a | a run that reaches OLS: stepped concurrency giving ≥ 10 steady rows spread ≥ 0.30, with the SLO analyzer recorded | OLS cycles, pinned cycles | restart suite (PR 2) and the settle suite's one-missing-row case, both through ITL, for its two named changes; the closed-loop probe through a controller restart; a replica at its sequence cap, admitted above the band |
| 4b | a throughput-analyzer run with its recorder on: the `ols-sweep` run with throughput listed and a controller restart mid-run, two recordings ([PR 4b](#pr-4b-the-throughput-analyzers-itl-line)) | its OLS and pinned cycles; settling readings; held cycles | negative control on the parent; a non-default policy band; a replica at its sequence cap, for the cap admission; the slope hold through 30 min of light load |
| 5 | a controller restart with the snapshot on | restored keys, start samples | restart suite plus restore-then-run; a restore under light load that keeps the ITL line; v0.10.0 and v0.10.1 v1 documents |

LWS and SGLang have no recording. Their synthetic suites stand in until one
exists, and the PR says so.

### 2.9 Recorder fields for TTFT, and capture

[What-if capacity](slo-capacity-planner.md) and TTFT pricing in the SLO analyzer
([SLO tiers](slo-tiers.md#phase-2-ttft), phase 2) both learn TTFT from
recordings, and today's recordings carry too little of it. Both PRs here land
before any recording either of them depends on is made.

**What is recorded today.** Every `Analyze` event's `learnrec.Input` carries,
per pod, `AvgTTFT`, `AvgServiceTime`, `PrefillComputedTokenRate`,
`QueueLength`, the length means and the KV fields, and per model
`SchedulerQueue` (flow-control queue size and bytes) and `ArrivalRate`.
`slo.Analyze`'s `AnalyzeEvent.RouterLatency` carries the router's TTFT
histogram as per-second bucket rates (streaming only, 5-minute window) and the
TPOT sum and count rates. It is present only when the SLO analyzer is listed,
the policy has an SLO (`slo.Empty()` is false) and it judged (`RouterCalled`).

**R1: collect and record**, each field behind a presence bit, so a recording
made before R1 replays unchanged:

| Level | Field | vLLM source (SGLang) |
| --- | --- | --- |
| Per pod | Presence bits for `AvgTTFT`, `AvgServiceTime`, `QueueLength`, which have none today | — |
| Per pod | Prompt- and output-length histograms | `request_prompt_tokens_bucket`, `request_generation_tokens_bucket` (`prompt_tokens_histogram`, `generation_tokens_histogram`) |
| Per pod | Engine TTFT histogram | `time_to_first_token_seconds_bucket` (same name) |
| Per pod | Queue-time mean and histogram | `request_queue_time_seconds` (`queue_time_seconds`) |
| Per pod | Prefill-time mean | `request_prefill_time_seconds` (vLLM only) |
| Per pod | KV transfer time mean | `nixl_xfer_time_seconds` (vLLM only) |
| Per model | Router TTFT and TPOT every cycle | a new `AnalyzerInput.RouterLatency`, collected by the engine every cycle while recording is on |
| Per model | Router per-request TPOT histogram | `llm_d_epp_request_streaming_tpot_seconds_bucket`, as bucket rates beside the TTFT ones. `RouterLatency` keeps only the TPOT `_sum` and `_count` rates today, and the collector queries only those |
| Per pod | Engine per-request TPOT histogram | `vllm:request_time_per_output_token_seconds_bucket`, the fallback source for the TPOT ratio (vLLM only) |
| Per pod | Queue minimum over the minute, `QueueMin` with `QueueMinKnown`: above zero, the engine had work waiting the whole minute (the busy minutes TTFT P1 reads its `V_P` from) | `min_over_time(num_requests_waiting[1m])` (`num_queue_reqs`). Each rank's least is summed over the pod's ranks, as the queue length sums their peaks: a floor under the pod's own least, and tighter than the largest rank's |
| Per model | Flow-control wait histogram, dispatched requests only (`outcome="Dispatched"`): only a dispatched request's wait is part of a TTFT | `flow_control_request_queue_duration_seconds`, which carries `model_name` and `target_model_name` as well as the pool, so it is read per model with the router latency (`RouterLatency.FlowControlWaitBuckets`), not through the pool join |
| Per pool | Pool saturation by stage, outcomes | `flow_control_pool_saturation` (stages `prefill`, `decode` and `effective`, the larger of the two, which gates dispatch; a pool with no endpoint reads 1.0) and `flow_control_requests_total`. They carry `inference_pool`, not a model, so they are recorded per pool and joined to a model only when the pool serves one |

**Cost.** Every query above runs only while a learner recording is on: the
engine tells the collector at the start of each cycle whether its recorder
is writing (`Engine.beginCollectorCycle`, collector `SetRecordTTFTInputs`),
and with recording off a cycle issues exactly the queries it issued before
R1, the slo analyzer's router queries included, for the models that list it.
With recording on, a namespace of vLLM models costs up to 16 more queries
per cycle: 8 per pod, 5 router queries (the slo analyzer's 3, for models
that do not list it, and the 2 histograms) and 3 per-pool queries. An SGLang
or mixed namespace adds the SGLang variants of the 5 engine-specific per-pod
queries. Each runs once per namespace, shared through the cycle memo, and a
failed one is counted once, under `query_type="learner_recording"`.

`learnrec.Input` mirrors `domain.AnalyzerInput`, so the router field is new on
both. Replay keeps serving `AnalyzeEvent.RouterLatency` to the SLO analyzer, for
fidelity; the new field is read only by the planner and by the SLO analyzer's
TTFT pricing. Histograms are bucket rates over the collector's window, as
`RouterLatency.TTFTBuckets` already is. They hold numbers only, so the export
needs no new pseudonymisation rule, and its leak and coverage tests
(`learnreplay/coverage_test.go`, `export_leak*_test.go`) are extended. The
goldens are regenerated with `learnergolden update`: values unchanged, new
fields absent. No switch: nothing reads the fields in R1. R1 also updates the
comment on `constants.VLLMRequestPrefillTimeSeconds`, which then becomes
collected.

**Later: write each cycle's input once.** Each `Analyze` event carries its
whole input, so a model listing three analyzers writes its rows three times
a cycle. With R1's fields that is an estimated 4 KB more per replica per
`Analyze`, about 3.3 times the measured 1.7 KB: at a 15 s cycle with 100
replicas and three analyzers listed, the 256Mi cap fills in about 40 minutes
instead of 2.1 hours. A later PR writes each model's cycle input once, as its
own event, and has each `Analyze` event name that event's sequence number
instead of carrying a copy; replay resolves the reference. That brings the
cost back to one input per model per cycle, whatever the policy lists, and
needs no change to what a replay reads.

**R2: `whatif capture` and a `Generate` replay mode.**

```
whatif capture --prometheus URL --namespace NS (--kube-state-metrics | --role-of ROLE=SELECTOR ...) \
    --from T0 --to T1 [--step 15s] [--policy FILE] -o run.jsonl.gz
```

A file of inputs alone cannot be replayed: `Replay` needs a `start` line, and
both of its modes apply the engine's own events, per analyzer, with the store
records each one read. So capture writes **cycle inputs** (one per model per
step, a `learnrec.Input` built by `internal/whatifsource` from the collector's
query templates; R2 creates that package, and the planner's standalone mode
extends it). A new third mode, **`Generate`**, writes the `start` line
(`learnrec.Start`: the saturation horizons including `BorrowTickGap`, the SLO
configuration and `Judging`, `Throughput`, from the policy file or the
defaults), then plays each cycle in the order a cycle calls them
(`engine.go`, `engine_v2.go`):

1. `slo.BeginCycle`, called by the cycle before `optimizeV2`
   (`engine.go:881`); the SLO analyzer counts its judging windows by it;
2. `NoteVariantsPresent` on saturation and slo, including parked variants when
   the capture can see them (`engine_v2.go:482-514`, `:519` onwards);
3. the three sweeps, saturation's `EvictStaleHistory`, the store's
   `EvictStale` and slo's `EvictStale` (`engine.go:1478-1479`,
   `engine_v2.go:561-616`), before anything stamps state this cycle;

then, **per model**:

4. `observeSettle` (`engine_v2.go:187`; "the settle tracker first"), or
   `holdSettle` for a model with no rows or no policy;
5. per variant, in key order, `StableAcceleratorFor` and then
   `FileScaleTarget` through `LoadFromScaleTarget` (`engine_v2.go:74-101`,
   reached from `buildAnalyzerInput` at `:199`). `forgetOnRequest` is left out:
   a capture has no annotation history, so it writes no `ForgetVariant` events;
6. `Analyze`: saturation first (`:203`), then the listed analyzers. With slo
   listed, its router source serves the captured router latency.

The variant states come from the role map, and the policy from `--policy` or the
defaults. The output is an ordinary recording that carries the commit of the
build that generated it, so `learnergolden add` (which replays at the recorded
commit) accepts it.

**Roles and readiness for a past window** cannot come from a kubeconfig, which
sees today's pods: a scaled-out run's pods are gone. They come from
kube-state-metrics in the same Prometheus, with three limits:

- `kube_pod_labels` exports no `label_*` by default since kube-state-metrics
  v2; the role and variant labels need
  `--metric-labels-allowlist=pods=[...]`. Without them, `--role-of` selectors on
  pod names give the role map, flagged as inferred; capture does not refuse.
- `kube_pod_status_ready` and `kube_pod_status_ready_time` give readiness and
  `ReadySince`; `kube_deployment_spec_replicas` gives replica counts for
  Deployments only. A LeaderWorkerSet needs a custom-resource-state
  configuration, or `--role-of`.
- **Engine parameters are not in kube-state-metrics.** `FileScaleTarget` parses
  the container's arguments (batch cap, `max-num-batched-tokens`, ranks), which
  it does not export; the `llm-d.ai/max-num-seqs` annotation appears only
  through `kube_deployment_annotations` with an allowlist. Only the GPU count
  (`kube_pod_container_resource_requests`) and the accelerator (`kube_pod_info`
  joined to `kube_node_labels`) are derivable. The rest come from flags
  (`--batch-cap`, ...) or the current Deployment, flagged when it may differ
  from the window's. The output is pseudonymised by the
same export as `learnergolden export`. A capture needs Prometheus to have kept
the series for the window; `_bucket` series are often kept for days, not weeks,
so a window worth keeping is preserved first as a Prometheus snapshot (the TSDB
snapshot API, or a `promtool tsdb` dump) and captured from it later.

**What `ols-sweep-slo` gives this work**: flow control off, the SLO analyzer
listed, streamed load; router TTFT, and per pod `AvgTTFT`, `AvgServiceTime`,
`QueueLength` and `PrefillComputedTokenRate`. It lacks the histograms, queue and
prefill time, transfer time, any flow-control series and fixed replicas. A
capture of its window adds the histograms if Prometheus still holds them.

## 3. A shared vocabulary, not a shared loop

### 3.1 The terms

Docs, logs and reviews describe every learner in the same steps: **observe**,
**admit** or refuse, **fit**, **predict** with a source and an age,
**validate** against a witness, then **keep, refit, forget or hold**. No code
drives learners through them. The learners read each other mid-cycle in a
fixed order, and a generic loop would change it.

### 3.2 The order the learners depend on

`Analyze` runs eight stages: `installRestored` → `observeFleetShape` →
`resolvePricing` → `fitLines` → `priceReplicas` (once per replica) →
`settleShape` → `priceDemand` → `applyFloorAndHolds`.

| edge | what reads what | which value |
| --- | --- | --- |
| restore → fit | `installRestored` installs windows and P2-prior bounds | before `fitLines` |
| shape → k2 key | the shape tracker sets the output length that keys k2 (row 3) | before pricing |
| ITL → derived mu → mu admission | mu's 0.5-of-derived test reads the line | this cycle's line (fitted first) |
| k2 → mu observation | a mu sample is written only beside a P1-obs k2 write | the same replica, same cycle |
| mu → k2 settle | k2's rate path reads mu on record | as that replica is priced, earlier replicas of this cycle included |
| k2 window → later replicas | each replica's P1-obs writes the window, the next replica is priced from it | then the median across replicas |
| served → mu choice | `servedBounds` (max and full-sibling median) | this cycle's rows, before pricing |
| mu choice in the floor | measured, derived or siblings, picked after every replica is priced | this cycle's figures |
| settle shape → holds | `settleShape` clears `shapeChanged` | read by `applyFloorAndHolds` |

Edges that read the **previous** cycle. A reorder breaks these silently:

| state | read by |
| --- | --- |
| mu-choice latch and sibling memory (`muChoice`) | the next cycle's mu choice |
| decode-saturation memory (#9) | prefill learning |
| last cycle's backlog and `backlogGrew` (#11) | the floor's growth test and the full queue charge |
| hot-rank memo | the hot-rank hold |

A PR that moves a learner keeps this order, or names the change.

### 3.3 Admission shapes

Admission is not a yes or no per reading:

- **ITL** is three-way: main and steady windows, main only (unsteady or
  settling), or refused. Every reading updates the replica's trace either way.
- **Mu** refuses a low reading, then accepts it once the same pod has repeated
  it for 2 min. Within the one-minute spacing a reading rewrites the last sample
  instead of adding one. "Admitted" and "became a sample" are different events.
- **k2's** settle test applies only when the queue overflows the rank, has two
  alternative tests, and one refusal reads another role's state.

So each learner counts its own outcomes. The shared code formats and exports
the counts; it does not call a per-reading admit.

### 3.4 Which side breaks the SLO

Each learner declares which side of its error breaks the SLO. **Any move toward
that side that is not driven by a new admitted reading needs a reason in this
table**, inside a fit or out: carries, `RaiseLast`, defaults, stashes,
restores, exact repeats and unknown ages. Two rules apply to all:

- An unknown age resolves to the safe side. (`measuredYieldsToDerived`
  treated a missing `SampledAt` as "as stale as can be", which skipped the
  30 min low hold. That was the risky move; PR 3 fixed it,
  `mu-unknown-age-resolves-safe`.)
- An exact repeat of a float (`rate == lastRead`) is evidence of use, not of
  saturation: it is the signature of a frozen series. Mu counted it as a
  write and kept its episode alive; PR 3 fixed it, `mu-exact-repeat-is-use`.

| learner | side that breaks the SLO | moves toward it without new evidence, today |
| --- | --- | --- |
| k2 | too high | none. The window mean rises on admitted readings only. A too-low k2 is **permanent** on a serving variant: touched on every read, never evicted, and its episode resets only on a write, which needs the saturation a low k2 prevents. The only exit is `llm-d.ai/forget-learned-state`; the runbook says so |
| borrowed k2 | too high | the upward relaxation over 6 h of serving time |
| measured mu | too high | the stale yield to a higher derived figure after 30 min (5 min toward a lower one). The restart that drops the latch (see [the mu choice](#the-mu-choice); restored to the safe side since PR 3). An unknown age (above; safe side since PR 3). An exact repeat: since PR 3 it refreshes freshness only under the derived figure, where losing it would let a repeating low rate yield to the higher figure, and not over it, where staleness yields to the lower one. Backed by new evidence, so allowed: the **immediate** yield when a Ready replica serves more than 2x it, and the sibling yield |
| ITL line and derived mu | ITL(kPrice) too low, that is, derived mu too high (with B pinned low, A comes out too high and the prediction is still low) | the 6 ms default B or a stashed B. With B pinned below the true intercept, the pinned A absorbs the error and the prediction at kPrice is low by (B_true − B_pin)(1 − kPrice / k̄): **risky whenever the fleet runs above its pricing point**, the under-provisioned fleet that most needs a correct line |
| prefill bound | too high | its window max only rises, survives the episode reset through `RaiseLast`, and never ages while read. Because `RaiseLast` carries the max past every reset, it is a running max over the bound's life: about 1.5σ above the mean after 10 readings, about 3.2σ after a day at one a minute. Unreviewed |
| start time | **both** | too low under-orders a fleet that is behind (λ > mu · ready). Too high, in `internal/signals/floor/floor.go`: `DrainHorizon` under-prices the backlog whenever S exceeds 60 s, `startingCredit` credits a stuck pod for up to 2S, and a fleet with spare projects more drain and orders less. Share pacing errs the other way: too low moves GPUs for bursts that end first. PR 5 bounds both sides |
| share need history (#14) | too low for the role, too high for its neighbours | `PeakNeed` is a max of adjacent-pair minima, low by design; trimming to the swing window ages a peak out with no new evidence |
| share release time (#15) | too low | p90 over ≥ 3 releases is the max at n = 3 |

A clamp that stops k2's fit from rising would turn it into a running minimum:
one admitted low reading would then price the fleet for good.

### 3.5 Hysteresis

Every threshold that switches a source, a **key** (the k2 key boundary on
output length, which swung capacity 39 % at constant load), a role verdict or
a demand term declares:

| item | rule |
| --- | --- |
| set, clear, dwell | three values, stated in the code comment and the developer guide |
| safe side | which side is safe. The clear direction is never faster toward the risky side. For a two-sided learner (start time) set and clear are symmetric, and the dwell is at least the learner's own noise correlation time |
| restart | an unpersisted latch restores to the safe-side choice. The mu latch does since PR 3 ([the mu choice](#the-mu-choice)) |
| tuning | dwell is tuned on noise with the gauges' real autocorrelation, not iid noise, which over-counts flips |

### 3.6 Clocks

The clock fix itself is [2.2](#22-one-clock). The clocks in use:

| clock | used by |
| --- | --- |
| **write** | ITL line age, the shared line |
| **use** | k2 and mu eviction, 7 d after the last read; the prefill bound's `Touch` |
| **write gap** | k2's and mu's new episode after 24 h without a write, on the same window as the use clock. The prefill bound defeats it (`RaiseLast`) |
| **agreement** | mu freshness: the last saturated reading within 2x. Refused readings set it too. An exact repeat sets it only while the measurement is under the derived figure (PR 3) |
| **serving** | the borrowed k2 relaxation, and the hold clocks that only a serving row advances |
| **existence** | `VariantTimeout` (last reported or found to exist), the ITL sweep at 24 h unseen, `evictStartSeenPods` |
| **since event** | the settle tracker: time since join, idle return, restart or lend |
| **cycle count** | start-time outliers, #273's `idleRows`, `PeakNeed`'s two in a row |

An age shown to an operator is the age of the last **admitted reading**, never
of the last use. A use-touched k2 otherwise reads young however old its
evidence is. Mu publishes its last admitted time; its freshness (agreement) is
a different clock and is logged, not published as an age.

## 4. Settled replicas

### 4.1 Four rules today

| | ITL steady trace (#273) | k2 settle (#282 `D/k2_settle.go`) | `noteReady` (#282, #288 `D/throughput_admission.go:176-210`) | balance tracker (#283 `internal/signals/balance/tracker.go`) |
| --- | --- | --- | --- | --- |
| joined wait | `steadyGate.steadyFor` (2 min) | 1 min lookback, queue standing | 1 min | `loadImbalance.idleAfterReady` (2 min) |
| ready time | first sight | later of Pod Ready and first sight of that Ready time, through `noteReady` (`k2_settle.go:197`) | later of Pod Ready (`replicaReadySince`, from `ReplicaMetrics.ReadySince`) and first sight of that Ready time | later of Pod Ready and first sight of that Ready time |
| restart events | joined, load back after idle (`steadyGate.idleRows` rows with no latency and no KV fill) | joined, a new Ready time under the same name | joined, a new Ready time under the same name | joined, warm-pool lend, any absence |
| key includes variant and bridge | no | no | no | yes: `(pod, variant, bridge)` |
| a missing row | restarts the settle (before PR 4a; kept up to the tracker's tolerance after it) | kept up to `K2SettleMaxGap` | kept with k2's trace; a row that is not Ready forgets it | forgotten (`tracker.go:117-141`) |
| first cycle after a controller restart | taken as settled (before PR 4a; waits `steadyFor` from first sight after it) | taken as established, unless the Pod's Ready time is under 1 min old | taken as established, unless the Pod's Ready time is under 1 min old | waits again |

### 4.2 One owner

One tracker in `internal/signals/settle` replaces them.

| question | answer |
| --- | --- |
| owner | **the engine**, one tracker per model, swept with the model. It observes this cycle's rows into the tracker at the top of `runAnalyzers`, before `observeLoadBalance` (`engine_v2.go:156`) and before any analyzer. An analyzer-owned tracker would be written after the balance tracker reads it (`observeLoadBalance` runs before `runV2AnalysisOnly`), one cycle late on every join |
| readers | the balance tracker (#283), the saturation, SLO and throughput analyzers, each through a **read-only view**. None writes. The engine builds the saturation and SLO analyzers (`steadystate/engine.go:335, 405`) and passes the view at construction. The throughput analyzer is built in `cmd/main.go:918` and added by `RegisterAnalyzer` after the engine exists, so it takes the view at registration through an optional interface (`settleViewTaker`), as the engine's other capabilities are reached |
| replay | the tracker is a function of the rows and the engine's clock read only. The engine's observation is an engine-side event in the stream ([2.4](#24-the-recorder)): the model and the `now` the engine read, which is earlier than the `a.now()` each analyzer reads inside `Analyze`. Replay applies it as the engine did, at that instant, with the next `Analyze` event's rows. It reads nothing from the store, so a rollout is detected from the rows (4.3) |
| concurrency | the tracker holds its own lock; one writer per model per cycle, any number of readers |
| key | (model, pod, variant, bridge). A warm-pool lend from the pool keeps the pool's Ready time but changes variant and bridge, so it is a new key and a new settle |
| prerequisite | #283 (merged) |

**Difference to reconcile with #283.** The balance tracker rebuilds its
sightings from this cycle's rows, so one missing row forgets a replica
(`tracker.go:117-141`). That forget is load-bearing: its comment says a
warm-pool Pod lent again "starts its grace again". The shared tracker keeps a
sighting across an absence up to one rule period (1 min; at an optimize
interval over 24 s, one missed cycle and half a cycle of slack, 2.5
intervals, so a long interval does not make every cycle an absence, PR 1): a
single missing row
is a missed scrape, and restarting a busy replica's settle for k2, mu and ITL
would re-open #273's A4. Under that rule a Pod lent again to the **same**
variant inside a minute keeps its key, (pod, variant, bridge = true), and so
would keep its grace. Not verified on a run: whether a re-lend keeps the key,
and whether ReadySince changes on a re-lend. PR 1 checks both, and keeps a
re-lend a new settle by an explicit lend event, not by absence.

When the balance tracker moves onto the view in PR 1, its imbalance timers
take the 1 min rule. That is a named change for #283's metrics and for the
`WVALoadImbalanced` alert, whose description says "Ready for at least 2
minutes" (`loadImbalance.idleAfterReady`). PR 1 updates the alert text and the
idle row in `docs/reference/prometheus.md` if their meaning moves. It is outside
the analyzer replay (the engine owns the timers and publishes them as metrics),
so PR 1 proves it with unit tests on the balance tracker: one missing row keeps
the timer, two rows past the period restart it, and a Pod lent again to the
same variant within 1 min restarts its grace. The first two fail on the parent;
the third passes on the parent and must keep passing.

### 4.3 What the tracker owns

- **A stamp per reading, taken when observed.** The learner stores it with the
  reading, because a window outlives the settle.
- **The events that start a settle:** a replica joins, load returns after idle,
  the engine restarts (an engine or LWS group restart under the same name gives
  a new Ready time), a warm-pool lend, a rollout. A rollout is detected from
  the rows (a pod with a new `Revision`, or a new pod), not from
  `capacity.Record.RollingOut`: `LoadFromScaleTarget` files that bit later in
  the cycle than the engine observes the tracker. An LWS group's row is its
  leader's, and its Ready time is the latest over the leader and its live,
  Ready workers (`groupReadyTimes` in
  `internal/collector/pod_serving_state.go`), so a group restart (#361) and a
  worker restarted alone under `restartPolicy: None` both give a new Ready
  time (the "PR 2's decisions" note at the top).
- **The ready time:** the later of the Pod's Ready time and the controller's
  first sight of it, as the balance tracker (#283) and `noteReady` (#288) do
  today. A node clock behind the controller's, or a
  lend that keeps the pool's Ready time, makes the Pod time too old.
- **Idle needs positive evidence, and the evidence is role-aware.** KV here
  is **peak** KV over the rule's window, not instant KV: prefill KV is freed
  once a prefix is handed to decode, so an instant 0 between requests is the
  ordinary case on a lightly loaded prefill pod.

  | role | a row counts toward idle when | load returns when |
  | --- | --- | --- |
  | decode, aggregated | peak KV known and 0, **and** either ITL is known-empty (series present, window empty) or the generation-token rate is known and 0 | a finite ITL, or the generation-token rate known and > 0 |
  | prefill | peak KV known and 0, **and** the request rate known and 0 | the request rate known and > 0 |

  Prefill uses the request rate, present on every engine, for both entering
  and leaving idle. `PrefillComputedTokenRate` is not a substitute: it is
  absent on SGLang. ITL-empty is not evidence on a prefill pod: the P/D sidecar rewrites prefill
  requests to `max_tokens=1`, so a busy prefill pod records no inter-token
  intervals and reads known-empty on every row. This table changes what
  `steadyGate.idleRows` counts on a prefill pod: scaling-policy.md defines an
  idle row as "no latency and no KV fill". That is a named change, and the PR
  that makes it rewrites the key's row there.

  `idleRows` counting rows in a row (default 2) make the replica idle. The ITL
  learner applies that count over the rows the tracker returns; the tracker
  does not fix it. "In a row" breaks on a gap: two counting rows more than
  1 min apart, with only unknown rows between, do not continue a run. The
  limit is on each gap, not on the run's span, so `idleRows: 5` (75 s at a
  15 s cycle) still makes a replica idle. A row where a gauge it needs is
  unknown neither counts toward idle nor breaks an idle run. So Prometheus
  timing out the KV and ITL queries for two cycles does not mark a busy fleet
  idle, and the next good row does not restart every replica's settle as
  "load returned after idle". A row with KV 0 and a finite ITL is a reading (a
  light or draining replica), not idle. A row with k > 0 and no ITL is a busy
  replica whose ITL is missing. A row whose bits are all false is a failed
  scrape: it neither counts toward idle nor restarts a settle.
- **Gaps and lookback.** A gap longer than the rule's period restarts the
  judgement. A gap is **missing rows**, not unknown gauges: a row is a
  sighting whenever the pod is listed, whatever its bits, so a Prometheus
  brownout longer than 1 min does not restart every replica's settle. A cycle
  that reads no rows of the model at all (its collection failed, or returned
  no series) is not a gap either: the engine holds the model's sightings
  through it (PR 1), for up to 5 min without an observation, or the
  tolerance if longer. A rule
  compares against the reading one period back, not one cycle back:
  cycle-to-cycle one-minute peaks share 45 s.
- **The brownout trade.** A run of unknown rows keeps the settle. So a replica
  that goes idle and takes load again inside a long brownout keeps its settled
  state, and its first known rows, a fill transient, are admitted. That errs
  on the cost side: a fill reading has a standing queue and low residency, so
  it lowers k2 and over-orders, and k2's own transient checks (0.7 of mu, 0.7
  of k2) catch most such readings. PR 1 could instead have ended the settled
  state after more than 2 min of unknown rows, at the price of 2 min of
  refused admissions per replica; it keeps the settle, and holds it through
  cycles with no rows at all as well. The brownout case in
  [section 8](#8-risks-and-limits) pins it.

Each learner keeps its own predicate and thresholds over the trace the tracker
returns. k2's rule needs a second gauge, a precondition and a cross-learner
read, and stays k2's. Before PR 1 merges, all four rules above are written
against the API (the review sketch already writes k2's against `Back` and
`Trace`).

**The tracker exposes event times; each learner keeps its policy.** The
tracker records when a replica joined, went idle, took load back, restarted or
was lent, and the time of each row. It does not decide how long a learner
waits. Each learner applies its own window, keyed by its policy. Examples, not a
complete list: `steadyGate.steadyFor`, `steadyGate.idleRows`,
`steadyGate.maxKDrift` and `steadyGate.minSettledSamples` for ITL, `k2Settle.*` for k2, `muChoice.*` and
`admission.*` for mu, `loadImbalance.idleAfterReady` for the balance tracker.
Every key in `internal/config/analyzer_tuning.go` stays honoured, with its
documented range, after PR 1 and 4a. A change to what a key means is a named
change, documented in [scaling policy](../reference/scaling-policy.md) in the
same PR. A key that can no longer be honoured is deprecated with a load-time
`WARNING`, as the crossed-pair repairs do today, never silently ignored.

### 4.4 The cost after a controller restart

Under the shared tracker every replica waits the window again after a
controller restart. This is **on the SLO side**: capacity is `min(k1, k2)`, and
with the snapshot off (the default) every k2 window is empty, so a saturated
fleet prices from k1, the high figure, for 1 min longer than today. ITL does
not refit for `steadyFor`, 2 min by default (a named change in PR 4a). #273 chose the current behaviour to avoid exactly this
delay.

What an operator sees during the ITL wait, and which side it errs on: with no
line there is no derived mu (`deriveMu` returns nothing on a zero model), and
with the snapshot off the measured-mu window is empty too, so the role has no
mu and the throughput floor is silent ("a role with no throughput on record is
silent"). Today that gap is the 3 unstamped readings a pinned A needs, about
45 s at a 15 s cycle. After 4a it is the 2 min settle plus those readings,
about 2.75 min, during which only occupancy holds the fleet: the **SLO side**.
On a default install it is masked by the 300 s scale-down stabilization
(`WVA_SO_SCALE_DOWN_STABILIZATION`, `deploy/lib/scaledobject.sh:3894`), as
long as the controller is back within about 2 min: the gap starts when the old
pod stops serving, so termination, image pull and leader election come on top
of the 2.75 min. An
operator who lowers that under about 3 min, or whose load rises during the
gap, gets an under-order. PR 4a runs the closed-loop probe through a
controller restart and pastes the result.

So: with the snapshot on, first sight is persisted (PR 5) and the wait applies
only to replicas with no record. With it off, the wait is accepted, logged on
the `learner-start` line ([6.1](#61-logs)), and named in the operator column of
PRs 2, 3 and 4a.

## 5. Fits: exclude by state, then fit untrimmed

The pinned ITL fit changed five times inside #273. A weighted median jumped
between two groups of slopes. A trimmed mean dropped a genuinely slower replica
and priced capacity 8.5 % high, on the SLO side. What holds is: stamp each
reading by the replica's state, drop the settling ones, and fit the rest
without trimming.

So there is no shared robust fit and no default fit. A value-based estimator
(median, trimmed or lower median) is allowed only where no state identifies the
contaminant, and the learner names the contaminant and the breakdown it needs.
Existing fits stay until a replay justifies a change.

## 6. Observability

### 6.1 Logs

**`learner-write`**, at `perReplicaLogLevel` (V(3)), one level above the
shipped `-v=2`. #303 moved the per-replica lines there because at the default
they were most of the controller's log, about 1.9 KB per replica per cycle;
`replica-capacity-summary` says what each replica was priced at, at the
default, split under the CRI line limit. A learner may log at the default only
if its lines pass #303's per-cycle budget test
(`TestCycleLogAtTheShippedVerbosityDoesNotGrowPerReplica`) with the first-write
burst after a restart included. Not verified: whether that test, as written,
fails on such a burst; the first PR that adds `learner-write` checks it, and
extends the test if it does not.

| item | rule |
| --- | --- |
| level | V(3) for per-key lines. At the default: `learner-start` (one per model) and nothing per key |
| when | at most **one line per key per cycle**, written after the last replica of the cycle is priced |
| trigger | the value differs from the **last logged** value (not last cycle's) by more than the learner's band, or is set for the first time. Against the last logged value a slow drift still logs, and noise under the band does not |
| band | per learner, stated in its PR, and above its measured cycle-to-cycle noise (mu's must exceed the 4 % that flipped it 11 times in 20 cycles) |
| heartbeat | a key that priced or was written in the last 10 min, and has not logged for 10 min, logs once |
| source change | a mu choice flip (measured, derived, siblings) is a `learner-write` with `source` changing, so latch flaps count from one line |
| fields | `learner`, `modelID`, `namespace`, `variant`, `role`, `key`, `value`, `previousValue`, `source`, `samples`, `pods` (each contributing pod with its reading and time base), and the admission tests passed with their margins |
| ceiling | at V(3), keys × 1 per cycle. 100 variants × about 9 keys = 900 lines per cycle (about 450 KB), reached once after a snapshot-off restart when every key is set for the first time. Steady state, with the heartbeat: up to keys / 40 per cycle, about 90 lines a minute at 900 keys. Each line stays under the CRI line limit (16 KiB); `pods` is bounded for that reason. At the default verbosity the per-cycle volume does not grow with keys or replicas |

E1's 0.0995 req/s was **admitted**; this is the line that would have shown it.

**Decision lines name their learner key and source.**
`throughput-demand-floor` gains `muKey`, `muSource`, `startEstimateSeconds` and
`startSource` (measured, or none until PR 5 wires a seed).

**Sources stay per learner,** in the vocabularies already shipped. k2 keeps
`P1-obs`, `P2-hist`, `P2-prior`, `P2-borrow`, `P3-k2`, `P4-k1`: the
troubleshooting runbook gives each a different remedy. Mu keeps measured,
derived, served-siblings, neighbour, borrowed-line. Start time keeps measured,
seed. **A restore is a field, not a source:** each line adds `restored: true`
until the learner's first admitted reading after a restart, and keeps its
source. No metric gains a `restored` label value; a restored figure shows as
`time() - ..._last_admitted_timestamp_seconds` older than the restart.

| learner | restored? | prices at once? |
| --- | --- | --- |
| k2 | yes | yes, as `P2-hist` (or `P2-prior`) |
| ITL | B, its stash and the held slope (`ITLSlopes`, #329) | yes, from the held slope, within `BucketRetention` and under the same args; otherwise not until refit |
| mu | window yes, choice no | the window yes; the choice restores to the safe side (PR 3) |
| start time | yes | yes |

**`learner-start`**, one line per model at the first cycle after a controller
start: restored keys per learner, cold keys, the oldest last-admitted age, and
`settleUntil`, the time the fleet-wide settle wait ends. It is the line that
logs "every replica waits again". There are no per-key restart lines, no count
lines and no "cleared" lines: counts are metrics only.

**Lines the tools parse stay stable, or are versioned.**
`hack/benchmark/dump_k2_decisions.py` parses `k2-decision`,
`replica-capacity-decision`, `scheduler-queue-demand`,
`throughput-demand-floor`, `scheduler-queue-prefill-share-dropped`,
`prefill-demand-held`, `hot-rank-hold`, `replica-capacity-skipped`,
`replica-capacity-store-fallback`, `variant-capacity-source`,
`zero-replica-capacity-estimate`, `itl-window`, `itl-fit`, `derived-mu` and the
shared-cache decision line. `replica-capacity-summary`, the default-level line
#303 added, is held to the same rule, and a tool that wants per-replica prices
at `-v=2` reads it. A PR that changes any line a tool in
`hack/benchmark/` parses either keeps its fields (adding is fine) or bumps a
`v` field and updates the tools in the same PR. `k2-decision`,
`replica-capacity-decision` and `derived-mu` are at V(3) since #303, and
`dump_k2_decisions.py` already says a run needs `--v=3`. So the zero-line guard
applies only to a run at `--v=3` or higher: there the tools fail when a run has
zero `k2-decision` or zero `throughput-demand-floor` lines, or zero lines of a
message the scenario declares (as `post_run_analyze.sh` already does for
`k2-decision`); an aggregated run legitimately has no prefill lines. At the
shipped `-v=2` the guard reads `replica-capacity-summary` instead. The P-code legend in `dump_k2_decisions.py` stays P-codes
only; `restored` is rendered as a separate column.

### 6.2 Metrics

- Labels: `namespace`, `model_name`, `variant_name`, `role`, and
  `controller_instance` when set. No pod, no bucket. The vocabulary is
  [Every signal as a metric](signals-as-metrics.md), within its budget of about
  60 series per model.
- Series are per variant. A gauge describes the key that priced the variant
  this cycle, and says so.
- Ages are published as `..._last_admitted_timestamp_seconds`, so that
  `time() - x` stays honest through a stalled analyzer.
- Each PR states its series count per variant, and a kept or replaced table
  for the `wva_learned_state_*` series it overlaps.
- **Deletion.** There are no per-key series, so nothing is deleted per key.
  A variant's learner series are deleted when the model is deleted, where the
  engine deletes the others. When the pricing key changes they are set from
  the new key; when no key remains (every key forgotten), the variant's
  learner series are deleted.
- **Forget reaches every learner.** The `Report` carries `pricingKey` and
  `forgotten{key, by: operator | evicted}`; the engine applies both. Every
  learner implements `Forget(key, by)`. The `llm-d.ai/forget-learned-state`
  annotation calls it on all of them through a plain list of learner names in
  `learn`, not a registry, and a spec asserts that every learner on the list
  is reached.

### 6.3 Who does what

`internal/signals/learn` holds vocabulary types: `Report` (counts by reason,
source, samples, last admitted time, writes, `pricingKey`, `forgotten`),
`Source`, `Reason`. The learners fill the Report; `learn` never logs and never
registers a metric. The analyzer logs; the engine publishes. `learn` imports
only the standard library, and `settle` likewise, so `domain` and
`internal/metrics` can use their types without a cycle.

The engine takes reports through an optional interface,
`TakeLearnerReports() map[model][]learn.Report`, as it takes
`TakeRestoreOutcomes`. The map is overwritten per model each cycle, so it stays
bounded when metrics are off. `learn` lands with its first user (PR 2), not as
a standing package.

## 7. Migration

Each PR replays the fixtures under [the golden rule](#25-replay-and-the-golden-rule)
and says what an operator sees: log lines added or changed, metric series added
or deprecated, docs rows rewritten, tools updated.

| PR | change | recording (exists?) | what an operator sees |
| --- | --- | --- | --- |
| **0a** | One clock (2.2), including the capacity store and the engine's calls into it; presence bits with ITL's three states (2.3), reusing `KvUsageInstantKnown`, `CapacityQueueKnown` and `PreemptionRateKnown` and adding `RunningRequestsKnown` (a prerequisite of #277). Unit tests, the `EpisodeGap` negative control, the one-cycle boundary test | none needed | Nothing, beyond 2.2's one-cycle boundary. Metrics: none |
| **0b** | The recorder at the input boundary in each analyzer and the store (2.4), with each `Analyze` result recorded and the test that a replay at the recording's commit reproduces it byte for byte; raw rows and the merged policy recorded every cycle; the store split into parsing and one shared method; one switch hook per package; replay with goldens and the golden rule (2.5), per-path counters, property tests with the known-failures ledger and a mutant per mapping row (2.6, 2.7). Mu's mean fold and the sibling yield go into the base goldens, the one-slow-pod case as a passing guard | E1 re-run from a build with 0a, and one under a non-default `analyzerTuning` (no) | Nothing. A recorder flag and overlay in `testing.md`. Metrics: none |
| **1** | `internal/signals/settle`, written by the engine at the top of `runAnalyzers`, ahead of `observeLoadBalance` and of the input builder (`buildAnalyzerInput`, #345 step 1), as #345 decided; read-only views for the balance tracker and the analyzers. The tracker exposes event times; each learner keeps its policy-keyed window (4.3). #283's one-missing-row forget becomes the gap rule, a named change proved by unit tests on the balance tracker, with a re-lend within 1 min still restarting the grace (4.2): a row missing one cycle under the same lend keeps the grace, and a Pod lent again restarts it, told apart by a lend event, not by the absence (both restart on the parent). `idleRows`, `steadyFor` and `idleAfterReady` keep their documented meaning, or the PR names the change in scaling-policy.md. 2.3's A4 and 273-r3 #2 negative controls | 0b's, the non-default one included (no) | The imbalance timers no longer restart on one missed scrape. The `WVALoadImbalanced` alert text is checked against the new rule |
| **2** | k2 (#3, #4) onto the tracker. k2 keeps the Ready-time rule `noteReady` applies today (#288), now read from the tracker; nothing is switched on twice. First task: a fixture from #361's workload-patch group restart (leader and workers recreated, pod names kept, Ready times differ). It establishes where a group's Ready time comes from. Done: the latest Ready time over the leader and its Ready workers (`TestLWSGroupReadyTimeIsTheLatestOfItsPods`). The rule: each pod waits from the later of its own Ready time and first sight. `learn` lands. `learner-write` for k2, at V(3). Restart suite | flow control off, P/D, same-name restart (no) | After a controller restart every replica waits once: `P1-obs-unsettled` across the fleet for 1 min, and with the snapshot off k2-less fleets price at k1 for that minute (SLO side). A pod back from a restart under the same name already waits today (#288) and still does. `learner-start` at the default; k2 writes at `--v=3` |
| **3** | Mu (#5, #6). The fold across pods stays the mean (#282). Its joined test reads k2's stamp. Exact repeats count as use. Unknown age resolves safe. The latch persisted or restored safe-side. Two prefill pods in one spacing pin #7's max before the fold is touched. The closed-loop probe and its fleet simulator land here (2.5) | flow control off (no) | `learner-write` for mu, including choice flips. `throughput-demand-floor` names `muKey`, `muSource`, `startEstimateSeconds`, `startSource`. Measured mu waits out the post-restart settle |
| **4a** | ITL #1 and #13 onto the tracker. Cap admission above the band (#293, #331), the policy band, the steady-gate keys and the persisted held slope (`ITLSlopes`, #329) stay as they are. Neutral on the recording except two named changes, each behind its own switch: ITL waits the settle after a controller restart (today it takes the first cycle as settled), and one missing row no longer restarts a settle (the 1 min absence rule, 4.2). Neither occurs in the OLS recording, so 4a carries its own two tests through ITL, a controller restart and one missing row, each failing on 4a's parent; PR 2's restart suite runs as well. The closed-loop probe runs through a controller restart (4.4) | a run that reaches OLS, SLO analyzer recorded with its router latency (no) | No ITL refit for 2 min after a controller restart; with the snapshot off the throughput floor is silent for about 2.75 min (SLO side, masked by the default 300 s scale-down stabilization as long as the controller is back within about 2 min). A missed scrape no longer restarts ITL's settle |
| **4b** | ITL #16 (throughput analyzer) moves onto `itl.Tracker`, a tracker of its own, and gains #273's fixes and row 1's cap admission, still reading the policy band (`ITLBand()`). Five switches over four PRs (4b-0 to 4b-3): the steady gate (the move, with the settling stamp, the pinned fit over the window, positive B, +Inf refused and the held slope), the GPS-mismatch clear dropped, the settle tracker, the wait after a controller restart, and cap admission; each with a negative control on the parent, and the probe for the slope hold. Design: [PR 4b](#pr-4b-the-throughput-analyzers-itl-line) | `throughput-sweep` and `throughput-sweep-restart` (no) | `learner-write` when A or B moves; `Reason` `T2-held`; no throughput capacity for about 2 min after a controller restart. Throughput-analyzer fleets may change, as named: mostly fewer replicas where the throughput analyzer set the order (above-band rows and joiners' warm-up no longer steepen A), and the analyzer is silent, so out of the scale-down vote, more often. Metrics: none |
| **5** | Start time (#12) and snapshot. Confirmations counted by distinct pod. The first-sample rule, a named change that bounds **both** sides. PR 5 first wires the seed, a named change of its own: `replicaStartSeconds` is reserved and read by nothing today ([trigger reference](../reference/scaledobject-trigger.md)), so every estimate starts from 70 s. Once it is read, the first-sample bound is anchored to the seed when the ScaledObject sets one, not to the 70 s default. With no seed it blends toward the default until N distinct pods agree, rather than clamping: measured starts run from 15 s (snapshot restore) to 463 s (cold JIT cache) ([start time](../reference/start-time.md)), and a fixed band around 70 s would clamp both ends. Property cases: with a seed of 120 s, a first sample of 15 s gives an estimate in [lower bound, 120] and one of 460 s an estimate in [120, upper bound]; with no seed the estimate sits strictly between the sample and 70 s (never clamped to 70) and reaches the measured value after N distinct pods. First sight persisted. The writer stays central (`internal/engines/steadystate/learned_state_store.go`) with per-learner adapters. The document stays **v1 and additive**: new fields are `omitempty`, as #329's `itlSlopes` was, and PR 5 does not bump the version, which is part of the data key (a v2 would make every upgrade a cold start). `PriorK2` and `K2BorrowedSince` stay dual-written for rollback builds. Fixtures: a v0.10.0 v1 document (no `itlSlopes`) and a v0.10.1 v1 document both restore, and the previous release reads PR 5's document: a test pins a frozen copy of v0.10.1's `learnedStateDoc` and decodes PR 5's document with it. Restart suite plus restore-then-run, including a restore under light load that keeps the ITL line (issue #316). Design: [PR 5 design](#pr-5-design-start-time-and-the-snapshot) | controller restart with the snapshot on (no) | Start estimates no longer take one pod's first sample raw. `restored: true` on restored lines. With the snapshot on, only replicas with no record wait after a restart |

Later, each after a recorded run that covers it: idle room and the decode gate
(#9, #10) after a P/D run; the prefill bound (#7), with a double-scrape fixture
and its own-reading witness. Out of scope: drift actions, fit changes (one
named exception: PR 4b moves the throughput analyzer's pinned fit from least
squares through B to row 1's k-weighted mean, since that is the fit
`itl.Tracker` takes), #8, #11,
#14, #15, and the share planner's holds (why a second transfer waited), which
belong to the utilization-share work.

**Order with #345** (a composite that does not depend on saturation): 0a, 0b,
then #345's step 0 (the engine-level golden, with its own events in 0b's
sink) and its step 1 (one registry, one
`AnalyzerInput` built before `Analyze`, zero golden change), then PRs 1 to 5,
then #305 and #345's later steps. `AnalyzerInput.Capacity` is a read-only view
of the store for the cycle; learning capacity stays in the analyzers, as this
plan has it. #345's last step (saturation runs only when listed) needs an owner
for k2, mu, start time and the snapshot first.

### Shipped behaviour each PR must keep

These merged after revision 6. A step that follows the plan without them would
undo them. Each row is a test: an input, an assertion, and a negative control
that fails it. The owning PR adds the test, or keeps it green if it exists.

| behaviour (shipped in) | input | assertion | negative control | owner |
| --- | --- | --- | --- | --- |
| A restart under light load keeps the ITL line (issue #316, #329) | a v0.10.0 v1 document (no `itlSlopes`), a v0.10.1 v1 document, and PR 5's document; a restore under light load | both old documents restore and the held slope keeps the line. A frozen copy of v0.10.1's `learnedStateDoc` (`steadystate/learned_state_store.go:83`), pinned in the test, decodes PR 5's document with every field it knows intact | a PR 5 document that renames or drops a v0.10.1 field fails the frozen decode | 5. 4a keeps `TestARestoredSlopeIsInstalledOnlyWhenItCanBeTrusted` (`saturation/held_slope_restore_guard_test.go`) and `TestAHeldITLLineSurvivesARestart` green and does not drop `ITLSlopes` |
| A non-default `analyzerTuning` is honoured (#298) | `idleRows`, `steadyFor` and `idleAfterReady` all off their defaults | idle after exactly `idleRows` rows, steady after exactly `steadyFor`, grace over after exactly `idleAfterReady`; none of them one cycle earlier | a build that hard-codes the defaults fails it | 0b (records it), 1 and 4a (test it) |
| On P/D decode, k2 reads the engine's deferred queue, rewritten before k2 reads `MaxRankQueueLength` (#294, #306) | a P/D replay; raw rows recorded | replay runs `decodeCapacityView` again and matches the golden | the base goldens | 0b |
| ITL admits a replica at its sequence cap above the band (#293, #331) | running equal to `max_num_seqs` (from the flag, lowered by `--max-num-active-seqs`) on every rank, requests waiting, preemption read and under `capMaxPreemptionRate`, KV above the band, a valid ITL | the reading is admitted to the fit | admission removed (`itl/admission.go` `atSequenceCap`) fails it | 4a. 4b runs the same input through the throughput analyzer, where admission is new: a named change, refused on its parent |
| Mu folds across pods by the mean, and yields to its full siblings (#282) | the base recordings; one slow pod | the goldens hold; one slow pod moves mu by its weight only | a fold by max or min fails the one-slow-pod case | 0b, 3 |
| The default verbosity does not grow per replica; no line passes the CRI limit (#303) | one cycle at 8 and at 64 replicas, the `learner-write` burst included | the same line count at the default verbosity for both, and every line under the CRI limit | a per-replica line at the default fails it | every PR that adds `learner-write` (2, 3, 4a, 4b, 5). Today the test lives only in the saturation package (`saturation/replica_log_test.go`: `TestCycleLogAtTheShippedVerbosityDoesNotGrowPerReplica` at 1 and 8 replicas, `TestSummaryStaysWithinTheCRILineLimit`); each other package that adds `learner-write` adds the same test (throughput in 4b) |
| A warm-pool Pod lent, and lent again, starts its grace again (#283) | balance-tracker rows: one Pod missing one cycle under the same lend; one Pod lent again to a variant within 1 min | the missing row keeps the grace; the re-lend restarts it. The tracker keys by {pod, variant, bridge} plus a changed ReadySince (`balance/tracker.go:117-141`), and a re-lend to the same variant keeps that key, so the discriminator is a lend event PR 1 adds (`ReplicaMetrics` carries only `FromWarmPool` today) | the parent restarts on both, so the first case fails there | 1 |
| A pod back from an engine or LWS group restart waits again, from the later of its Ready time and first sight (#288; #361 issues LWS group restarts) | PR 2's first task: a fixture from #361's workload-patch group restart, leader and workers recreated, pod names kept, Ready times differ. It establishes where a group's Ready time comes from. Done: the group's row is its leader's, with the latest Ready time over the leader and its Ready workers, so a worker restarted alone under `restartPolicy: None` makes the group wait (`TestLWSGroupReadyTimeIsTheLatestOfItsPods`) | each pod waits from the later of its own Ready time and first sight | on the parent, a pod whose Ready time the group restart does not change shows no wait | 2 |

### PR 4b: the throughput analyzer's ITL line

Line numbers are main's at 67790e633. T = `internal/engines/analyzers/throughput`,
I = `internal/signals/itl`, D as before.

#### Row 16 against row 1 today

| rule | row 1 (saturation) | row 16 (throughput) |
| --- | --- | --- |
| Band | policy band (`I/tracker.go:175,252-255`) | the same band, on the window only (`T/analyzer.go:294-301,189`). The pinned fit (tier 2) reads **every** healthy row with k > 0, no band: rows above 0.80, the thrashing ones the band exists to keep out, and rows near zero (`T/analyzer.go:637-643`) |
| Cap admission | above the band at the sequence cap (`I/admission.go:142-147`, `I/tracker.go:254`) | none. The analyzer reads no capacity store, so it knows no cap (`T/recording.go:26-27`, `T/analyzer.go:108-111`) |
| Ready, bridge | Ready and not lent (`I/tracker.go:212,244`) | bridges dropped (`T/analyzer.go:291,767-776`); Ready not read, the collector's zero ITL on a not-Ready pod stands in (`T/analyzer.go:353-357`) |
| ITL valid | > 0, not +Inf, Finite by presence bits (`I/settle.go:97-102`) | > 0 and not NaN (`T/sanity.go:69`, `I/window.go:130`). **+Inf is admitted** to the window, and poisons tier 1 for up to 20 rows or 30 min (every fit fails `ValidModel`); tier 2 fails the same cycle |
| k > 0 | `traceable`, a k of 0 is no reading and no trace (`I/steady.go:250-252`) | met for admission: the band's lower edge is > 0, tier 2 checks k > 0 (`T/analyzer.go:638`). There is no trace, so nothing for a k of 0 to unsettle. Not a change of its own: it comes with the trace |
| Steady gate | full fit on the steady window only, 40 rows, carry 5 min (`I/tracker.go:186-191,268-269,305-328`) | full fit on every windowed reading (`T/analyzer.go:605-607`) |
| Settle stamp | a reading taken in its replica's settle is stamped and kept out of the pinned fit (`I/tracker.go:260`, `pinnedReadings` `:428-447`) | none: a joiner's warm-up reading enters tier 2 on its first cycle |
| Settle source | the engine's settle tracker, 4a's six rules (`D/itl_settle.go:76-97`) | none. Registered without a settle view (`cmd/main.go:943`) |
| Pinned fit | k-weighted mean of (ITL - B) / k over the window (`I/fit_pinned.go:56-78`); at least 10 readings and 3 settled (`I/tracker.go:347-348`) | least squares through B, sum((ITL - B) k) / sum(k^2), over **this cycle's** rows (`T/analyzer.go:633-645`); one reading of one replica is enough |
| Remembered B | only a positive B from a steady full fit (`I/tracker.go:335-337`) | any tier-1 B (`T/analyzer.go:612`). `ValidModel` admits a negative B (`I/model.go:64`), and tier 2 then pins it |
| Held slope | last A held under too few settled readings or a short window, bounded by `BucketRetention` and the engine arguments (`I/tracker.go:375-383`, `I/admission.go:93-98`) | none. No line is `T2-failed` and the variant is skipped (`T/analyzer.go:367-374`) |
| Window clears | never on a shape change (`I/window.go:87-89`); args change stashes B (`D/mu_from_itl.go:286-307`); none mid-rollout (`D/analyze.go:511-513`) | cleared on a shape change (`T/analyzer.go:168-178`) and after 3 GPS mismatches (`:425-437`), B kept; no args rule; no rollout rule |
| Key | namespace, model, variant, accelerator, GPUs (`D/analyzer.go:1131-1134`) | namespace, model, variant (`T/analyzer.go:564-566`) |
| Forget | swept 24 h unseen (`D/analyzer.go:844`); on `learn.Learners` | the whole variant state after 1 h unseen (`T/analyzer.go:202-206`); no forget method, not on `learn.Learners` |
| Persisted, shared | B, stash, held slope; lines published and borrowed (`D/analyze.go:543-590`) | nothing |
| Report | `itl.Reading` counts, `learn.Report`, `learner-write` (`D/itl_learner.go`), path counters `itl-ols`/`itl-pinned` (`D/mu_from_itl.go:370-375`) | `Reason` `T1-ols`/`T2-default`/`T2-pinned`/`T2-failed` (`T/constants.go:75-79`), debug lines, `VariantState` (`T/types.go:63-96`) |

Persistence, sharing, the args stash, the rollout rule, speculative decoding
(#274) and the key are **not** in 4b: none is one of #273's fixes or the cap
admission.

#### Reuse

Row 16 moves onto `itl.Tracker`, `itl.Admission` and `itl.Settle`, with a
tracker **of its own**, as the slo analyzer did (row 13,
`slo/analyzer.go:504-538`). Sharing the saturation analyzer's instance is
out: both analyzers would add the same rows to one key in one cycle, and the
keys differ. What blocks the move today, each fixed in 4b-0:

1. **No settle view.** `cmd/main.go:943` passes only the clock. The engine
   hands the throughput analyzer its tracker as it does slo's
   (`steadystate/engine.go:335,408`), with `WithSettleView`,
   `WithOptimizationInterval` and `WithSlopeHoldFor` (`BucketRetention`).
   Without a view it observes a tracker of its own, as slo does
   (`slo/settle.go:94-109`).
2. **No engine parameters.** Cap admission needs the stated cap, and the
   held slope's bound needs `ArgsFingerprint`. The analyzer takes the
   capacity store read-only and reads `EngineParams` only, as slo's
   `admission` does (`slo/analyzer.go:504-517`). Replay already runs the
   analyzers on one store; the field check for a throughput-only replay is
   `EngineParams` (2.4).
3. **The two window clears.** The shape-change clear is kept, holding B and
   the last A. #453 added `Tracker.ClearWindow(key)` (`I/tracker.go:503`):
   the main and steady windows go; B, the held slope and the settle traces
   stay. The shape-change clear calls it from 4b-1. The GPS-mismatch clear
   is dropped in 4b-1 ([below](#the-gps-mismatch-clear-dropped-in-4b-1)).
4. **Config through an interface.** `internal/config`'s tests import this
   package (`T/analyzer.go:295-297`), so steadiness and the cap's preemption
   rate are read as `ITLBand` is: `ITLSteadiness() itl.Steadiness` and
   `ITLCapPreemptionRate() float64` (`config/analyzer_tuning.go:649,659`).
5. **Recorder.** `start.Throughput` is a bool (`learnreplay/replay.go:114`);
   it becomes the throughput analyzer's settings (slope hold, interval, a
   view or not), so a replay builds it as the run did.

The `Reason` values map from `itl.Reading`: `TierOLS` is `T1-ols`; pinned with
a learned B `T2-pinned`, else `T2-default`; no line `T2-failed`; a held slope
`T2-held` (new); the above-band fallback `T2-above-band` (new, added in
4b-1's review: [4b-1 as built](#4b-1-as-built)).

#### Named changes

Switches live in T and are set by `SetSwitchesForReplay`
(`T/recording.go:54`). Off is the parent's code, so all off reproduces the
base goldens.

| switch | changes | an operator sees | negative control (fails on the parent) |
| --- | --- | --- | --- |
| `throughput-itl-steady-gate` | The move onto the tracker, with the key's own trace, and #273's fixes with it: the steady gate on the full fit; the settling stamp; a pinned fit over the window's readings, in the band, k-weighted, with at least 10 readings and 3 settled; a positive B only; +Inf refused; the held slope. The shape-change clear through `ClearWindow`, which drops the held slope too; the above-band fallback for a key with no line and no slope to hold ([as built](#4b-1-as-built)) | `Reason` `T2-held` and `T2-above-band`. `learner-write` for the line when A or B moves (band 5 %, as 4a), and a tier change. Fewer `T2-*` lines in a variant's first cycles (10 readings first). Metrics: none | A joiner's warm-up reading (k 0.46, 450 ms against a line near 0.09) moves A on the parent and not on; one reading of one replica sets A on the parent and not on; an +Inf ITL leaves `T2-failed` for 20 rows on the parent; a step through the band teaches B on the parent from unsteady rows |
| `throughput-itl-keeps-the-window-on-gps-mismatch` | Three cycles of GPS mismatch no longer clear the window; the comparison stays a diagnostic, on the minute-mean k, one summary line per variant ([why](#the-gps-mismatch-clear-dropped-in-4b-1)) | `GPS mismatch persisted, clearing observation window` is gone; `GPS mismatch detected` moves to the debug level with a per-variant summary. More full fits on a P/D decode fleet | A correct line with the generation rate 30 % off for three cycles: the window empties on the parent, keeps every reading on |
| `throughput-itl-reads-the-settle-tracker` | A replica's settle start comes from the engine's tracker, with 4a's rules: restart under its name, idle on evidence, the absence tolerance, the Finite pod mean. Meaningful only with the gate on; on alone it moves nothing | None in logs; a missed scrape no longer restarts a replica's settle | A restart under the same Pod name with no not-Ready row between: settles on, not on the key's own trace |
| `throughput-itl-waits-after-controller-restart` | After a controller restart, replicas already serving wait their settle. With no snapshot of this line (none exists), the analyzer has no line for `steadyFor` | `T2-failed` and no throughput capacity for about 2 min after a restart: the analyzer is not live, so it neither orders nor vetoes a scale-down | The restart case of 4a, through the throughput analyzer: a line on the first cycle on the parent, none for `steadyFor` on |
| `throughput-itl-admits-at-sequence-cap` | Readings above the band from a replica at its stated cap, with preemption at most `itlBand.capMaxPreemptionRate`, enter the window | A line, and so throughput capacity, on a variant whose only replica in reach is at its cap (#287) | A replica at its cap above the band: refused on the parent, admitted on. With the gate on and this off, the variant has no line |

What can move the fleet size, and which way:

- **Down (TA orders fewer).** Tier 2 today takes above-band rows and every
  joiner's warm-up: both steepen A, cut supply (`N_sat / ITL(0.85)`), and
  order more, most during a scale-up, which feeds itself. Dropping them
  raises supply.
- **Silent more often.** The 10-reading floor and the restart wait leave
  variants with no line where tier 2 had one. A silent analyzer is out of the
  scale-down vote (`T/analyzer.go:488-493`), so the others can shrink the
  fleet where TA would have held it: the risky side, masked by the default
  300 s scale-down stabilization as in 4.4.
- **Either way.** B from steady fits only, and the held slope at light load
  in place of a tier-2 fit from k near zero, whose A is noise over a small k.
- **Up (live again).** Cap admission, and +Inf no longer blanking the line.

TA only drives a scale-up where it is the highest order; saturation still
votes. A policy that lists throughput alone has no such floor: a silent line
holds the role. 4b-1 says so in
[configuration.md](../reference/configuration.md#the-throughput-analyzers-line),
with how long the line is silent after a restart or a shape change, and the
above-band fallback's one-sided error.

#### Recordings

No throughput recording exists. The in-process goldens carry throughput
results (`acceptance` 52 rows, 19 `T1-ols`; `engine` 28, 7 `T1-ols`;
`mu-paths` 192, all `T2-default`), synthetic, so the switches move them but
the physics is not in them.

**Needed:** the `ols-sweep` run of testing.md, with the throughput analyzer
on, as one run that gives two recordings.

| item | value |
| --- | --- |
| scenario | `hack/benchmark/scenarios/guides/pd-disaggregation.yaml`, `MODEL_ID=Qwen/Qwen3-0.6B`, EPP flow control off, decode KV 1024 blocks and `VLLM_MAX_NUM_SEQ=64`, decode 1-1, **prefill 1-1** (ols-sweep had 1-2) |
| analyzer config | the policy lists `saturation` and `throughput`, default `analyzerTuning`. Set it **before** the recorder: the throughput analyzer registers only at controller start (`config/config.go:425-437`) |
| recorder | the `learner-recorder` patch as the last change before load; default cap (256Mi) |
| load | `hack/benchmark/closed_loop_sweep.py`, default schedule (about 55 min) |
| restart | the leader Pod deleted 90 s into level 5 (1200 s, 20 min in), snapshot off, as for the slo pair. The first recording is `throughput-sweep`, the second `throughput-sweep-restart`. The run is taken at a commit with 4b-0 merged; the recipe is [testing.md](../developer-guide/testing.md#learner-goldens) |
| GPUs | 2 H200 for about 70 min including setup; torn down on every exit path |
| counters above zero | throughput OLS and pinned cycles; settling readings; held cycles in the restart recording |

A kind or simulator run is not enough: the simulator's ITL has no warm-up
and no thrashing above the band, which are the readings this PR removes,
and only a real decode engine has reached a full fit (2.8). The cap and the
held slope through 30 min of light load stay synthetic (2.8): the sweep does
not hold the decode pod at its cap without preempting.

#### Tests

- **Property cases (2.6), run on the throughput line.** The generator moves
  from D's tests into a test package both can import, with D's `cases.txt`
  unchanged. Cases: transient (joiner warm-up, load step, controller
  restart); first sample (one reading); stale value at N = 2 and 4 (B
  converges); missing and duplicated (+Inf ITL, k of 0, a missed row);
  edges (band edges at default and non-default, the cap and its preemption
  rate, `steadyFor`, `minSettledSamples`, 10 rows and spread 0.30); known
  truth at k = 0.85 (the throughput analyzer prices at `DefaultKSat`);
  contamination paired with heterogeneity (a slow replica moves A by its
  weight). Each mutant is a switch off. Cases the parent fails go in the
  known-failures ledger with 4b as owner, and leave it as each lands.
- **Closed-loop probe (2.5),** on a fleet simulator driving the throughput
  analyzer (the D simulator, moved beside the generator): the slope hold
  (a line learned at load, 35 min below the band, then a step back to
  need); a scale-up whose joiners carry warm-up ITL; a controller restart
  before a load step (under need for at most 5 min); one replica at its
  cap with siblings below the band. Each pastes its result on and off.
- **Verbosity:** the throughput package gets `replica_log_test.go`'s two
  tests (shipped-behaviour table).
- **Replay switches** (`learnreplay/switch_runs_test.go`): the four go in
  `replaySwitches`, with `switchPackage` learning `PackageThroughput`, and the
  outputs each moves, measured: the steady gate is expected to move
  `results.csv` and `variant-capacities.csv` of `acceptance`, `engine` and
  `mu-paths`, the composite files of `engine`, and both new goldens; the
  restart wait `acceptance`, `engine` and `throughput-sweep-restart`. The
  settle-tracker and cap switches are expected to move none, and go in
  `heldBy` with their negative controls. `switchBase` takes the base
  digests of every output named. 4a's and earlier rows stay in
  `earlierSeams`; each 4b PR moves its predecessor's rows there.

#### Order

| PR | contains | goldens |
| --- | --- | --- |
| 4b-0 | the admission's inputs, wired and not yet used: settle view, slope hold and interval in the admission; the capacity store wired but not read until 4b-1 (`EngineParams` only, then), wired in main (`Engine.NewThroughputAnalyzer`) and in replay; the policy read through interfaces (`itlPolicyOf`); `start.ThroughputConfig`, so a recording states the analyzer's settings and a replay builds it as the run did. The window still reads only the band | none move: every golden replays byte for byte |
| 4b-1 | the two recordings with base goldens (own commit, taken after 4b-0 merged); the shared generator and simulator; the verbosity test; `throughput-itl-steady-gate`, `throughput-itl-keeps-the-window-on-gps-mismatch`, `learner-write` | as named above |
| 4b-2 | `throughput-itl-reads-the-settle-tracker`, `throughput-itl-waits-after-controller-restart` | the restart wait's |
| 4b-3 | `throughput-itl-admits-at-sequence-cap` | none expected |

4b-1 can land on the synthetic goldens alone if the recording is late; the
recordings then land in 4b-2 with their base goldens. Forget for this line
(and mu, which waits for it, 4a's decisions) is not in 4b: a small PR of its
own after 4b-3.

Decided on the design: the real 2-GPU recording; the cap stays synthetic;
the shape-change clear is kept, holding B (it held the last A too until
4b-1's review dropped that: see [4b-1 as built](#4b-1-as-built)) (the GPS-mismatch
clear, first kept too, is dropped: see below); the
analyzer reads `EngineParams` from the store now, not after #345;
`T2-held` is a new `Reason`; the k-weighted pinned fit is in scope (the
exception section 7 names); the four-PR split.

#### 4b-1 as built

Status: built on `feat/learner-4b1`, on main after 4b-0 (#455). The
`throughput-sweep` recordings are not in it; they land in their own commit,
with base goldens, when the run is taken (`TestGoldens_ThroughputSweepFloors`
waits for them).

What each switch moves, measured on the in-process goldens (`acceptance`,
`engine`, `mu-paths`, the only fixtures that run the throughput analyzer),
each switch on alone against all off; base f7b4f7e47, the same tree as main's
69fb6395c, so `switchBase` is unchanged by the merge:

| switch | outputs it moves | how |
| --- | --- | --- |
| `throughput-itl-steady-gate` | `results.csv` and `variant-capacities.csv` of all three; `composite-holds.csv`, `composite-release.csv` and `composite-variants.csv` of `engine` | each variant's first cycles have no throughput capacity (10 readings first), and the line after them is the window's pinned or full fit, not one cycle's rows. Since the review's above-band fallback, one more `mu-paths` row: `variant-02` at 09:00:45 is priced from above the band (`T2-above-band`, `TotalDemand` 540 where it was 0), the fixture's only fallback cycle |
| `throughput-itl-keeps-the-window-on-gps-mismatch` | `variant-capacities.csv` of all three | the in-process rows trip the GPS check; kept, the window reaches a full fit where the clear had emptied it |

What else 4b-1 built:

- **Shared test helpers.** `internal/engines/analyzers/internal/learnertest`
  holds what the saturation and throughput tests share: the seeded
  generators and the time-base family, the property judgement against a
  known-failures ledger, and the closed-loop fleet simulator
  (`learnertest.Fleet`). The saturation tests run on it unchanged: every
  case id in `cases.txt` and every closed-loop probe's result is the same
  as before the move. A test holds that no non-test file imports it.
- **Property cases on the throughput line** (2.6;
  `throughput/property_test.go`, ledger and case list under
  `throughput/testdata/properties`): transient (joiner warm-up, load step
  with an ITL overshoot, controller restart); first sample; stale B at
  N = 2 and 4; missing and duplicated (+Inf ITL, k of 0, a missed row);
  contamination paired with heterogeneity (one slower replica moves A by its
  weight); known truth at `itl.DefaultKSat`, direct and on the time base;
  edges (10 readings, spread 0.30, the band at the default and at
  [0.05, 0.90], `steadyFor` at the default and at 10 min, the sequence cap);
  and the above-band fallback never pricing under the truth (below).
  The stale-B case's 15 % is its declared tolerance: on the tracker the
  converged B's error has mean about 0 and SD about 3.6 %, so 15 % is about
  4 SD. The mutant is the steady gate off: joiner warm-up, load step, one reading,
  +Inf ITL, ten readings and both `steadyFor` edges fail on the parent
  (`TestThroughputPropertyMutants`). One ledger row: the sequence cap, owner
  4b-3.
- **The above-band fallback** (part of `throughput-itl-steady-gate`;
  `itl.Admission.AboveBandFallback`, which only this line sets). A key with
  no line from the band and no slope it may hold -- one that never fitted,
  under load that holds every replica above the band -- fits A from its
  readings above the band at a k of at least `itl.DefaultKSat` (0.85), under
  the same steady gate, settle stamp and floors (10 readings, 3 settled),
  with B pinned at the least ITL among those readings (`Reason`
  `T2-above-band`). The band's first fit replaces it; it is never used while
  the band gives a line or a slope is held, and its A is never held nor its
  B stored as the key's intercept. Without it, a controller restart 15 s
  before a step into saturation left the restarted line silent for as long
  as the load stayed up, and with throughput alone the fleet stayed at 4
  against a need of 9.

  It is one-sided by construction: it never prices a higher mu than the
  truth. Every reading is ITL = B' + A k + noise with A >= 0 and k > 0, so
  the least ITL is at or above the true B'. On readings of the true line,
  the line through that B prices, at kSat, A kSat + B' + (B - B')(1 -
  kSat/kbar), kbar being the readings' mean k. Both factors of the last term
  are >= 0: the first because B >= B', the second because every reading's
  k is at least kSat. That second condition is why readings between the
  band's top (0.80) and 0.85 are not kept. Overloaded readings, which sit
  above the line, only raise the price. The strict minimum is used, not a
  low quantile: above the band the gauge's noise (about 1 ms) is small
  against A k (40 ms or more). Measured on the property case
  `known-truth/throughput-itl/above-band-fallback` over 100 seeds, the
  priced ITL is at least 2.5 % over the truth at a true B of 4 ms and 2.0 %
  over at 10 ms. Negative control: with B pinned at the 6 ms default, as the
  band's fit pins a key that never fitted, the 10 ms case prices under the
  truth on every seed.
- **A shape change drops the held slope.** The clear empties the window and
  now forgets the held A too (`itl.Tracker.ForgetSlope`), on this line only:
  shorter requests have a higher A, and the old shape's A held into them
  priced a lower ITL, so fewer replicas, until ten new readings arrived.
  Until the new shape fits, the line is the above-band fallback or silent.
  The saturation analyzer's clear is unchanged.
- **One summary line when the line goes silent or comes back,** at the
  default level, as the GPS summary: `throughput analyzer: ITL line` with
  `silent`, the `cause` of a silence (`no-healthy-rows`, `fit-declined`,
  `shape-change`, `all-above-band-below-ksat`, `restart`,
  `too-few-readings`, judged in that order), the `source`, and the cycle's
  rows in the band, above it, below it and settling, and what the band's
  and the above-band windows hold. A cycle with no healthy row reads
  nothing and has no line; before the round-2 review it priced from the last
  cycle's reading, for as long as the rows stayed unhealthy. The reference
  pages say what each `cause` means and what to do
  ([cycle log](../reference/cycle-log.md#reason-values-reason-field),
  [troubleshooting](../reference/troubleshooting.md#symptom-index)). A
  GPS mismatch the log opened is closed when the line goes silent or the
  variant has no shape, and a NaN GPS error (an infinite observed rate) is
  no mismatch.
- **Closed-loop probe** (`throughput/closed_loop_probe_test.go`), each
  case run on the tracker and on the parent's line: the slope hold through
  35 min of light load (on: settles at need, the line held 39 cycles;
  parent: runs to the replica cap, ends 6 against a need of 3); a scale-up
  whose joiners carry warm-up ITL (on: peak 6, need 6; parent: peak 33); a
  restart 2 min before a step (on: settles at need; parent: peak 40, ends
  over); a restart 15 s before a step that pushes every replica above the
  band (on: need 9, final 10, under need for 30 s, 7 cycles priced from
  above the band; with the fallback off, the negative control, final 4 and
  under need for 24m45s of the 25 min after the restart, as before the
  fallback). A replica at its cap with siblings below the band: the band
  still admits nothing (4b-3), and the fallback prices the variant from the
  capped replica's readings after its tenth.
- **Verbosity** (`throughput/replica_log_test.go`): the shipped-behaviour
  row's two tests. At the default level a run does not log more lines at 64
  replicas than at 8 (1 line at 64, the variant's GPS summary; 3 at 8,
  where the line is silent for its first cycles and logs that and its
  return); the parent's line logs 76 and 616. No line passes the CRI limit
  at the default or the verbose level.
- **Per-path counters** (`throughput.PathNames`): `throughput-itl-ols`,
  `throughput-itl-pinned`, `throughput-itl-held`,
  `throughput-itl-settling` and `throughput-itl-above-band`, the last not
  required of any golden (a recording reaches it only by starting into
  saturated load). The golden check requires the first three (the
  in-process goldens reach each); settling is zero there by construction and
  is required by `TestGoldens_ThroughputSweepFloors`, which skips, saying
  so, until the `throughput-sweep` recordings are listed in `goldens.yaml`,
  and then requires full, pinned and settling counts on both and held
  counts on the restart recording, with the parent's line (the steady gate
  off) as its negative control.

#### The GPS-mismatch clear: dropped in 4b-1

**Decision: the throughput line's window is no longer cleared on a GPS
mismatch.** The mismatch is still computed and logged, and the line keeps
its readings. It is a named change of its own in 4b-1, behind
`throughput-itl-keeps-the-window-on-gps-mismatch`, apart from the steady
gate. This revises the earlier "kept" for this clear; the shape-change
clear stays, through `Tracker.ClearWindow`.

**What it does today.** Each cycle `checkVariantGPSMismatch`
(`T/analyzer.go:855-939` at f7b4f7e47) compares, per replica at an instant k
of at least 0.30, the rate the line predicts, `k·C / KVreq / ITL(k)`
(`itl.TokenRate`), with the replica's `GenerationTokenRate`. Over 15 %
(`itl.DefaultGPSMismatchThresholdPct`) on any replica is a mismatch. Three
cycles in a row clear the variant's window (`consecutiveGPSMismatches`,
`T/analyzer.go:441-455`, `DefaultGPSMismatchClearThreshold`); B is kept.

**Why it existed, and why that is gone.** It came with the analyzer
(efca1b4cf, #1250). There a mismatch also suppressed spare capacity, and the
stale readings kept that suppression on for up to the window's 30 minutes;
the clear was added "to break calibration lock". The same PR then dropped
the suppression: `anyGPSMismatch` is a placeholder no output reads
(`T/analyzer.go:528-529`). Nothing is locked any more, so the clear only
throws readings away.

**It trips on a correct line.** On the committed recordings, the check was
run per Ready row with a known generation rate and ITL, at k >= 0.30, with
the replica's own observed ITL as the line: the best line there can be, so
every error left comes from the footprint model `k·C / KVreq` and the time
base, not from the fit. A clear is three such rows in a row on one pod.

| recording | rows | over 15 %, instant k (as today) | clears, instant k | over 15 %, minute-mean k | `k·C/KVreq` vs running, median / p90 |
| --- | --- | --- | --- | --- | --- |
| `e1` (aggregated decode, steady shape) | 596 | 7.4 % | 9 | 0.8 % | 2.5 % / 8.6 % |
| `e1-tuned` | 564 | 8.2 % | 10 | 1.4 % | 2.5 % / 8.5 % |
| `ols-sweep` (P/D decode, steps of load and shape) | 95 | 49.5 % | 13 | 36.6 % | 8.6 % / 246 % |
| `ols-sweep-slo` | 27 | 40.7 % | 2 | 7.1 % | 9.9 % / 24 % |
| `ols-sweep-slo-restart` | 66 | 53.0 % | 10 | 47.9 % | 26.7 % / 293 % |
| `pd-restart` (decode at its cap, queued) | 8 | 100 % | 2 | 100 % | 3069 % / 4283 % |

On a steady aggregated fleet a perfect line still clears the window about
ten times a run, from the instant k alone against a minute's rate. On a P/D
decode fleet the footprint model itself misses: across a step of load or
shape, and above all with requests whose KV landed and that wait to be
scheduled (`pd-restart`), the KV holds far more than the running sequences
`k·C / KVreq` assumes. A check that cannot pass on the true line is no test
of the line, and the clear it triggers is what kept the 4b-0 differential
test's window from filling until its rows dropped the generated-token rate.

**What 4b-1 does instead.** The comparison stays as a diagnostic, on the
minute-mean k where it was read (`KvUsageForITL`), and its per-pod INFO line
moves to the debug level with one summary per variant (the verbosity test,
shipped-behaviour table, would fail a per-replica line at the default
level). The window keeps its readings: a line that really moved is refitted
by the steady gate and the window's age and size, which is what row 1 does
and what no GPS signal ever told it. Kept in the switch-off path, the clear
is as today.

**Negative control.** A fleet whose generation rate disagrees with a
correct line by 30 % for three cycles: on the parent the window empties
(its sample count falls to 0); with the switch on it keeps every reading.
The step 4 count of the throughput recording (the recipe) is the run-level
check: with the switch on, the replay must keep at least as many full fits
as there were clears off.

The recipe keeps its step 4 grep: the recording is taken on 4b-0, where the
clear still runs, and its count of clears is the evidence above, measured
on the line itself.

#### 4b-2 as built

Status: built on `feat/learner-4b2`, on main after 4b-1 merged (#456,
1c9a34fe8). The line reads each replica's settle from the engine's
settle tracker (`T/settle.go`), which 4b-0 wired (`WithSettleView`) and
4b-1 withheld. An analyzer given no view -- a test that drives `Analyze`
alone, a replay of a recording with no `start.ThroughputConfig` -- observes
a tracker of its own, as the slo analyzer does, and stands for a controller
that has watched the fleet: nothing waits after a restart.

**The switches, split as 4a split them.** The table above names one switch
for the move, with "restart under its name, idle on evidence, the absence
tolerance, the Finite pod mean" inside it. Each of those moves the fleet on
its own, and 4a's review split them out for the saturation and slo lines
(`itl-restart-under-its-name-settles` and `itl-idle-needs-evidence` in
9b52db281). 4b-2 takes the same six, prefixed `throughput-`:

| switch | changes against 4b-1's own trace | which way the fleet can move |
| --- | --- | --- |
| `throughput-itl-reads-the-settle-tracker` | the settle start is the tracker's: joined, back past the tolerance (`settle.Rejoined`), lent, a new revision under the same name (`settle.RolledOut`), Ready again after a not-Ready row. Its own differences: a gap between two of the model's cycles past the tolerance (a paused controller, a clock jump) settles every replica, and a rollout that keeps the Pod's name settles; the own trace saw neither | fewer readings after a gap or an in-place rollout: **silent more often** (the risky side, 4.4), and a rollout's warm-up kept out: **down** |
| `throughput-itl-waits-after-controller-restart` | the engine's tracker is new with the controller and reads every replica on its first cycle as `settle.Joined`; on, those replicas wait `steadyFor` from that sight. Off, as 4b-1, they are taken as settled. Only with an engine view and on the tracker (the move on): the analyzer alone never waits, nor does the line's own trace. The above-band fallback's readings do not wait (round-1 review, below) | **silent** for `steadyFor` plus the ten readings in the band (cycle 8 on a three-replica variant at 15 s); a joiner whose warm-up straddles the restart is kept out: **down** against the parent's over-order |
| `throughput-itl-keeps-the-settle-through-a-missing-row` | one missing row keeps the settle up to the tolerance (2.5 intervals, at least 1 min). Off, as 4b-1, the replica starts over | more readings, **live more often**; a replica restarted inside the tolerance with nothing to show it (no not-Ready row, no new Ready time) is admitted while warm: **up**, the cost side |
| `throughput-itl-restart-under-its-name-settles` | a new Ready time under the same name settles. Off, only a not-Ready row shows the restart | a fast restart's warm-up kept out of A: **down** (the parent over-orders) |
| `throughput-itl-idle-needs-evidence` | idle only on evidence (KV known 0, and ITL known-empty or generation rate known 0; prefill by its request rate). Off, a row with neither ITL nor k is idle | a failed scrape no longer settles a busy replica: **live more often**; an idle replica whose gauges are unknown is not idle, so its return's warm-up is admitted: **up** |
| `throughput-itl-reads-the-finite-pod-mean` | a Pod with an Absent rank is no reading. Off, its Finite ranks' mean was one | fewer readings: **silent more often** on a fleet of such pods; and **either way** on the line: the pod's fill counts the Absent rank and its ITL does not, so the reading sat left of its line (an idle rank, A too steep, over-order) or right of it (a busy rank, A too flat, under-order) |

**A lend moves nothing on this line.** A warm-pool lend is a new key on the
tracker (`settle.Lent`), but the analyzer drops lent rows before the line
(`ownReplicasOnly`) and `itl.Tracker` refuses a `FromWarmPool` row, on either
path (`TestITLSettle_ALentPodIsNoReadingOnEitherPath`). **A rejoin** past
the tolerance settles on either path, as 4b-1's own trace with the absence
rule did (`TestITLSettle_AnAbsencePastTheToleranceStartsTheSettleAgain`);
the move's only rejoin of its own is the gap between cycles above.

**`throughput-itl-waits-after-controller-restart` is 4b-2's**, as the Order
table has it, not later: it is defined against the engine's view, which
4b-2 is the first to read, and with the view and the move on, leaving it
off would read a controller restart as a fleet that was watched.

Negative controls, each through `Analyze` on the engine's rig
(`T/settle_test.go`), run against 4b-1's tip with the switch names shimmed:
the restart, settling cycles `nil` against `[0..7]`; one missing row,
`[11..18]` against `[0..7]`; a new Ready time under the name, `nil` against
`[0..7, 10..17]`; a new revision under the name, the same; a 3 min gap
between cycles, the same; a scrape that read nothing, `[12..19]` against
`[0..7]`; a Pod with an Absent rank, 3 readings offered against 2. Each test
also runs its switch off on this tree and asserts the parent's result.

Property cases (`T/property_settle_test.go`, on the engine's rig, a new
tracker per controller): `transient/throughput-itl/controller-restart-with-a-joiner`
(a replica joins two cycles before a controller restart, its warm-up lasting
six cycles) fails on 100 of 100 seeds with the wait off and on 4b-1's tip
(seed 1: priced 0.2698 s against 0.0564 s, 378.6 %), and passes with it on;
`joiner-warm-up-on-the-engine-tracker` and
`missed-row-on-the-engine-tracker` pass on either path and are guards.

Closed-loop probe (`TestThroughputProbe_AControllerRestartOnTheEngineTrackerWaitsItsSettle`),
the restart case of 4b-1 on the engine's rig, need 9: a step 2 min after
the restart, final 10, under need for 30 s, silent 10 cycles (2.5 min)
from the restart on, against final 9, 0 s and 2 cycles with the wait off;
a step 15 s after, which pushes every replica above the band inside the
wait, final 10, under need for 30 s, silent 3 cycles (45 s), the same as
with the wait off. Both inside the probe's 5 min, on the SLO side as 4.4
says. Before the round-1 review the 15 s case was under need for 1m45s and
silent 8 cycles (2 min): the restart wait held the above-band fallback too.

**Goldens.** No committed recording gives the throughput analyzer the
engine's view (the in-process three predate `ThroughputConfig`), so the
restart wait moves none until `throughput-sweep-restart` lands, and is held
by its unit tests (`heldBy`). Measured after the round-1 review, each alone
against all off from main's goldens: none of the six moves a golden, and
all on reproduces main's goldens byte for byte, so each is held by its unit
tests. Before the review the move moved `acceptance/variant-capacities.csv`
(3 of 149 rows changed bytes) and `engine/variant-capacities.csv` (2 of
139, where this section said 1): on the cycles
after a clock jump past an hour, model-01, the first model analyzed, read
the gap on the analyzer's own tracker as an absence (`settle.Rejoined`) and
held its slope (`T2-held`) where the key's own trace refitted (`T1-ols`).
That was the eviction order, not the move (S3 below). The acceptance
fixture no longer reaches the held slope, which the engine fixture alone
reaches, as in 4b-1. The engine's own recording test
(`steadystate/learner_recording_test.go`), whose throughput analyzer has the
engine's view, prices 8 variants where 4b-1 priced 21: the band's line waits
after the controller start and after each day the clock jumps, as the slo
line does, and the 4 of the 8 that are pd-d's are priced from above the
band (`T2-above-band`) inside that wait. Before the review it priced 4.

**Round-1 review.**

- **S1: the above-band fallback does not wait after a restart.**
  Decision: the restart wait stamps only the band's readings. A reading
  the above-band window keeps is stamped as settling only for a settle that
  is not the restart's (`itl`'s `aboveBandSettling`): a joiner, a return
  after idling, or a replica that became Ready less than `steadyFor` before
  the cycle, by its Pod's Ready time, so a pod that just became Ready is
  still kept out. Why: the fallback pins B at the least ITL among its
  readings and prices at a k of at least 0.85, so it never prices a higher
  mu than the truth (4b-1 as built); a warm-up reading of a replica that was
  serving before the restart sits above the line and can only raise its
  price. Waiting `steadyFor` for it bought nothing and kept the line silent
  under need. Measured on the probe above, 15 s case: under need 1m45s and
  silent 8 cycles with the stamp, 30 s and 3 cycles without. Negative
  control: with the stamp put back, the probe returns to 1m45s and fails.
  The property case `known-truth/throughput-itl/above-band-fallback` (no
  engine view) still holds on 100 of 100 seeds; a new engine-rig case,
  `known-truth/throughput-itl/above-band-fallback-after-a-restart`, runs the
  same fleet from a restart, its first two cycles 30 % over the line, and
  holds the one-sided margin on 100 of 100 seeds with the fallback pricing
  inside the wait. With the stamp put back it fails on 100 of 100 (the
  fallback first prices on cycle 8). `itl`'s
  `TestTracker_AboveBandFallbackDoesNotWaitAfterARestart` holds both sides:
  cycle 3 with the replicas Ready an hour before, cycle 7 with them Ready
  15 s before the restart, on the key's own trace and on the tracker.
- **S3: the analyzer's own tracker after a long gap.** The own tracker
  forgets a model not analyzed for `ownSettleTimeout` (1 h), inside
  `observe`, after the model analyzed first that cycle has observed. So
  after a gap past an hour that model read the gap as an absence and
  settled, and every later model, evicted meanwhile, was first-seen and
  did not: the result depended on the order. `observeOwnSettle` now makes a
  model last seen more than `ownSettleTimeout` ago first-seen whatever the
  order: it forgets the model's observations, observes it backdated, and
  drops its line keys' replica traces (`itl.Tracker.ForgetTraces`), keeping
  their windows and held slope, as the eviction keeps them for that model.
  Part of the move: off, the line reads no tracker. Test:
  `TestITLSettle_AfterALongGapTheAnalyzerAloneSettlesEveryModelAlike`, two
  identical models and a 2 h gap; before the fix the first settled on
  cycles 8 to 15 (`pinned-B` for 11 cycles) and the second never. A no-op
  `observeOwnSettle` fails it and four other `TestITLSettle_` tests. The
  goldens came back to main's (above).
- **The slo analyzer has the same logic, not fixed here** (`slo/settle.go`:
  first-seen only on a model's first sight, forgotten after `stateTimeout`).
  Its eviction runs only from `EvictStale`, which the engine and the replay
  call at the top of the cycle, so there every model reads a long gap
  alike. A caller that never calls it, or calls it between two models'
  `Analyze` calls, gets the order or the gap read as an absence. Its
  switches and goldens are 4a's, so a follow-up issue carries it: make a
  model last seen past the timeout first-seen in its `observeOwnSettle`, as
  here.
- **N1: the restart wait reads the tracker only.** `WaitsAfterRestart` was
  set with an engine view whatever the move said, and on the key's own
  trace it made every replica of a key made anew wait `steadyFor`, not only
  a controller restart. It is now set only with the move on
  (`throughput.settleAdmission`). Four `TestITLSettle_` tests whose switch-off
  runs read the own trace now expect no restart settle there, and
  `TestITLSettle_TheOwnTraceDoesNotWaitAfterARestart` fails with the old
  setting. The saturation and slo analyzers set it as before.
- **N2:** `WithOptimizationInterval` sets the interval of the analyzer's own
  tracker too, so its absence tolerance follows the controller's interval.

**Round-2 review.** The own-tracker path after a long gap now treats a
model the tracker does not hold (never seen, or evicted inside another
model's `observe`) the same as a stale one, and forgets the traces of
every line key of the model by prefix, absent variants included, all under
`a.mu` and all behind `throughput-itl-reads-the-settle-tracker`. The order
and absent-variant tests are guards: the round-1 code also passed them,
because `observe` evicts a variant state at the same timeout and the line's
keys with it; the new path does not rest on the two timeouts staying equal.
Mutants killed: the stale path with the switch off
(`TestITLSettle_AfterALongGapWithTheMoveOffTheOwnTraceIsKept`, an idle run
before the gap that 4b-1's own trace keeps), the eviction keeping the
model on the tracker or keeping its stamp
(`TestITLSettle_TheEvictionForgetsAModelOnTheOwnTracker`), and no
`ForgetTraces` (four tests). The probe figures the reference pages quote
are pinned exactly, the control included (`itl.Admission.AboveBandWaitsAfterRestart`,
set only by negative controls): in the band, 30 s under need and 10 silent
cycles; above it, 30 s and 3; the stamp put back, 1m45s and 8. On the
settle rig, three steady replicas price first on cycle 8 after a restart
and one replica on cycle 10, not 8 + 10: readings taken during the settle
count towards the ten, and only three must be settled.
### PR 5 design: start time and the snapshot

Design for the row 5 above, checked against main f82a55480. D is
`internal/engines/analyzers/saturation`. Numbers marked *unmeasured* are
choices, not results.

#### What exists today

| part | where | what it does |
| --- | --- | --- |
| Sample | `internal/collector/pod_serving_state.go:220-245` | Pod Ready `LastTransitionTime` minus creation, 0 when not Ready or outside (0, 3600] s. A readiness blip re-stamps the transition, so an old pod can report its age |
| Fold | `D/start_est.go:86-206`, called once per variant per cycle (`D/analyze.go:496`), after a restore is installed (`D/analyze.go:50`) | Each pod once (`startSeenPods`, key `itlKey\|namespace\|pod`, `:119`), bridges skipped. The first sample **replaces** the prior (`:171`); later ones fold by EWMA α 0.3 (`:42`, `:165`). A sample over 3x the estimate is refused (`:54`, `:151-158`) until 3 refusals, counted **per refusal**, not per pod (`startOutliers`, `:62`) |
| The 70 s | `DefaultReplicaStartSeconds`, `D/start_est.go:35`, applied at `:190-191` | Reaches **only** the gauge and the `replica-start-measured` log. Sizing never reads it: `startSecondsPerRole` skips a variant with no measurement (`:263-266`) |
| Floor | `D/throughput_floor.go:519-521`, `:556`, `:627`; `internal/signals/floor/floor.go:250`, `:664` | Min over the role's measured variants, only while the arrival rate is a true arrival rate. Projects the backlog over the start (`backlogAtLanding`) and caps engine queues over `DrainHorizon`. No measured variant: no projection, `startSource: none` (`:737-751`) |
| Share pacing | `D/analyze.go:142` (`RoleStartSeconds`, max per role), `steadystate/composite.go:146`, `allocation/utilization_share_groups.go:908`, `allocation/utilization_share_timing.go:49`, `:113` | Slowest measured start, capped at 30 min. None measured: `shareModelLoadEstimate`, 5 min (`:14`) |
| Warm pool | `internal/warmpool` | Does not read it. Bridges are excluded from the fold |
| Seed | `registry/scalermeta.go:102-122` | `replicaStartSeconds` is declared, not parsed by `ParseMeta` (`:192`), read by nothing |
| Persistence | `D/learned_snapshot.go:118` (`startSeconds`), export `:605`, restore `:1410-1413` | Estimate per key only, installed when the key has none. `startSeenPods` and `startOutliers` are working state (`D/learned_maps.go:100-101`): after a restart every running pod is folded again |
| Metrics | `internal/metrics/metrics.go:1203`, `:1218` | `wva_replica_start_seconds` histogram; `wva_replica_start_seconds_estimate{source=measured\|seed}`, where `seed` today means the 70 s default |

So the row's "every estimate starts from 70 s" is true of the gauge only. A
variant with no measured start is not projected at all.

#### The changes

Each is a named change behind its own replay switch (2.5), on in the
constructor. The prior P is the seed when the ScaledObject sets one, else 70 s.
N = 3 distinct pods, and a seed's bounds are [S/2, 2S]. Both are named
constants in `D/start_est.go` (`startConfirmPods`, `startSeedLowFactor`,
`startSeedHighFactor`) whose comments say they are *unmeasured*: N matches
today's 3-refusal guard and run T's 4-5 pod scale-ups. An LWS group counts
once, as its leader's row.

Decided (review of this design): N = 3 and [S/2, 2S]; unseeded, unmeasured
variants stay unprojected; the gauge's sources become `default`, `seed` and
`measured`; a bad seed is ignored and logged, never a frozen model; the frozen
decoder pins v0.10.1; sizing never uses a start shorter than measured (C); a
kind recording, a GPU witness only later and optional; the split below.

**A. Read the seed** (`start-reads-replica-start-seed`). `ParseMeta` parses
`replicaStartSeconds` as a number in (0, 3600]. A bad value is ignored, as if
unset, and logged once each time the value changes; it is never a trigger
error: today any value is accepted, and a rejected trigger freezes the model. The engine puts it on
`domain.VariantReplicaState` (`StartSeedSeconds`), as `modelOutputSeed`
(`steadystate/engine.go:1105`) does for output tokens, so the recorder carries
it. A seeded variant with no measurement now reaches the floor and share
pacing at the seed.
- Effect: a seeded, unmeasured role is projected over the seed (was: not at
  all), so the floor can order **more** during its first scale-up, and share
  pacing uses the seed instead of 5 min (shorter or longer). Unseeded
  variants do not move: with no seed and no measurement a role stays
  unprojected, and the 70 s stays out of sizing. `startSource: seed` on
  `throughput-demand-floor`.
- Gauge `source` on `wva_replica_start_seconds_estimate`: `default` (the
  70 s, which today is labelled `seed`), `seed` (the ScaledObject value) and
  `measured`. `seed` changes meaning, so the PR rewrites its row in
  [prometheus.md](../reference/prometheus.md) and adds an upgrade note to
  [upgrade and uninstall](../reference/upgrade-and-uninstall.md): a query on
  `source="seed"` now finds only ScaledObject seeds.
- Negative control: seed 120, no start measured, arrival rate > 0. The
  parent projects nothing (`startSource: none`); the PR projects over 120 s.

**B. Confirm by distinct pod** (`start-confirms-by-distinct-pod`). A refused
outlier is remembered per key as a set of pods. The guard opens when N
distinct pods are over 3x; one pod repeating never opens it. The set is
pruned with the pods, as `startSeenPods` is. The guard compares a sample with
the measured part of the estimate only, never with P.
- Effect: one pod with a readiness blip no longer lifts the estimate on its
  fourth cycle, so the projection, and the floor, stop over-ordering on it
  (**down** only). `replica-start-outlier-rejected` gains `distinctPods` and
  `needed`.
- Negative control: estimate 70, one pod reporting 2700 s on 4 cycles. The
  parent folds it on cycle 4 (about 860 s); the PR keeps 70.

**C. Blend the first samples** (`start-blends-first-samples`). With n < N
distinct pods measured, mean m:

    estimate = ((N - n) * P + n * m) / N

With a seed S it is also held in [S/2, 2S] while n < N. With no seed it is
never clamped. From N pods on, the estimate is m and later pods fold by the
EWMA as today. The cases (2.6):

| case | n | estimate | must hold |
| --- | --- | --- | --- |
| seed 120, first sample 15 | 1 | 85 | in [60, 120] |
| seed 120, first sample 460 | 1 | 233.3 | in [120, 240] |
| seed 120, first sample 3600 | 1 | 240 (held) | in [120, 240] |
| no seed, first sample 15 | 1 | 51.7 | strictly in (15, 70) |
| no seed, first sample 463 | 1 | 201 | strictly in (70, 463) |
| no seed, 3 pods at 463 | 3 | 463 | equals the measurement |

The blend is the **published** estimate: the gauge, the log and the cases
above. The readers that size, the floor's backlog projection and share
pacing, take **max(blend, m)** while n < N, so sizing never uses a start
shorter than one measured. From N pods on they read the estimate. A seeded
variant with nothing measured is sized at the seed.

| reader case | n | published | sized at |
| --- | --- | --- | --- |
| no seed, first sample 460 | 1 | 200 | 460 |
| no seed, first sample 15 | 1 | 51.7 | 51.7 |
| seed 120, first sample 460 | 1 | 233.3 | 460 |
| seed 120, first sample 15 | 1 | 85 | 85 |

- Effect: estimates no longer take one pod raw, and 5c can only
  **over-order**, during a variant's first N starts: a fast variant (15 s) is
  projected over the blend, longer than measured, so the floor orders more and
  share moves wait longer; a slow one is sized at its measurement, as on the
  parent. `replica-start-measured` gains `pods`, `prior`, `priorSource`
  (`default` or `seed`) and `sizedSeconds`.
- Negative controls: no seed, one sample of 15 s: the parent's estimate is
  15, the PR's 51.7. No seed, one sample of 460 s: a reader that takes the
  blend alone sizes at 200 and fails the reader case.

**D. Counted pods survive a restart** (`start-counted-pods-restored`). The
snapshot keeps, for each counted pod, its Ready time. After a restart a pod
whose row carries the same Ready time is counted already and is not folded
again. A pod with no Ready time, or another one, is folded as today.
- Effect, snapshot on only: the restored estimate is no longer pulled toward
  the running pods' starts at α 0.3 each, and a pod that blipped before the
  restart is not read as a new start. Small; either direction.
- Negative control: restored estimate 70, four running pods reporting 90 s
  with their stored Ready times. The parent ends near 85; the PR stays at 70.

**E. First sight survives a restart** (`settle-first-sight-restored`, the
settle package's first switch, with its own hook). The snapshot keeps the
settle tracker's sighting per model and replica: `FirstSeen`, `ReadySince`,
`ReadySeen` (`internal/signals/settle/settle.go:209-246`). On restore a
sighting is installed only when the replica's first row is Ready with the same
non-zero Ready time. An engine restart, an LWS group restart, or a new lend
changes it, and that replica waits as today. Idle runs are not kept: a return
of load during the restart is missed, as today.
- Effect, snapshot on only: after a controller restart only replicas with no
  record wait (k2 1 min, ITL `steadyFor`, mu behind them). The k1 pricing
  after a restart (4.4) ends sooner, so the fleet is sized **lower** than on
  the parent during that window. `learner-start` counts restored sightings,
  and `settleUntil` covers only the cold replicas.
- Negative control: four replicas Ready for 1 h, controller restart with the
  snapshot on. The parent refuses every k2 reading (`P1-obs-unsettled`) for
  1 min; the PR admits them on the first cycle.

All five: start time joins `learn` as learner `start`, with `learner-write`
at V(3) and `restored: true` until its first admitted sample after a restart
(6.1). The "Learned state: snapshot restored" line adds `startKeys` and
`sightings`. Metrics: no new series; the gauge's `source` values become
`default`, `seed` and `measured` (A).
The closed-loop probe runs C for true starts of 15 s and 463 s, and E through
a restart, and the PR pastes both.

#### Persistence and back-compat

The document stays v1. New fields, all `omitempty`:

| field | in | holds | an old document without it |
| --- | --- | --- | --- |
| `startSamples` | `LearnedState` | per key: n (capped at N) and mean m | n = N: the estimate keeps full authority, as before. No fleet moves on upgrade |
| `startAdmittedAt` | `LearnedState` | per key: last admitted start (the deferred last-admitted stamp) | zero: as old as can be |
| `startCounted` | `LearnedState` | per counted pod (`itlKey\|ns\|pod`): its Ready time | every running pod is folded again, as today |
| `settle` | `learnedStateDoc`, beside `state` | per model: each replica's key and its three sighting times | every replica waits, as today |

- `startSeconds`, `priorK2` and `k2BorrowedSince` keep their names and are
  still written. The seed is never stored: it is read from the ScaledObject.
- The per-pod fields are the only ones that grow with replicas (about 150 B
  each). They are spare entries in `learnedStateEntries`
  (`steadystate/learned_state_store.go:581`), dropped first at the budget:
  a dropped one costs one replica one wait. Their times are fixed per
  sighting, so they enter the digest without rewriting every minute.
- Forget drops the new maps with the rest (`D/learned_maps.go` lists them).
- Fixtures: a v0.10.0 document (no `itlSlopes`) and a v0.10.1 one restore
  through the current path; the light-load restore keeps the ITL line for the
  v0.10.1 one.
- Rollback: `json.Unmarshal` (`learned_state_store.go:243`) ignores unknown
  fields, so v0.10.x reads PR 5's document and drops the new ones.

**The frozen decoder.** v0.10.1 is commit 760f76d76; v0.10.2 has the same
blobs for every file below, so one copy covers both. Built from git alone:

    git show v0.10.1:internal/engines/steadystate/learned_state_store.go         # learnedStateDoc, learnedStateWriter (blob d1f8d339c7a0)
    git show v0.10.1:internal/engines/analyzers/saturation/learned_snapshot.go   # LearnedState and its *Snapshot types (b8277be9e29d)
    git show v0.10.1:internal/engines/analyzers/saturation/forget_variant.go     # ForgetMarkSnapshot (d10d6a82f7cf)
    git show v0.10.1:internal/signals/capacity/rolling_snapshot.go               # RollingAverageSnapshot (a3157ef25c84)

Copy the struct declarations only (fields and tags, no methods, the embedded
`RollingAverageSnapshot` kept embedded), with a `v0101` prefix, into
`steadystate/learned_state_v0101_test.go`, its comment naming the tag, the
commit and the blobs. The test encodes a PR 5 document with every field set
through the production encoder, decodes it into the frozen type, and asserts
every field the frozen type knows comes back equal. A second assertion pins
the frozen type's JSON tag set to a literal list, so an edit to the copy
fails. Negative control: renaming `startSeconds` or dropping the `priorK2`
write fails it.

#### Recording

Start time is a pod-lifecycle signal: Pod creation to Ready. kind with the
inference simulator gives real samples, so **no GPUs are needed**. The
measured GPU distribution (15 s to 463 s) is already in the start-time
reference and enters as property cases.

From a build with PR 5a merged (the seed is in the recorded input), on kind,
with the recorder on, `LEARNED_STATE_SNAPSHOT=true` and `--v=3`:

1. One model, two variants: `fast`, readiness at about 20 s; `slow`, about
   240 s, its ScaledObject setting `replicaStartSeconds: "120"`.
2. Load that takes each variant to at least 4 replicas: N + 1 distinct
   starts each. About 15 min.
3. Optional: restart the engine process in one long-running pod (a container
   restart under the same name, the blip case). Skip if the simulator image
   cannot; the synthetic suite covers it.
4. When the ConfigMap holds both keys, copy the recordings out, then delete
   the controller pod under load. `kubectl rollout restart` alone loses them:
   the recording volume is an `emptyDir`.
5. 5 min after the restart, drop to light load (issue #316) and restart again
   the same way.
6. One more scale-up per variant after the second restart.

Capture: every recording; the ConfigMap before and after each restart; each
controller pod's full log to a file; `kubectl get pods -o json` each minute
(the Pods' own creation and Ready times are the independent witness).
Counters above zero: start samples written, start keys restored, sightings
restored. About 45 min. Optional: run the v0.10.0 and v0.10.1 images on the
same cluster for 5 min each with the snapshot on, and keep their documents as
the back-compat fixtures; hand-built ones from the frozen types also do.

Later and optional, after 5d: a GPU witness. One small model, one variant,
1 GPU per replica, 1 to 4 replicas, one restart; about 30 min and 4 GPUs on a
shared cluster, torn down on every exit path. No PR waits for it.

#### Split and order

| PR | content | fleet sizing |
| --- | --- | --- |
| 5a | Parse the seed and carry it in the input, read by nothing. The frozen v0.10.1 decoder and the v0.10.0 and v0.10.1 fixtures. Zero golden diff | none |
| (recording) | the run above, from 5a, base goldens committed first | — |
| 5b | B | down only, on a pod that blips |
| 5c | A and C together: the seed's bound is part of the blend, and the property cases need both. The gauge's source rename with its prometheus.md and upgrade notes. `startSamples`, `startAdmittedAt`, learner `start` | up only: during a variant's first N starts, and for a seeded, unmeasured role |
| 5d | D and E, snapshot on only. `startCounted`, `settle` | lower during the minutes after a restart |
| later, optional | the GPU witness | — |

5c carries the most change, all of it on the cost side: sizing reads
max(blend, m), so it never under-orders. It goes after 5b so the
confirmation rule it leans on is already proven.

#### 5a as built

Built on main 35bd3dd88 (after #462, R1), not on f82a55480, so line numbers
above are f82a55480's.

- **The seed.** `registry.ParseMeta` (`internal/registry/scalermeta.go`)
  reads `replicaStartSeconds` as seconds in (0, `MaxReplicaStartSeconds`],
  3600. Any other value (zero, negative, NaN, infinite, over the hour, not a
  number) leaves `Meta.ReplicaStartSeconds` 0 and is handed back in
  `Meta.ReplicaStartSecondsIgnored`; it is never a trigger error. An empty
  value is absent, as for the other keys.
- **Carried, not read.** `Engine.applyStartSeeds`
  (`steadystate/engine.go`), called in `prepareModelData` right after the
  states are projected from discovery, sets
  `domain.VariantReplicaState.StartSeedSeconds` and its presence bit
  `StartSeedSet`, from the variant's own registry entry. It is per variant:
  there is no largest-wins across a model's variants as for
  `defaultOutputTokens`; how roles combine is 5c's to decide. Both fields are
  tagged `learnrec:"omitzero"` (R1's tag), so a recording carries the seed
  in `Input.VariantStates` and an unseeded state is written as before.
  `ChangeReporter.ReportIgnoredStartSeed` logs "A variant's
  replicaStartSeconds was ignored" once each time the written value changes,
  and again if a fixed value turns bad again.
- **Zero golden diff.** Every variant state in the nine goldens (4256) reads
  back and is written again byte for byte
  (`learnrec.TestStartSeed_APre5aRecordingIsWrittenAgainByteForByte`).
- **The frozen decoder** is `steadystate/learned_state_v0101_test.go`, from
  the commit and blobs above. Three tests: v0.10.1 reads a document with
  every current field set and gets every field it knows back, equal; a field
  v0.10.1 does not know is left out while empty, checked inside each entry
  type v0.10.1 knows as well as at the top; and the frozen tag set is pinned
  to a literal.
- **The fixtures are hand-built**: no capture of either release's ConfigMap
  exists. `TestLearnedStateFixtures_Generate` builds them from the last
  learned state the mu-paths golden exported (a real analyzer's state),
  projected onto the frozen types and encoded with `json.Marshal` as
  v0.10.1 does; the v0.10.0 one drops `itlSlopes`, the one field v0.10.0
  (823ddcd74) lacks. They live in `steadystate/testdata/learned-state/`.
  Both restore through `restoreLearnedState` into a real analyzer with
  nothing aged, unusable or unattributable, and the analyzer's export
  holds every entry unchanged. The light-load restore that keeps the ITL
  line from the v0.10.1 fixture runs cycles, so it is left to 5d's
  restore-then-run. The optional recorded documents from the kind run may
  replace them.
- **Not unit-tested:** the `applyStartSeeds` call inside
  `prepareModelData`, which needs the API server. The kind recording is its
  witness: the seeded variant's states carry `StartSeedSeconds`.

## 8. Risks and limits

- **One shared bug.** A defect in the settle tracker reaches every learner at
  once. Its tests cover every case the four versions were fixed for: k == 0, KV
  zero with an ITL present, k > 0 with no ITL, one missing row on a busy
  replica, the KV and ITL queries both failing for two cycles on a busy fleet,
  an idle replica's empty ITL window, a P/D prefill pod under light load
  (instant KV 0 between requests, ITL always empty: it must not go idle while
  its request rate is above 0), a Prometheus brownout longer than 1 min with
  every pod still listed (including an idle-and-return inside it, with the
  expected behaviour being the trade PR 1 picks in
  [4.3](#43-what-the-tracker-owns)), the same brownout with no rows at all (a
  failed collection: held), an optimize interval over a minute, a restart under the same name, an LWS group with mixed Ready times,
  a missed cycle, a slow fill, a just-Ready pod, a bridge lend, clock skew.
- **A shared write path already changed a learner silently.** #282 turned #5's
  in-spacing fold from a max into a lower median, and then into the mean. #7 kept its max only through
  an empty pod name.
- **Self-sealing errors.** Each capacity learner is witnessed only when
  saturated, and the error that over-orders stops saturation. A too-low k2 or
  mu removes its own witness; for k2 it is permanent until forgotten
  ([3.4](#34-which-side-breaks-the-slo)). Validation residuals are one-sided by
  construction, and must not be read as two-sided.
- **Start time blends before it trusts (PR 5c).** For a variant's first N
  starts the published estimate leans toward the prior, but sizing reads
  max(blend, measured mean), so 5c can only over-order, and only then: a
  fast variant is projected over a longer start than it showed. It cannot
  size a slow variant shorter than measured
  ([PR 5 design](#pr-5-design-start-time-and-the-snapshot)).
- **The prefill bound** is reviewed before it moves: a double scrape once
  doubled its input, its max kept it, and `RaiseLast` keeps it past a reset.
- **The recorder can evict the controller** if pointed at `/tmp`. It is off by
  default and has its own volume and cap.
- **Open defects this does not fix:** instantaneous k paired with a rate[1m]
  ITL; shape dependence of the ITL line (#275). Speculative decoding is read
  per accepted token in the saturation analyzer since #274; the throughput
  and slo analyzers still read the step time.

## Deferred to the implementing PRs

Round-3 lows, each settled in the PR named.

| item | PR |
| --- | --- |
| Determinism beyond the clock: sorted output, a fixed float format, no state write that depends on map order (`roleDemand`, `pendingRestore`, `eppByRole`); the acceptance replay runs three times in one process as well as 30 days apart | 0a, 0b |
| The recorder volume's `sizeLimit` is the per-volume ceiling (two recordings at the cap) plus headroom for the export. The writer stops each recording at the cap and deletes the oldest closed recording before the ceiling | 0b |
| The export also pseudonymises args and config strings and borrowed-line donors, with a per-export key that is not committed | 0b |
| `learner-write`'s `pods` lists at most 16, lowest reading first, with `podsOmitted` | 2 |
| Forget reaches every learner through a plain list of learner names in `learn`, not a registry; it is a named change of its own; when no key remains, the variant's learner series are deleted | 2 |
| Each operator-column cell names its metric series, or "metrics: none" | each PR |
| A witness column per learner in 3.4 ("witnessed when", "non-saturating witness"), which also tells the probe which side to drive | 3 |
| 3.6's cycle-count clocks become durations, or record the cycle interval in the threshold | each learner's PR |
| The snapshot gains a last-admitted stamp for start time and B, as an additive `omitempty` field of v1; a document without it publishes 0 (as old as can be) | 5, or the first PR that publishes a start-time or B age, whichever is earlier |

## Review notes (round 1)

Four reviews of the first draft (architect, statistician, SRE, skeptic):

- **Direction enum dropped.** A sketch fed k2 readings 100k, 100k, 20k, then
  eleven at 100k. The window mean returns to 100 000. A clamped prediction
  stays at 73 333 for good.
- **No driver loop.** The k2 window changes between replicas of one cycle, and
  mu reads k2's write in the same cycle. A batch loop changes the median across
  replicas, and so the replica count.
- **Harness first.** The recorded runs lack instant KV, ReadySince and
  per-replica rates, and never reach OLS. The first draft's replay rule passed
  trivially on k2, mu and B.
- **Observability.** The first draft's per-key count line had no value, pod or
  previous value, and merged k2 sources that the runbook treats differently.
- **Corrections.** The decode-gate fix was reverted, the idle-room cut removed,
  and the pinned fit is a mean. The first draft missed hard edges, #15 and #16.

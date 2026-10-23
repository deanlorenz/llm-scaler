# One learner lifecycle for the analyzers

- **Status:** proposed. No code changed. It describes the tree after #272, #273
  and #282 have merged. Each section says which PR a behaviour comes from.
- **Problem:** the analyzers learn about a dozen quantities. Each one has its
  own way of accepting readings, fitting, forgetting and logging. The same
  defects were fixed in one learner after another, because none of them could
  reuse another's fix.
- **Decision:** one small lifecycle every learner goes through:
  observe, admit or reject with a reason, fit, predict with an age, check the
  prediction against what was seen, then refit, forget or hold. It also adds
  one shared tracker that says whether a replica has settled. The model, the
  windows, the units and the direction policy stay with each learner. For
  example, k2 may only fall.
- **Shape:** one package and six small PRs. The pilot is the ITL line. Each PR
  is replayed against recorded cluster runs and must make the same decisions
  as its parent, unless the PR names the change.

## Why

The two-namespace runs of October 2026 (issue #270, its rerun, and E1/E2) and
the reviews that followed found the same five kinds of defect in different
learners:

| defect class | where it hit |
| --- | --- |
| **A transient admitted as a reading.** Examples: a replica that just joined, the first minute of a load step, a slow fill. | ITL intercept learned from a ramp (#273). Pinned A taken from unsteady rows (#273). k2 taken at 8 % KV behind a 160-request step (#282). k2 recorded mid-fill on long outputs (#282). A replica just Ready counted as idle room (#282). |
| **The first sample adopted raw.** | The GPS calibration factor (#273, removed). A fresh pod's 0.0995 req/s became the bucket's mu (#282, E1). The start-time estimator's first measurement replaces the 70 s default. |
| **A stale value never refreshed.** | A measured mu outranked a derived one for the rest of a run (#282). The 6 ms default intercept stayed on every multi-replica fleet (#273). |
| **A missing reading read as a value.** | k == 0 meant "not collected" and was traced as zero load (#273). A NaN KV read as 0, and so as idle (#283 review). A missing instant series. |
| **One reading dominates.** | Least squares pulled by three joined-pod readings out of sixteen (#273, now a weighted median). The saturated throughput sample was the hottest replica (#282, now a lower median). One hot decode pod read as "decode saturated" (#282). |

Every fix above was written once, for one learner. The joined-replica test now
exists three times, with three windows and three restart rules (see
[Settled replicas](#31-settled-replicas)). That is the strongest single
argument for this proposal.

One rule from [the k2 design](../plans/analyzers/k2-capacity-model.md) governs
the rest: **errors are asymmetric**. A capacity that is too high breaks the SLO.
A capacity that is too low costs money. So k2 is never raised, floored, aged or
reset, and the only fix allowed is stricter admission. A common lifecycle that
cannot express "this learner may only fall" would be worse than none.

## 1. Inventory

D = `internal/engines/analyzers/saturation`. "Persisted" means it is in the
learned-state snapshot (`D/learned_snapshot.go`, ConfigMap `wva-learned-state`,
off by default).

| # | quantity | where | learns | admission | fit | memory and forgetting | persisted | findings that hit it |
| - | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | ITL line `ITL(k) = A·k + B` | `internal/signals/itl/` (`tracker.go` Observe, `steady.go`, `fit_pinned.go`, `model.go`). Wired in `D/analyze.go` `fitLines`/`noteITL` | A and B in s/token against KV fill k. Key per variant, decode only | Must be Ready, not from the warm pool, with finite ITL > 0. Needs k > 0 (#273). k from 0.15 to 0.80 is in band. Steady: the replica's k moved ≤ 0.10 over 2 min, and a replica that joined waits 2 min (#273). No rows are taken mid-rollout | OLS on the steady window when it holds ≥ 10 rows spread ≥ 0.30. Otherwise A by k-weighted median with B pinned to the learned B or the 6 ms default (#273). Refuses A ≤ 0 and a non-positive line | Main window 20 rows / 30 min. Steady window 40 rows, carried up to 5 min past the main window (#273). Sweep after 24 h unseen. A change of engine args stashes B | B and the B stash. Windows and traces are not | A1–A5, the LAD fit, 273-light F1 (A lags a shape change) and F2 (two levels at different shapes teach a wrong B, #274), H2 speculative decoding, H3 shape dependence |
| 2 | Shared and borrowed ITL line | `D/analyzer.go` `publishLine`/`borrowLine`, `internal/signals/capacity/fingerprint.go` | Another variant's line, used as this one's | `EnableLearnedStateReuse` (off by default), the same fingerprint, never its own line | none | Ages on `learnedAt`, the last write. A borrowed line may hold a fleet but never order replicas | yes (`ITLLines`) | — |
| 3 | k2, KV at saturation | `D/replica_capacity.go` `k2Chain` (P1-obs, P2-hist, P3, P4), `D/k2_settle.go`, `D/keys.go` `historyKey` | Tokens resident on the busiest rank of a queued replica. Bucket key includes the smoothed output length (#282) | Queue ≥ threshold. Refused when: downstream saturated, not admitted (bridge, not Ready), mid-rollout, above the physical ceiling, or unsettled (#282: joined under 1 m; a queue deeper than the batch needs the rate and residency at 0.7 of record, or residency flat ±25 % against a reading 1 m back with the queue standing throughout) | Mean of a 10-sample window per bucket | Never raised, floored or reset. A gap of 24 h starts a new episode. Evicted 7 d after the last read | yes, with args tags and stash | B1 key flapping, B3 load-step admission, imbalance probe F, settle hole (a fall), stats F1 slow fill, ADV-1 decode gate, ADV-3 rate escape, E1 joined pod |
| 4 | Borrowed k2 (lower bound) | `D/k2_borrow.go` | A sibling variant's lower k2 | Only when lower than the variant's own k2. KV total within 3 % | none | Held 30 m, then relaxed over 6 h of serving time. Re-arms on an args change or a figure ≥ 20 % lower | yes (`LowerBounds`) | — |
| 5 | Measured saturated throughput (mu) | `D/throughput_floor.go` `recordSaturatedThroughputFrom`, `D/throughput_admission.go` | Completions per second of a saturated replica, per shape bucket | Only on a P1-obs write. Refused on RequestRate ≤ 0, or below 0.5 of the derived mu (#282) | One sample a minute at most. Within the minute: max per pod, then the lower median across pods (#282). The read is the window median | Stale against derived: when they differ by more than 2x and the newest sample is ≥ 5 min old, derived wins (#282). Evicted 7 d after the last read | yes | B5 max fold, B7 double scrape, E1 0.0995 req/s, measured outranking derived |
| 6 | Derived mu | `D/mu_from_itl.go` `deriveMu` | Sequences at kPrice divided by ITL(kPrice), over the output length | Inherits the line's admission | Formula | None of its own. It is as fresh as the line | no | A1 (calibration, removed), every ITL finding |
| 7 | Prefill throughput bound | `D/prefill_bound.go` `notePrefillBound` | Prefill tokens/s of a saturated replica | `EnablePrefillThroughputBound`. Saturated, serving rows only | Window max. A lower reading is written back up (`RaiseLast`) | **May only rise**, the opposite of k2. Evicted with the bucket | with the throughput maps | Not reviewed. A rise-only capacity bound is the risky direction (see [Risks](#5-risks-and-what-not-to-unify)) |
| 8 | Fleet shape and shape change | `D/shape_change.go` `noteFleetShape`/`settleFleetShape`, `D/fleet_shape.go` | Rate-weighted mean input and output length and prefix hit rate. A change hold of up to 5 min | Tolerance band, anchored on the router's arriving prompt length | Weighted mean | Released once every variant has measured itself. Evicted after the bucket retention | no | B1 (the k2 key now reads the tracked length), adversarial 5 (5 m shape against the short window) |
| 9 | Decode-saturation memory | `D/roles.go` `roleSaturated`, `D/analyzer.go` `rememberDecodeSaturation` | Whether decode is saturated, which gates prefill learning | A Ready decode pod with a queue that is also full (#282) | none | Remembered 1 min | no | Imbalance fix 1, ADV-1 |
| 10 | Idle room | `D/idle_room.go` `noteIdleRuns` (#282) | Which replicas are reliably idle, so a pinned queue can move to them | Idle on every reading for 2 min with no gap > 1 min, against effective capacity. Not a warm-pool bridge. Prefill needs a rate on record | none | A run resets on any non-idle reading | no | 282-modeller H1 (just Ready), ADV-2 (0.30 of the cache), M1 (an instant KV dip) |
| 11 | Floor arrival rate and trust | `D/throughput_floor.go` `offeredArrivalRate`/`floorTrust`, `internal/signals/floor/` | Lambda and how far to trust it after a step | none | A blend from 0.5 to 0.8 of resident demand (#282) | none | no | B4, stats F2 cliff (now a blend) |
| 12 | Replica start time | `D/start_est.go` `noteReplicaStart`, sample from `internal/collector/pod_serving_state.go` | Seconds from creation to Ready, per variant | Each pod once. Not from the warm pool. 0 to 3600 s. A sample above 3x the estimate is refused until 3 in a row | EWMA, α 0.3. **The first sample replaces the default** | Pods forgotten when gone. The estimate goes with the variant | yes | C3 (namespace). The first sample adopted raw (same class as A1) |
| 13 | SLO analyzer line | `internal/engines/analyzers/slo/` `fitLine`, `price.go` | Its own ITL tracker per variant and role, priced at the TPOT-target fill | Same tracker, same rules as #1 | Same | Evicted after 24 h unseen. No engine-args reset | no | Same as #1 |
| 14 | Utilization-share need history and swing | `internal/engines/allocation/utilization_share_ledger.go` (`RecordNeeds`, `rederive`, `Swinging`), PR #272 | Mean need over the swing window; whether a role's transfers keep changing direction | A landed move or a cancel, counted as the move it would have been. A spare return (`Entitled`) does not count (#272) | Arithmetic mean | Needs trimmed to the window. Moves and cancels kept 2x the window | need history only, in `wva-utilization-share-history` | 272 F1 and F2 (cancels counted as reversals, fixed in #272) |

The snapshot (`D/learned_snapshot.go`, `internal/engines/steadystate/learned_state_store.go`)
is not a learner. It carries #1, #2, #3, #4, #5 and #12 across a restart, with
their clocks. A restore never makes a figure younger.

Fourteen quantities. Eleven learn from replica readings (#1, #3, #5–#13; #6 through
#1's line). Two are borrowing rules over other variants' learned values (#2,
#4). One learns from the controller's own decisions (#14).

## 2. The lifecycle

Every learner goes through the same steps each cycle:

```text
observe   -> readings for this cycle, each tagged with its replica and the
             time base of each value (instant, max over 1m, rate over 1m)
admit     -> each reading is admitted, or rejected with one reason from a
             closed set
fit       -> the model from the admitted readings in its window
predict   -> a value, its source, its age, and how many readings back it
validate  -> the prediction against this cycle's witness observation
decide    -> keep, refit (drop the old window), forget (back to the fallback)
             or hold (keep, and say so)
```

As Go, the shared part is small. The model stays generic:

```go
// Verdict is what admission says about one reading.
type Verdict struct {
	Admit  bool
	Reason Reason // e.g. ReasonJoined, ReasonUnsteady, ReasonNoValue, ReasonImplausible
}

// Prediction is a learner's current answer, with the evidence behind it.
type Prediction[M any] struct {
	Model   M
	Source  string    // "own", "pinned", "borrowed", "derived", "default"
	Samples int
	At      time.Time // when the newest admitted reading was taken
	Used    time.Time // last read, for learners that age on use
}

// Policy is what each learner declares and the loop enforces.
type Policy struct {
	Direction Direction     // Free, FallOnly (k2), RiseOnly (prefill bound)
	MaxAge    time.Duration // 0 = never ages
	AgeOn     AgeClock      // LastWrite or LastUse
	OnDrift   DriftAction   // Refit, Forget, Hold
}
```

**Each learner keeps:**

- its model and fit;
- its windows and units;
- its admission tests beyond the shared ones;
- its policy;
- its witness, meaning which observation checks the prediction.

**The loop owns:**

- the order of the steps;
- the reason counters;
- the log line and the metrics;
- enforcing `Direction`. A FallOnly learner's refit can never return a higher
  value than it held. A FallOnly learner with a higher fallback cannot be given
  `Forget`. The type rejects that combination, so the k2 hazard cannot be
  configured back in.

The witness per learner:

| learner | prediction | witness | today |
| --- | --- | --- | --- |
| ITL line | ITL at the observed k | the observed ITL | `itl-gps-mismatch`, diagnostic only |
| measured mu | mu | derived mu | yields after 2x and 5 min (#282) |
| derived mu | mu at kPrice | tokens served by a saturated replica | none |
| k2 | resident tokens at the limit | tokens of a queued, settled replica | none. Admission only |
| start time | seconds to Ready | each new start | the 3x outlier rule |

## 3. Shared building blocks

### 3.1 Settled replicas

Today three components decide whether a replica has settled. Each has its own
rules:

| | ITL steady gate (#273) | k2 settle memo (#282) | idle runs (#282) |
| --- | --- | --- | --- |
| where | `internal/signals/itl/steady.go` `replicaTrace` | `D/k2_settle.go` `settleMemo.readySince`, `pods`; `D/throughput_admission.go` `noteReady` | `D/idle_room.go` `noteIdleRuns`, stored in `settleMemo.idle` |
| joined wait | 2 min (`DefaultSteadyFor`) | 1 min (`JoinedReplicaWindow`) | 2 min of idle readings (`IdleSettledFor`) |
| gauge judged | instant k, drift ≤ 0.10 over 2 min | resident tokens, ±25 % against a reading 1 min back, queue standing | busiest-rank KV under 0.30 of effective capacity, no queue |
| present on the first cycle | taken as steady | taken as established | no exemption: the run must build |
| a missed cycle | trace dropped, so the replica waits again | kept up to 2 min | a gap > 1 min resets the run |
| missing value | k ≤ 0 is not traced (#273) | tokens ≤ 0 not judged | NaN read as 0, so idle (#283 review) |

They answer one question about one replica: has this gauge held still, for this
long, since it was Ready? Proposed: one tracker in a new package
`internal/signals/settle`. It is keyed per model and replica and fed once per
cycle:

```go
tr.Note(pod, now, settle.Reading{Ready: r.Ready, Bridge: r.FromWarmPool, Values: map[settle.Gauge]float64{...}})
ok, reason := tr.Settled(pod, now, settle.Rule{Gauge: settle.KvInstant, For: 2*time.Minute, Band: settle.Abs(0.10)})
```

The tracker owns:

- **Ready since.** Prefer the Pod's Ready `LastTransitionTime`. The collector
  already reads it for the start-time sample. With it, a controller restart no
  longer exempts a pod that turned Ready seconds before. First-seen-Ready
  becomes the fallback.
- **A missing value is not a value.** Zero, NaN, Inf or an absent series all
  give "no reading". That neither judges a replica nor restarts its trace.
- **Gaps.** Any gap longer than the rule's period restarts the judgement.
- **Lookback.** The rule compares against the reading one period back, not one
  cycle back. Cycle-to-cycle readings of one-minute peaks share 45 s, which is
  how #282's first settle test let residency grow 2.4x a minute (stats F1).

Each learner keeps its own numbers. ITL's 0.10 of k over 2 minutes and k2's 25 %
of residency over 1 minute measure different gauges on different time bases.
The proposal unifies the mechanism, not the thresholds.

### 3.2 Plausibility against the prediction

Several fixes check a reading against what is already known. Mu must be at
least 0.5 of derived. k2's rate path needs 0.7 of the k2 on record. The start
time refuses samples above 3x the estimate. Make this one helper: a band around
the current prediction, with a "consecutive refusals before accepting" count so
that a real change gets through. The start estimator's 3-in-a-row rule is the
working example. The band and the count are per learner. A FallOnly learner's
band may be one-sided.

### 3.3 Robust fits

Offer the fits that already exist, written once:

- weighted median of per-reading slopes (the pinned ITL fit, #273);
- lower median of per-replica values (saturated mu, #282);
- trimmed mean.

The default for a new learner is a median. **Existing learners keep their fit**
until a replay shows a change is safe. k2's window mean is part of its
calibrated behaviour.

### 3.4 The first sample

No learner adopts its first reading in full without saying so. A learner
either needs N readings before it predicts (ITL needs 10, mu needs 2 to order),
or it reports `Source: "first"`, and the decision layer may treat that as
borrowed: it may hold a fleet but not order one. The start-time estimator is the
one learner still taking its first sample raw.

### 3.5 Drift and refit triggers

Validation keeps a short record of signed residuals (witness minus
prediction). Drift is K of the last N residuals beyond the band on the same
side. What happens on drift is the learner's `OnDrift`:

- **ITL line: Refit.** Drop the main window's rows that are older than the
  drift. This also answers 273-light F2: a shape change detected by #8 is a
  drift event for the line.
- **Measured mu: Forget** to derived. #282's 2x and 5-minute rule becomes this
  learner's drift rule.
- **k2: Hold.** Log and count only. A FallOnly learner moves only through
  admission.

The first PR for each learner ships validation **logged only**. A drift action
is switched on in a later PR, once the replays show what it would have done.

### 3.6 Staleness

Today ages are measured against different clocks. The k2 and mu windows age on
last use, ITL lines on last write, start estimates with the variant. Each
learner states `AgeOn` and `MaxAge` in its policy. The loop reports the age in
every prediction, so a stale figure is visible in the cycle line. Today it is
silent, as the 6 ms default was.

### 3.7 Logging and metrics

- **One line per learner per key per cycle:** `learner-cycle` at V(2). It
  carries `learner`, `namespace`, `variant`, `key`, `offered`, `admitted`, a
  count per rejection reason, `source`, `samples`, `ageSeconds`, `residual`.
  It replaces `itl-window`, the per-replica `k2-decision` rejections and
  `saturated-throughput-refused` as a count.
- **One line per reason class when it first appears for a key**, and again
  when it clears. Not every cycle. Default log volume is already about 2.2 KB
  per pod per cycle.
- Every line names the namespace (#271, C3).
- **Metrics:**
  - `wva_learner_readings_total{learner, reason}`, where reason includes
    `admitted`;
  - `wva_learner_prediction_age_seconds{learner, source}`;
  - `wva_learner_drift_total{learner, action}`.
  
  These carry no pod label. Variant labels follow the vocabulary in
  [Every signal as a metric](signals-as-metrics.md).

### 3.8 Snapshot

Each learner implements `Snapshot() (Entry, bool)` and `Restore(Entry, now)`.
Today the snapshot code reaches into the analyzer's maps. The document format
(`v1`), the per-variant fingerprint check (whole / args / prior) and the rule
that a restore never makes a figure younger stay as built. The settle tracker
and the validation residuals are not persisted: after a restart, Ready since
comes from the Pod.

## 4. Migration

Each step is one PR. All of them start after #272, #273 and #282 have merged.
Each one carries:

- **Replay fixtures.** Per-cycle inputs reduced from the recorded controller
  logs of E1, E2 and the issue #270 rerun, as CSV under the package's
  `testdata/`, the way `D/testdata/k2-fill/` is today. The extractor (today a
  scratch script) moves into `hack/` with the first fixture.
- **A decision test.** The replay is run on the PR and on its parent. Every
  priced figure and every replica count must match, except the changes the PR
  names. Each named change must fail on the parent: that is the negative
  control.
- **A count check.** Each replay asserts how many cycles it ran, so an empty
  replay cannot pass.

1. **Package, no users.** `internal/signals/learn` (verdicts, reasons,
   prediction, policy, the loop, the counters, the log line, the metrics) and
   `internal/signals/settle`. Unit tests only.
2. **Pilot: the ITL line (#1, #13).** The steady gate moves onto
   `settle`. Admission reasons become the shared counter. `itl-window` becomes
   `learner-cycle`, and `docs/reference/cycle-log.md` is updated in the same
   PR. Validation is logged from `itl-gps-mismatch`'s residual. Replay: the E1
   and E2 tracker inputs (the harness that produced the LAD numbers in #273)
   must give the same A and B on every cycle.
3. **Measured and derived mu (#5, #6).** The joined test, RequestRate > 0 and
   0.5 of derived become shared admission. The 2x and 5-minute yield becomes
   the drift rule. Replay: #282's `TestE1Replay_*` and `TestE1_*`, extended to
   the full E1 and E2 fixtures. sg2's mu must never take 0.0995.
4. **k2 (#3, #4), policy FallOnly, OnDrift Hold.** The settle test moves
   onto `settle` with its own rule (1 min lookback, ±25 %, queue standing).
   Ready since comes from the Pod. Replay: `k2-fill` traces, E1, and the #270
   rerun. An added assertion: no admitted k2 write raises the bucket's value
   above its parent-run value on any cycle.
5. **Idle room and the decode gate (#9, #10).** Both move onto `settle`. This
   fixes the NaN-reads-idle defect for free. Replay: #282's imbalance specs.
6. **Start time (#12) and snapshot (#3.8).** The first-sample rule (3.4) and
   the outlier band (3.2) become shared. Snapshot entries move behind the
   learner interface. A snapshot round-trip test runs over every learner.

Out of scope until later: drift actions other than Hold, any fit change, and
#7, #8, #11 and #14 (see the next section).

## 5. Risks and what not to unify

- **A refactor that changes decisions.** This is the main risk. Every step is a
  move, not a fix, and the replay decision test is the guard. A PR that must
  change a figure says so and carries its own negative control.
- **Direction policies stay separate.** k2 falls, the prefill bound rises, the
  start time moves both ways. The prefill bound's rise-only max is the
  dangerous direction for a capacity, and nobody has reviewed it. Inventorying
  it here does not endorse it. Review it before it joins.
- **Thresholds stay per learner.** One settle mechanism with one set of
  numbers would either hold ITL readings out for too long or let k2 in too
  early.
- **Fits stay per learner** until a replay justifies a change. Replacing k2's
  mean with a median moves every k2-priced fleet.
- **One shared bug.** With a shared settle tracker, a defect in it reaches
  every learner at once. Its tests must include every case each of the three
  current versions was fixed for: k == 0, a restart, a missed cycle, a slow
  fill, a just-Ready pod, a warm-pool bridge.
- **Not learners:**
  - The utilization-share swing (#14) stabilizes the controller's own
    decisions and does not learn from replica readings. It adopts only the
    log and metric conventions.
  - The floor's trust blend (#11) is a weighting, not a fit.
  - The shape tracker (#8) is a detector that other learners consume. It
    reports into the loop as a drift source, but its own logic stays where it
    is.
- **Generic code.** The type parameters stay on `Prediction` and the loop.
  Learners stay plain structs. If a learner reads worse after its PR than
  before, that PR is wrong. See [Structuring the analyzers](analyzer-structure.md),
  which measured how expensive these files already are to read.
- **Open defects this does not fix:**
  - instantaneous k paired with rate[1m] ITL (A5). The reading's time-base tag
    makes it visible to admission, but the fix is a collector change;
  - speculative decoding (H2);
  - shape dependence of the ITL line (H3, #274).

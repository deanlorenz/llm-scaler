# Shared rules for the analyzers' learners

- **Status:** proposed. No code changed. It describes main (with #272 and #281)
  plus the heads of #273 (3e611e3fb), #282 (0422dc146) and #283 (40762ccd1).
- **Problem:** the analyzers learn about fifteen quantities. Each one accepts
  readings, fits, forgets and logs in its own way. The same defects were fixed
  in one learner after another, because no learner could reuse another's fix.
- **Decision:**
  1. Build the test harness first. Every later step is judged by it.
  2. Share one **settle tracker**, one **vocabulary**, and one **set of
     observability rules**. Do not share a loop that drives the learners, a
     direction enum, or a robust-fit library.
  3. Fix the clock mix before anything moves.
- **Shape:** six PRs. PR 0 is the harness. Each later PR says what changes for
  an operator.

## Why

The two-namespace runs of October 2026 (issue #270, its rerun, E1 and E2) and
the reviews of #273 and #282 found six kinds of defect:

| defect class | where it hit |
| --- | --- |
| **A transient admitted as a reading.** A replica that just joined, the first minute of a load step, a slow fill. | ITL intercept learned from a ramp (#273). k2 taken at 8 % KV behind a 160-request step (#282). k2 recorded mid-fill on long outputs (#282). A replica just Ready counted as idle room (#282). |
| **The first sample adopted raw.** | A fresh pod's 0.0995 req/s became the bucket's mu (E1, #282). k2 prices its first admitted reading at once. The start-time estimator's first measurement replaces the 70 s default. |
| **A stale value never refreshed.** | A measured mu outranked a derived one for the rest of a run (#282). The 6 ms default intercept stayed on every multi-replica fleet (#273). |
| **A missing reading read as a value.** | k == 0 meant "not collected" and was traced as zero load (#273). A NaN KV read as 0, and so as idle (#283 review). |
| **One reading dominates.** | Least squares pulled by three joined-pod readings out of sixteen (#273: now dropped by state, then a k-weighted mean). The saturated throughput sample was the hottest replica (#282: now a lower median, bounded by the served rate). |
| **A hard edge with no hysteresis.** The most frequent class. | The k2 key boundary swung capacity 39 % at constant load. Demand fell 48 % for a 2 % rise in lambda. Mu flipped between measured and derived 11 times in 20 cycles under 4 % noise. `backlogGrew` swung demand 1.54x on one request (fixed on #282 by e7d9f0ff1). |

The joined-replica test now exists four times, with four sets of rules (see
[Settled replicas](#4-settled-replicas)): the case for one settle tracker. The
last class no shared code fixes. Only a test that perturbs the inputs finds it,
so the harness comes first.

**Errors are asymmetric.** A capacity that is too high breaks the SLO. One that
is too low costs money. [The k2 design](../plans/analyzers/k2-capacity-model.md)
draws its rule from that: nothing outside admission raises k2. No floor, no
age-out, no reset. That rule stays.

## 1. Inventory

D = `internal/engines/analyzers/saturation`. "Persisted" means it is in the
learned-state snapshot (`D/learned_snapshot.go`, ConfigMap `wva-learned-state`,
off by default).

| # | quantity | where | admission | fit | memory and forgetting | persisted |
| - | --- | --- | --- | --- | --- | --- |
| 1 | ITL line `ITL(k) = A·k + B`, saturation analyzer | `internal/signals/itl/`, wired in `D/analyze.go` | Ready, not a bridge, finite ITL > 0, k > 0. Each reading goes to the main window, the main and steady windows, or nowhere (see [Admission shapes](#33-admission-shapes)). A reading taken while its replica settles (joined, or load returned after idle, under 2 min) is stamped and kept out of the pinned fit | OLS on the steady window at ≥ 10 rows spread ≥ 0.30. Otherwise A = k-weighted mean of `ITL − B` over unstamped readings, B pinned. Fewer than 3 such readings: the last A is held (`SlopeHeld`, not persisted). A < 0 refused | Main 20 rows / 30 min. Steady 40 rows, carried 5 min past the main window. Swept 24 h unseen. Args change stashes B | B and its stash |
| 2 | Shared and borrowed ITL line | `D/analyzer.go` `publishLine`/`borrowLine` | `EnableLearnedStateReuse` (off), same fingerprint | none | Ages on last write. May hold a fleet, never order one | yes |
| 3 | k2, KV at saturation | `D/replica_capacity.go` `k2Chain`, `D/k2_settle.go`, `D/keys.go` | Queue ≥ threshold; downstream not saturated; not a bridge; not mid-rollout; under the physical ceiling. Joined under 1 min: refused. When queue × footprint exceeds what the busiest rank holds, also: rate ≥ 0.7 of mu on record **and** residency ≥ 0.7 of k2 on record, **or** residency within ±25 % of the reading 1 min back with the queue standing on every cycle | Mean of a 10-sample window. Admitted readings raise it as well as lower it. The first admitted reading prices at once | Key: the fleet's tracked output length for decode and aggregated, prefill's own average. 24 h write gap starts a new episode. Evicted 7 d after last read | yes, with args tags and stash |
| 4 | Borrowed k2 (lower bound) | `D/k2_borrow.go` | Only when lower than the variant's own k2. KV total within 3 % | none | Held 30 min, then relaxed **upward** over 6 h of serving time. Re-arms on an args change or a figure ≥ 20 % lower | yes |
| 5 | Measured mu | `D/throughput_floor.go`, `D/throughput_admission.go` | Only on a P1-obs k2 write, so it inherits k2's joined test. RequestRate > 0. Not under half the highest rate a Ready sibling served (`servedBounds`). Not under 0.5 of derived, unless the same pod read that low on every saturated cycle for 2 min | One sample a minute. Within the minute a reading rewrites the last sample: max per pod, lower median across pods | See [the mu choice](#the-mu-choice) below. Evicted 7 d after last read | window yes, choice no |
| 6 | Derived mu | `D/mu_from_itl.go` `deriveMu` | none: a formula on #1 per replica | none | As fresh as the line | no |
| 7 | Prefill throughput bound | `D/prefill_bound.go` | `EnablePrefillThroughputBound`. Saturated, serving rows. No joined test, no rate check, no band | Window max. It writes through #5's fold with pod `""`, so it kept its max only because every prefill pod folds under one empty key | **Only rises.** Touched on every read, so never ages while pricing | with #5 |
| 8 | Fleet shape and shape-change hold | `D/shape_change.go`, `D/fleet_shape.go` | Tolerance band on the router's prompt length | Rate-weighted mean | Hold up to 5 min | no |
| 9 | Decode-saturation memory | `D/roles.go` `roleSaturated` | One full decode pod gates prefill learning (the #282 fix that changed this was reverted, 7ab00fa8c). Bridges count | none | 1 min | no |
| 10 | Idle room | `D/idle_room.go` | Decode only. Idle on every reading for 2 min, no gap > 1 min, not a bridge | none | Run resets on a non-idle reading | no |
| 11 | Floor arrival rate, trust and backlog memory | `D/throughput_floor.go`, `internal/signals/floor/` | none | Blend from 0.5 to 0.8 of resident demand. A queue counts as grown past a noise band over a minute (#282). An engine's queue is backlog only up to what it drains (#282) | Last cycle's backlog per role | no |
| 12 | Replica start time | `D/start_est.go` | Each pod once, not a bridge, 0–3600 s. A sample over 3x the estimate is refused until 3 in a row, counted per variant **by cycle**: one pod can pass it alone in a minute | EWMA α 0.3. The first sample replaces the default | Seen pods not persisted: after a restart every running pod is folded again. The floor reads it as a min per role, share pacing as a max | yes |
| 13 | SLO analyzer line | `internal/engines/analyzers/slo/` | Its own ITL tracker, same rules as #1 | Same as #1 | 24 h unseen | no |
| 14 | Share need history | `internal/engines/allocation/utilization_share_ledger.go` `RecordNeeds`, `PeakNeed`, `PeakKnown` | A peak needs two readings in a row. After a restart a full swing window before a peak is known | Mean over the swing window. Step detector `ShareStepSpread` 0.25 | Trimmed to the window | yes, `wva-utilization-share-history` |
| 15 | Share release time | same file, `MeasuredRelease` | Completed releases | 90th percentile of recent releases, needs ≥ 3 | Recent releases only | no |
| 16 | ITL line, throughput analyzer | `internal/engines/analyzers/throughput/analyzer.go` `resolveITLModel` | The window's k band only | OLS, else B pinned to `lastFittedB` | Per variant | no |

#### The mu choice

A measured mu and a derived mu are both offered every cycle, and
`measuredYieldsToDerived` picks one. It keeps the measured window either way.

- A measurement under derived yields **at once** while a Ready sibling serves
  more than 2x it.
- Otherwise it yields only when stale. Freshness is the last saturated reading
  that **agreed** with it within 2x, not its newest sample. A stale measurement
  above derived yields after 5 min. One below derived yields after 30 min.
- The choice is latched: out at 2.0, back at 1.6.

Open: the latch is not persisted, so a restart reverts a yield in the 1.6–2x
band. A one-replica variant with no line has nothing to yield to.

Also learned and not tabled: the hot-rank hysteresis memo and the share swing,
which stabilize the controller's own decisions rather than learn from replicas.

The snapshot carries #1–#5 and #12, plus the accelerators, the forgotten set,
`K2BorrowedSince`, `PriorK2` (read by rollback builds) and both stashes. A
restore never makes a figure younger.

Row 16 has none of #273's fixes (steady gate, settling stamp, k > 0 guard). It
moves with row 1.

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

### 2.2 What PR 0 adds

- **A recorder.** Behind a flag, the analyzer writes its per-cycle inputs as
  JSON lines: every `ReplicaMetrics` field it reads (instant and peak KV with
  presence, ReadySince, the warm-pool flag, per-replica generation and
  completion rates, prefill rate, queue, ITL, KV capacity), the policy, and
  `now`. `docs/developer-guide/testing.md` says how to turn it on for a run.
- **Replay with goldens.** A replay drives the analyzer from a recording and
  compares its outputs (priced figures, rc and sc, window contents) to golden
  CSV committed beside the fixture. A `make` target regenerates the goldens, so
  each named change shows as a golden diff. A replay is open loop: it compares
  up to the first divergence and reports the rest as "diverged, not compared".
- **Per-path counters that must be above zero.** P1-obs admitted, settle
  refusals per reason, mu samples written, mu yields, OLS cycles, pinned
  cycles, instant-KV rows, joined refusals with a known ReadySince. A replay
  that touched none of a PR's paths fails rather than passes.
- **Property tests, one per defect class.**
  - Transient, first sample, stale value, missing value, one dominant reading:
    a synthetic case each, run against every learner it applies to.
  - **Hard edges:** perturb each input by a few percent of noise and jitter.
    The output may move by a bounded amount and must not alternate cycle to
    cycle.
  - **Monotonicity:** demand does not fall as lambda or the queue rises. The
    order stays bounded by what the fleet can drain.
- **Recorded runs needed before the k2 PR:** one with flow control off and one
  P/D run, both with the recorder on.

Holds, latches and the self-sealing low mu are closed-loop behaviours. A log
replay cannot show them. A PR that changes one also needs a closed-loop probe,
as #282's review used.

## 3. A shared vocabulary, not a shared loop

### 3.1 The terms

Docs, logs and reviews describe every learner in the same steps: **observe**,
**admit** or refuse, **fit**, **predict** with a source and an age,
**validate** against a witness, then **keep, refit, forget or hold**. No code
drives learners through them. The learners read each other mid-cycle in a
fixed order, and a generic loop would change it.

### 3.2 The order the learners depend on

`Analyze` runs `fitLines`, then `priceReplicas` (once per replica), then
`applyFloorAndHolds`. Inside that:

| edge | what reads what | which value |
| --- | --- | --- |
| ITL → derived mu → mu admission | mu's 0.5-of-derived test reads the line | this cycle's line (fitted first) |
| k2 → mu observation | a mu sample is written only beside a P1-obs k2 write | the same replica, same cycle |
| mu → k2 settle | k2's rate path reads mu on record | the record as that replica is priced, earlier replicas of this cycle included |
| k2 window → later replicas | each replica's P1-obs writes the window, the next replica is priced from it | then the median across replicas |
| mu choice in the floor | measured or derived is picked after every replica is priced | this cycle's figures |

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

Each learner declares which side of its error breaks the SLO, and which moves
toward that side need evidence. The constraint applies to **admission and
forgetting**, never to the fit.

| learner | side that breaks the SLO | moves toward it allowed without new evidence |
| --- | --- | --- |
| k2 | too high | none. No floor, age-out, reset or forget. The window mean still rises on admitted readings |
| borrowed k2 | too high | the upward relaxation over 6 h of serving time |
| measured mu | too high | yielding to a higher derived figure, after 30 min stale (5 min toward a lower one) |
| ITL line and derived mu | A or B too low | none beyond refit on admitted readings |
| prefill bound | too high | its window max only rises. This is the risky direction and it is unreviewed |
| start time | too low | none. Its consumers decide: the floor reads a min, share pacing a max |

A clamp that stops k2's fit from rising would turn it into a running minimum:
one admitted low reading would then price the fleet for good.

### 3.5 Clocks

Five clocks are in use:

| clock | used by |
| --- | --- |
| **write** | ITL line age, the shared line |
| **use** | k2 and mu eviction, 7 d after the last read |
| **write gap** | k2's new episode after 24 h without a write, on the same window as the use clock |
| **agreement** | mu freshness: the last saturated reading within 2x |
| **serving** | the borrowed k2 relaxation, and the hold clocks that only a serving row advances |

**A defect to fix first:** `capacity.RollingAverage` (the k2 and mu windows)
stamps `time.Now()` in `Add`, `Observe` and `NewRollingAverage`. The ITL
tracker, the settle memo and the mu choice use the cycle clock `a.now()`. So
mu's episode reset runs on wall time while its freshness runs on the cycle
clock, and no test can age a window past `EpisodeGap`. PR 1 adds `AddAt`,
`ObserveAt` and `NewRollingAverageAt`, and every learner takes `now` from its
caller.

An age shown to an operator is the age of the last **admitted reading**, never
of the last use. A use-touched k2 otherwise reads young however old its
evidence is.

## 4. Settled replicas

Four components decide whether a replica has settled:

| | ITL steady trace (#273) | k2 settle and first sight (#282 `D/k2_settle.go`) | `noteReady` (#282 `D/throughput_admission.go`) | balance tracker (#283 `internal/signals/balance/tracker.go`) |
| --- | --- | --- | --- | --- |
| joined wait | 2 min | 1 min lookback, queue standing | 1 min | 2 min |
| ready time | first sight | first sight | Pod Ready time, wired but stubbed to zero (`replicaReadySince`, TODO(#283)) | later of Pod Ready and first sight of that Ready time |
| restart events | joined, load back after idle | joined | joined | joined, warm-pool lend |
| first cycle after a controller restart | taken as settled | taken as established | taken as established | waits again |

One tracker replaces them, in `internal/signals/settle`. It is owned per
analyzer instance (the SLO analyzer feeds its own), keyed per model and pod,
and swept with the model. It owns:

- **A stamp per reading, taken when observed.** The learner stores it with the
  reading, because a window outlives the settle.
- **The events that start a settle:** a replica joins, load returns after idle,
  the engine restarts (an engine or LWS group restart under the same name gives
  a new Ready time), a warm-pool lend, a rollout.
- **The ready time:** the later of the Pod's Ready time and the controller's
  first sight of it, as #283 does. A node clock behind the controller's, or a
  lend that keeps the pool's Ready time, makes the Pod time too old. Accepted
  cost: after a controller restart, or with an unreadable Pod listing, every
  replica waits the window again. That is logged.
- **Presence per gauge.** KV zero is idle: a value. A row with k > 0 and no ITL
  is a busy replica whose ITL is missing, not "no reading". The collector
  supplies a presence bit per gauge, as `KvUsageInstantKnown` does now.
- **Gaps and lookback.** A gap longer than the rule's period restarts the
  judgement. A rule compares against the reading one period back, not one cycle
  back: cycle-to-cycle one-minute peaks share 45 s.

Each learner keeps its own predicate and thresholds over the trace the tracker
returns. k2's rule needs a second gauge, a precondition and a cross-learner
read, and stays k2's. Before PR 1 merges, all four rules above are written
against the API.

## 5. Fits: exclude by state, then fit untrimmed

The pinned ITL fit changed five times inside #273. A weighted median jumped
between two groups of slopes. A trimmed mean dropped a genuinely slower replica
and priced capacity 8.5 % high, on the SLO side. What holds is: stamp each reading by the
replica's state, drop the settling ones, and fit the rest without trimming.

So there is no shared robust fit and no default fit. A value-based estimator
(median, trimmed or lower median) is allowed only where no state identifies the
contaminant, and the learner names the contaminant and the breakdown it needs.
Existing fits stay until a replay justifies a change.

## 6. Observability

### 6.1 Logs

- **`learner-write`**, at the shipped default verbosity, whenever a learned
  figure moves past its band or is set for the first time. Fields: `learner`,
  `modelID`, `namespace`, `variant`, `role`, `key`, `pod`, the reading and its
  time base, `value`, `previousValue`, `source`, `samples`, and the admission
  tests it passed with their margins. E1's 0.0995 req/s was **admitted**; this
  is the line that would have shown it.
- **Every pricing and decision line names its learner key and source.**
  `throughput-demand-floor` gains `muKey`, `muSource` and
  `startEstimateSeconds`.
- **Sources are per learner,** in the vocabularies already shipped. k2 keeps
  `P1-obs`, `P2-hist`, `P2-prior`, `P2-borrow`, `P3-k2`, `P4-k1`: the
  troubleshooting runbook gives each a different remedy. Mu keeps measured,
  derived, neighbour, borrowed-line. Start time keeps measured, seed. Each adds
  **`restored`** until its first admitted reading after a restart.
- **The per-replica lines stay:** `k2-decision`, `itl-window`,
  `saturated-throughput-refused`, `saturated-throughput-low-confirmed`.
  `hack/benchmark/dump_k2_decisions.py`, `post_run_analyze.sh` and the fixture
  extractor parse them. A PR that changes one updates those tools in the same
  PR, and the tools fail when they find zero lines in a run.
- A reason's "cleared" line waits until the reason has been absent for several
  cycles. After a restart, one summary line per model, not one line per key.
- Count lines cover only keys offered a reading or read for pricing this cycle.

### 6.2 Metrics

- Labels: `namespace`, `model_name`, `variant_name`, `role`, and
  `controller_instance` when set. No pod, no bucket. The vocabulary is
  [Every signal as a metric](signals-as-metrics.md).
- A gauge describes the key that priced the variant this cycle, and says so.
- Ages are published as `..._last_admitted_timestamp_seconds`, so that
  `time() - x` stays honest through a stalled analyzer.
- Each PR states its series count per variant, and a kept or replaced table
  for the `wva_learned_state_*` series it overlaps.
- **Deletion:** the engine deletes a model's learner series where it deletes
  the others. A learner's eviction and an operator's
  `llm-d.ai/forget-learned-state` delete that key's series.

### 6.3 Who does what

`internal/signals/learn` holds only vocabulary types and returns a `Report`
(counts by reason, source, samples, last admitted time, writes). It never logs
and never registers a metric. The analyzer logs; the engine publishes. `learn`
imports nothing above `internal/domain`, and `settle` imports nothing, so
`internal/metrics` can use their types without a cycle.

## 7. Migration

Each PR replays the PR 0 fixtures, shows its golden diff, and says what an
operator sees: log lines added or changed, metric series added or deprecated,
docs rows rewritten, tools updated.

| PR | change | what an operator sees |
| --- | --- | --- |
| **0** | The recorder, replay with goldens, per-path counters, property tests | Nothing. A recorder flag in `testing.md` |
| **1** | Clocks (3.5) and `internal/signals/settle`. #283's balance tracker is its first user | Nothing in production, where the two clocks agree |
| **2** | k2 (#3, #4) onto the tracker. The Pod Ready time switched on. `learner-write` for k2 | A pod back from a restart under the same name waits again: more `P1-obs-unsettled` after restarts. k2 writes are visible |
| **3** | Mu (#5, #6). Its joined test reads k2's stamp rather than recomputing it. Two prefill pods in one spacing pin #7's max before the fold is touched | `learner-write` for mu. `throughput-demand-floor` names `muKey`, `muSource`, `startEstimateSeconds` |
| **4** | ITL: #1, #13 and #16 onto the tracker. #16 gains #273's fixes, a named change with a negative control | `learner-write` when A or B moves. Throughput-analyzer fleets may change, as named |
| **5** | Start time (#12) and snapshot. Confirmations counted by distinct pod. The first-sample rule, a named change. The snapshot writer stays central with per-learner adapters, and reads a v1 document from the previous release | Start estimates no longer take one pod's first sample raw. `restored` sources |

Later, each after a recorded run that covers it: idle room and the decode gate
(#9, #10) after a P/D run; the prefill bound (#7). Out of scope: drift actions,
fit changes, #8, #11, #14, #15.

## 8. Risks and limits

- **One shared bug.** A defect in the settle tracker reaches every learner at
  once. Its tests cover every case the four versions were fixed for: k == 0, KV
  zero as idle, k > 0 with no ITL, a restart, a missed cycle, a slow fill, a
  just-Ready pod, a bridge lend, clock skew.
- **A shared write path already changed a learner silently.** #282 turned #5's
  in-spacing fold from a max into a lower median. #7 kept its max only through
  an empty pod name.
- **Self-sealing errors.** Each capacity learner is witnessed only when
  saturated, and the error that over-orders stops saturation. A too-low k2 or
  mu removes its own witness. Validation residuals are one-sided by
  construction, and must not be read as two-sided.
- **The prefill bound** is reviewed before it moves: a double scrape once
  doubled its input, and its max kept it.
- **Open defects this does not fix:** instantaneous k paired with a rate[1m]
  ITL; speculative decoding; shape dependence of the ITL line (#274).

## Review notes

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

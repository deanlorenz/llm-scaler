# single-analyzer

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
  *(read this first, before any other file)*
- **What / goal / mission:** Add an engine-side step that composes N analyzer results into one
  before the optimizer sees them, so the optimizer can be simplified back to single-analyzer-only
  logic. Must be a no-op when saturation is the only enabled analyzer (today's default).
- **Worktree:** `worktrees/single-analyzer` (branch `single-analyzer`)
- **Role / scope:** Mission owner — owns STATE, plan, branch, and integration decisions
- **Ledger / log:** `.session/2026-09-01-s8.md`
  ⚠ DO NOT READ — not yours; new session creates its own ledger

## Task

- **Plan / spec:** `.session/spec.md`
  *(do not read upfront — pull on demand only)*
- **Ledgers index:** `.session/ledgers/README.md` — one-line-per-file index of archived
  session captures and superseded snapshots (call-maps, the pre-CT6 spec snapshot, the
  original PR #2 design notes). Read the index before opening any individual ledger file.
- **PR specs:** `.session/pr-spec-34-composite-signal.md` (merged, verified scope + the two
  gists that described it) and `.session/pr-spec-next-coverage-units.md` (not yet opened,
  CT6-only scope, blocked on the test-fix). See "PR history" below.
- **Context:**
  - `internal/engines/allocation/optimizer_interfaces.go`
  - `internal/engines/allocation/analyzer_helpers.go`
  - `internal/engines/allocation/rescale.go`
  - `internal/engines/steadystate/engine_v2.go`
- **Refs:**
  - `worktrees/session-tracking/missions/single-analyzer/ledger-analyzer-optimizer-refactor.md`
  - `worktrees/session-tracking/missions/single-analyzer/fairshare-value-correctness-investigation-2026-08-25.md`
  - `internal/engines/allocation/multi_backup/` — pre-CT3b multi-entry originals (`//go:build ignore`)
- **Expected output:** Working code on `single-analyzer` branch; PR(s) to upstream
- **Done / completion criteria:** All tasks in Steps complete; `go build ./...` and
  `go test ./internal/engines/...` clean; upstream PR(s) merged
- **Limits:** Keep `.session/` out of every PR branch; do not expand scope beyond approved plan
- **Extra rules / rule refs:** `conventions/coder-orchestration.md` before dispatching coders;
  `conventions/pr-branch.md` before PR branch work; `conventions/pr-workflow.md` before opening PRs

## Execution

### Steps / subtasks

- [x] CT1a — nil-guard `rescaleModelDecisions` (commit `8906ef7b`)
- [-] CT1b — engine-side guard on nil saturation result (commit `122d1699` on `single-analyzer`; excluded from PR #1 by user request — separate bugfix, future PR)
- [x] CT2 — collapse `AnalyzerResults []NamedAnalyzerResult` to single `CompositeSignal` field (commit `e4106109`)
- [x] CT3a — write engine-side reduce contract (skipped — simplification was mechanical, no design ambiguity)
- [x] CT3b — simplify 7 single-entry optimizer helpers (commit `b980f682`)
- [ ] CT4 — score-weighted aggregation / fairness fix — BLOCKED on user decision (fix-now vs defer)
- [x] CT5 — document `RoleCapacities` role-visibility contract (commit `fcf9c905`)
- [x] CT6 — normalize sat→composite to coverage units (commit `f20e06f9`)
- [x] CT6 compile fix — 6 test call-sites + `multi_backup` move (commit `18f4d4ff`, 2026-09-06)
- [x] CT6 correctness fix — RC/SC/Remaining/Spare/supply normalization + `SatRoleDemand` +
  composite logging + tests (commits `c5af5696`/`290ca75f`/`896879d5`/`e4b1e77d`, 2026-09-06;
  reviewed Pass by independent reviewer, cherry-picked from coder branch `coder-ct6-fix-v3`)
- [x] CT6 log-function merge — folded `logCompositeSignal` into `logAnalyzerResult` (one
  function, one log key, union of fields) + doc update (commit `65c344af`, 2026-09-07;
  user-caught design flaw in the cherry-picked fix, fixed directly, not yet pushed)
- [x] CT6 composite naming + log completeness — composite entry now named
  `allocation.CompositeSignalName` ("CompositeSignal", not "saturation");
  `hasSaturationResult` → `hasCompositeResult` (dropped the now-stale `Name` comparison, kept
  `Result != nil` as a defensive guard); added `score`/`live`/`tokenRoleDemand` to the log line,
  renamed `satDemand`→`tokenDemand`; `cycle-log.md` rewritten to describe each analyzer's line
  by its own unit (saturation=tokens, throughput=tokens/sec, CompositeSignal=%) instead of
  pre/post-normalization prose (commit `991800ce`, 2026-09-07; user-caught during PR review,
  not yet pushed)
- [ ] CT6 composite metrics — TODO, separate future PR, see Known issues
- **User is still reviewing the rest of the PR diff (2026-09-07, in progress) — expect further
  findings before this is considered fully wrapped.**
- [ ] CT7 (a.k.a. "PR #2") — engine-side reduce to wire non-saturation analyzers into
  CompositeSignal — design + Q1-Q4 open questions now in `spec.md`'s CT7 section

**Last completed:** CT6 — Normalize sat→composite to coverage units (commit `f20e06f9`, 2026-09-01)

**Next step / resume point:** CT6 fully landed on `single-analyzer` — both the compile fix
(`18f4d4ff`) and the correctness fix (`c5af5696`/`290ca75f`/`896879d5` cherry-picked from
coder branch `coder-ct6-fix-v3`, which was reviewed **Pass** by an independent reviewer —
report at `.session/review-coder-ct6-fix-v3.md` — plus `e4b1e77d`, a follow-up cleanup for the
review's one non-blocking nit: dropped an internal `(CT6)` tag from a test's `Describe`
string). `go build`/`go vet`/`go test ./internal/engines/...`/`gofmt` all independently
verified clean by the reviewer at the coder's tip; re-verified locally after the nit fix.

**Pushed to origin 2026-09-06:** `single-analyzer` fast-forwarded on `origin`
(`git@github.com:deanlorenz/llm-scaler.git`) `233f74a1` → `c2a0774e` (17 commits, user-authorized
per-op). `origin/single-analyzer` at that point built clean and carried the full CT6 correctness
fix.

**2026-09-07 — user is reviewing the full CT6 PR diff, in progress; findings so far:**
1. The coder's CT6 fix added a second, separate logging function (`logCompositeSignal`)
   instead of extending the existing `logAnalyzerResult`. Fixed: merged into one function,
   updated the doc (commit `65c344af`).
2. `cycle-log.md`'s rewrite for (1) was itself wrong — described the composite line via
   "pre-conversion"/"post-conversion" implementation prose instead of plain per-analyzer units.
   Also surfaced: the composite entry's `Name` was never changed from `"saturation"` (no real
   `"composite"` identity existed), `hasSaturationResult`'s `Name` check was about to go stale,
   and `Score`/`Live`/`SatRoleDemand` were missing from the log line with no reason. Fixed all
   together: `Name` → `allocation.CompositeSignalName` ("CompositeSignal"),
   `hasSaturationResult` → `hasCompositeResult` (Result-only check), log line completed,
   `satDemand`→`tokenDemand` renamed, `cycle-log.md` rewritten again to describe each
   analyzer's line by its own unit (commit `991800ce`).

Both `65c344af` and `991800ce` are **not yet pushed** — 2 commits ahead of
`origin/single-analyzer` (still at `c2a0774e`). These two commits were made directly by the
mission owner (not a coder) in response to the user's PR review, and had NOT been through
independent review — user asked (2026-09-07) to get them reviewed like everything else.
**Reviewer dispatched** (agentId `a7a738431d7e541ca`, background) against just these 2 commits;
report will land at `.session/review-65c344af-991800ce.md`. Do not push or consider this PR
done until that verdict comes back.

**Remaining before this can be considered fully wrapped:**
1. Wait for the review of `65c344af`/`991800ce` (in progress) and address any findings.
2. Push the accumulated fix commits to origin — needs its own per-op authorization (the
   2026-09-06 push authorization is consumed, per `conventions/push.md`).
3. Decide with the user whether CT4's fairness fix (`fairShareValue`, still blocked on a
   fix-now-vs-defer decision) belongs in the next PR.
4. Finalize the next PR's exact scope/boundary (see PR history below) and open it via the
   PR-branch workflow (`conventions/pr-branch.md`/`conventions/pr-workflow.md`).
5. Implement CT6 composite metrics (separate future PR — see Known issues; explicitly not
   blocking the current PR).
6. Clean up: the two failed coder-dispatch worktrees/branches from 2026-09-06
   (`.claude/worktrees/agent-a223357ad56398278`, `.claude/worktrees/agent-a64b37115d72e7617` if
   still present) and the completed `coder-ct6-fix-v3` worktree/branch
   (`.claude/worktrees/agent-acc4742a2f2a1aceb`) can be removed once the user confirms nothing
   else is needed from them — not yet done.

### PR history

Full detail and verification method for each PR: `.session/pr-spec-34-composite-signal.md`
and `.session/pr-spec-next-coverage-units.md`.

- **PR #34 — MERGED 2026-09-02** (https://github.com/ev-shindin/llm-scaler/pull/34, merge
  commit `347de1a9`). **Corrected 2026-09-06** (verified against `gh pr diff 34`'s actual
  content, not commit messages): scope is CT1a + CT2 + **CT3b + CT5** — the PR-prep branch
  squashed CT2/CT3b/CT5 into one commit (`113fec1d`) even though they're 3 separate commits on
  the mission branch (`e4106109`/`b980f682`/`fcf9c905`). Does NOT contain CT1b or CT6.
- **Next PR — not yet opened, no branch cut yet.** Scope is **CT6 only**: `f20e06f9` (original
  normalization) + `18f4d4ff` (compile fix) + `c5af5696`/`290ca75f`/`896879d5`/`e4b1e77d`
  (correctness fix + tests) + `65c344af` (log-function merge) + `991800ce` (composite naming +
  log completeness) — CT3b and CT5 are already merged in PR #34, so they are not part of this
  PR's diff. User's PR review still in progress (2026-09-07) — more commits may be added before
  this is ready to cut. `65c344af`/`991800ce` not yet pushed. The s7 stale-args bug (`b067642a`)
  is NOT relevant to this PR — it only ever existed on the mission branch, never on any
  PR-prep branch, so there's nothing to carry forward for it.
- **CT7** (engine-side reduce) is not scoped into either PR above; it needs its own PR once its
  4 open design questions (spec CT7 section) are resolved, and depends on the next PR's test-fix
  landing first (CT7 builds on `runAnalyzersAndScore`'s current slice-returning shape).
- CT4 blocked on user decision; CT1b deferred to a future PR (excluded from PR #34 by user
  request).

### Known issues

- **FIXED 2026-09-07 (commits `65c344af`, `991800ce`; not yet pushed): three compounding gaps
  in the coder's CT6 fix, all caught by the user during PR review, none by the coder or
  reviewer.**
  1. `logCompositeSignal` was an unnecessary duplicate of `logAnalyzerResult` — same struct,
     two functions, two log keys, mismatched field sets. Merged into one (`65c344af`).
  2. The doc fix for (1) itself described the composite line via "pre-conversion"/
     "post-conversion" prose instead of plain per-analyzer units — confusing and exposed
     implementation history that doesn't belong in a log-field reference.
  3. Auditing the doc surfaced that the composite entry's `Name` was never actually changed
     from `"saturation"` — there was no real `"composite"` identity in the code for the doc to
     describe. Fixed by adding `allocation.CompositeSignalName` ("CompositeSignal") and setting
     it in `normalizeToCompositeUnits`. This in turn required fixing `hasSaturationResult`
     (renamed `hasCompositeResult`), whose `Name == domain.SaturationAnalyzerName` comparison
     would have gone stale — traced the call chain and confirmed `Result != nil` is the only
     semantic that ever mattered there (verified `Result` is provably non-nil by construction
     for every request reaching that check, so the check is a defensive guard, not live logic
     today). Also audited the full `NamedAnalyzerResult` struct against the log line and found
     `Score`/`Live`/`SatRoleDemand` missing with no reason — added them (`991800ce`), renaming
     `satDemand`→`tokenDemand` per the user's explicit naming request.
- **TODO (separate future PR): composite metrics.** Spec's confirmed fix design said "add a
  log line (and/or metric)" for the post-normalization composite; only the log line
  (`logCompositeSignal`) shipped in the CT6 fix. The metrics half is still open: emit
  `wva_required_capacity`/`wva_spare_capacity` (and any other composite fields worth exposing)
  for the *normalized* composite, as coverage fractions — already confirmed acceptable in
  spec.md's CT6 "Resolution of the observability tradeoff" (2026-09-06): per-analyzer metrics
  (`wva_analyzer_demand`/`wva_analyzer_target`, from `recordAnalyzerMetrics` on raw
  pre-normalization `namedResults`) are a distinct metric family and stay untouched — no
  information is lost by adding a second, composite-scoped metric family alongside them. Design
  question is resolved; only the implementation is outstanding. Not blocking the current PR.
- **CT6 correctness bug — FIXED and merged 2026-09-06, reviewed Pass.** `RequiredCapacity`/
  `Remaining`/etc. now normalized alongside `PerReplicaCapacity`/`TotalDemand`/`RoleDemand`.
  Commits `c5af5696`/`290ca75f`/`896879d5`/`e4b1e77d` on `single-analyzer` (cherry-picked from
  coder branch `coder-ct6-fix-v3`, independently reviewed — see `.session/review-coder-ct6-fix-v3.md`).
  Original bug description retained below for historical reference.
  `normalizeToCompositeUnits` converts `PerReplicaCapacity` to a unit-less coverage fraction
  but never touches `RequiredCapacity`, `SpareCapacity`, `Remaining`, `Spare`, or
  `RoleCapacities[role].RequiredCapacity`/`.SpareCapacity` — those are computed by
  `applyUniversalThreshold` *before* normalization runs, from raw (token-scale) demand, and
  never revisited. `initRoleState` (`analyzer_helpers.go:150,156`) seeds `pickerState`/
  `e.Remaining` directly from these raw-scale fields, and every downstream helper
  (`roleBottleneckReplicas`, `safeRemovalReplicasForRole`, `applyAllocation`,
  `applyDeallocationForRole`) divides/subtracts them against the now-normalized (fractional)
  `PerReplicaCapacity` — producing replica counts wrong by roughly `1/PRC_fraction` (e.g. 4x
  too many for a 0.25 coverage fraction) for any model that actually has nonzero demand.
  **Not caught by any test:** all 7 new CT6 unit tests construct `NamedAnalyzerResult` by hand
  and never touch `RequiredCapacity`/`Remaining`; all pre-existing optimizer/rescale tests use
  a fixture that bypasses `buildCapacities`/`normalizeToCompositeUnits` entirely; the only 2
  tests that call the real `collectV2ModelRequest` path use an empty `&domain.AnalyzerResult{}`
  (zero demand) and check only `Disaggregated`. No test exercises the real build-then-normalize
  pipeline with nonzero demand. **`SatDemand` itself is not affected** — it's correctly
  model-scoped (set once from `Result.TotalDemand` before normalization) and its only consumer
  (`rescaleInputsForGroup`) uses it as a per-model scalar, never per-role; `roleDemandGPUs` is
  also fine (it reads `TotalDemand`/`rc.TotalDemand`, which normalization *does* correctly reset
  to `1.0` in lockstep with `PerReplicaCapacity`). **Fix design CONFIRMED 2026-09-06** — full
  field-by-field disposition (verified via exhaustive grep of every read site, not just the
  ones with a visible bug) in `.session/spec.md`'s CT6 section and
  `.session/pr-spec-next-coverage-units.md`. Not yet implemented. New field:
  `SatRoleDemand map[string]float64`. Confirmed acceptable: `wva_required_capacity`/
  `wva_spare_capacity` become coverage fractions for the composite (correct); per-analyzer
  metrics stay in their own units (unaffected, different code path). Also confirmed: add
  logging/metrics for the normalized composite itself (currently invisible). TODO noted for
  later (not this fix): revisit whether model-level "non-role" fields should exist at all,
  vs. requiring `role="both"` and always going through `RoleCapacities`.
- **CT4 fairness:** `fairShareValue` equalizes absolute remaining demand, not coverage ratio.
  Fix-now vs. document-and-defer is the user's call. See spec CT4 section and
  `worktrees/session-tracking/missions/single-analyzer/fairshare-value-correctness-investigation-2026-08-25.md`.
- **Rescale weight long-term fix:** token weight is proportional to N_full only for homogeneous
  PRC; longer-term fix tracked in spec rescale-fairness section (separate CT).
- **Coder dispatch mechanics — two confirmed-broken patterns (2026-09-06), for future dispatches:**
  (1) `Agent` with `isolation:"worktree"` always creates a brand-new ad-hoc worktree for the
  subagent; it cannot be pointed at a worktree you already prepared — a pre-created worktree
  handed to it that way is simply ignored. (2) Launching a background `Agent` *without*
  `isolation` from a session that is itself pinned (via `EnterWorktree`) makes the subagent's
  Bash sandbox inherit the *parent's* pinned worktree, not any path the subagent later passes to
  its own `EnterWorktree(path=...)` call — `EnterWorktree` inside such a subagent relocates file
  tools only, never Bash, and `ExitWorktree` refuses outright for a pinned subagent. Net effect:
  there is currently no way to hand a pre-existing worktree to a subagent and have its Bash
  actually run there. Working pattern instead: prepare a branch/commit with the exact starting
  state, launch with `isolation:"worktree"` (real Bash, but a fresh unrelated worktree/branch),
  and have the coder `git reset --hard <sha>` to that prepared commit as its first action, then
  work and commit from there; integrate via cherry-pick afterward.

## Key decisions (for resuming context)

**Engine→optimizer boundary:**
- `CompositeSignal NamedAnalyzerResult` (single value, not slice); `saturationNamedEntry` deleted;
  all consumers read `req.CompositeSignal` directly.
- `normalizeToCompositeUnits` runs after `buildCapacities` (RC/SC computed from raw tokens first,
  then PRC/demand normalized to coverage units). Must not move before `buildCapacities`.
- After normalization: `TotalDemand=1.0`, `RoleDemand[role]=1.0`, `PRC=PRC/D(role)`.
- `SatDemand float64` on `NamedAnalyzerResult` preserves raw token demand for rescale weight.

**Rescale weight:**
- `rescaleInputsForGroup` uses `SatDemand` (not `Result.TotalDemand`) — survives CT6 normalization.
- Intended semantic: `weight ∝ N_full(M)`.

**Semantic framework (established 2026-09-01):**
- `C(SO) = PRC/D` — per-replica coverage fraction ∈ (0,1]
- `N_full(SO) = ceil(1.0/C(SO))` — replicas for full coverage
- Multi-analyzer reduce: `N_full = max_i`, `C = min_i`
- Same-role SOs add: `C(M,R) = Σ C(SO)`; cross-role: `min(C(M,prefill), C(M,decode)) + C(M,both)`
- `NG(SO) = N_full × G(SO)` — GPU demand

**Multi-entry originals:**
- `internal/engines/allocation/multi_backup/` (`//go:build ignore`) holds pre-CT3b slice-based
  optimizer helpers for the planned engine-side reduce (CT7, spec section).

**PR isolation:**
- `pr/single-analyzer` is ephemeral staging for upstream PRs; does not need to stay in sync with
  `single-analyzer`. PR work done via coder agent on
  `/home/dean/code/llm-d/worktrees/pr-single-analyzer`, not by the orchestrating session.

## Session log

- 2026-08-27 session=2026-08-27-ct1b-review status=retired ledger=.session/2026-08-27-session-tracking-setup.md
- 2026-08-29T22:32 session=2026-08-29-ct2-resume status=retired ledger=.session/2026-08-29-ct2-resume.md
- 2026-08-30T08:00 session=2026-08-30-ct3-resume status=retired ledger=.session/2026-08-30-ct3-resume.md
- 2026-08-30T17:58 session=2026-08-30-ct3-s6 status=retired ledger=.session/2026-08-30-ct3-s6.md
- 2026-08-31T00:00 session=2026-08-31-s7 status=retired ledger=.session/2026-08-31-s7.md
- 2026-09-01 session=2026-09-01-s8 status=retired ledger=.session/2026-09-01-s8.md
- 2026-09-06 session=2026-09-06-single-analyzer-1 status=retired ledger=.session/2026-09-06-single-analyzer-1.md
- 2026-09-06T12:00 session=2026-09-06-single-analyzer-2 status=retired ledger=.session/2026-09-06-single-analyzer-2.md
- 2026-09-06T18:00 session=2026-09-06-single-analyzer-3 status=active ledger=.session/2026-09-06-single-analyzer-3.md

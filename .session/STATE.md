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
- [ ] CT7 (a.k.a. "PR #2") — engine-side reduce to wire non-saturation analyzers into
  CompositeSignal — design + Q1-Q4 open questions now in `spec.md`'s CT7 section

**Last completed:** CT6 — Normalize sat→composite to coverage units (commit `f20e06f9`, 2026-09-01)

**Next step / resume point:** CT6 compile fix landed directly on `single-analyzer` (commit
`18f4d4ff`). CT6 *correctness* fix implemented by coder v3 (agentId `acc4742a2f2a1aceb`,
`isolation:"worktree"`, reset to `18f4d4ff` per the revised dispatch approach — see prior
session log / ledger for the two earlier failed dispatch attempts and their postmortem) on
branch `coder-ct6-fix-v3` (worktree `.claude/worktrees/agent-acc4742a2f2a1aceb`): 3 commits —
`7663d180` (core fix: normalize RC/SC/Remaining/Spare/supply alongside PRC, add `SatRoleDemand`,
add `logCompositeSignal`), `a8e9c511` (extend the 7 existing CT6 unit tests to cover the
newly-normalized fields), `0a5c0f52` (new e2e test through the real
`collectV2ModelRequest`→optimizer pipeline with nonzero demand — coder reports verifying it
fails pre-fix, passes post-fix). Coder reports `go build`/`go vet`/`gofmt`/
`go test ./internal/engines/...` all clean (158 specs), plus a broader `./internal/...` pass
clean (only `test/e2e`, which needs a live cluster, not run). **Not yet independently verified
by the mission owner or a reviewer — reviewer v2 (agentId `ad557beac4e2c53a5`) dispatched
2026-09-06 to independently re-run build/vet/test/gofmt and verify the field-by-field table**;
do not treat this as done until that review reports back. After review passes: mission owner
cherry-picks `7663d180`/`a8e9c511`/`0a5c0f52` onto `single-analyzer` (per
coder-orchestration.md rule 10 — never merge coder worktree directly). Then: decide whether
CT4's fairness fix is in scope for the next PR, finalize the next PR's exact boundary (see PR
history below), and push `18f4d4ff` (+ the cherry-picked CT6 fix) to origin — needs explicit
per-op push authorization, not yet given.

### PR history

Full detail and verification method for each PR: `.session/pr-spec-34-composite-signal.md`
and `.session/pr-spec-next-coverage-units.md`.

- **PR #34 — MERGED 2026-09-02** (https://github.com/ev-shindin/llm-scaler/pull/34, merge
  commit `347de1a9`). **Corrected 2026-09-06** (verified against `gh pr diff 34`'s actual
  content, not commit messages): scope is CT1a + CT2 + **CT3b + CT5** — the PR-prep branch
  squashed CT2/CT3b/CT5 into one commit (`113fec1d`) even though they're 3 separate commits on
  the mission branch (`e4106109`/`b980f682`/`fcf9c905`). Does NOT contain CT1b or CT6.
- **Next PR — not yet opened, no branch cut yet.** Scope is **CT6 only** (`f20e06f9`) — CT3b
  and CT5 are already merged in PR #34, so they are not part of this PR's diff. Blocked on the
  CT6 test-fix (see Known issues) being committed first; the s7 stale-args bug
  (`b067642a`) is NOT relevant to this PR — it only ever existed on the mission branch, never
  on any PR-prep branch, so there's nothing to carry forward for it.
- **CT7** (engine-side reduce) is not scoped into either PR above; it needs its own PR once its
  4 open design questions (spec CT7 section) are resolved, and depends on the next PR's test-fix
  landing first (CT7 builds on `runAnalyzersAndScore`'s current slice-returning shape).
- CT4 blocked on user decision; CT1b deferred to a future PR (excluded from PR #34 by user
  request).

### Known issues

- **CT6 correctness bug — believed fixed 2026-09-06 by coder v3, pending independent review.**
  `RequiredCapacity`/`Remaining`/etc. never get normalized, only `PerReplicaCapacity`/
  `TotalDemand`/`RoleDemand` do. Fix on branch `coder-ct6-fix-v3` (commits `7663d180`/
  `a8e9c511`/`0a5c0f52`), not yet cherry-picked onto `single-analyzer` — see "Next step" above.
  Original bug description retained below for reference until the fix is verified and merged.
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
- **`origin/single-analyzer` still does not build** — the compile fix (commit `18f4d4ff`) is
  local-only so far (not pushed); origin's tip (`d90bd565`, carrying `f20e06f9` without the fix)
  still fails `go build ./...`. Needs a push (with explicit per-op authorization) at some point.
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

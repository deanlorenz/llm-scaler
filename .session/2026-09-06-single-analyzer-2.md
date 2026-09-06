Continues: .session/2026-09-06-single-analyzer-1.md

# Session ledger — 2026-09-06-single-analyzer-2

## Session start

- Resumed mission `single-analyzer` via `/resume-mission` skill.
- All prior sessions verified (retired + `## Verified` markers present). No pending ledgers.
- Ownership declared on agentbus (`mission.single-analyzer`, seq=80).
- Read `session-tracking` `CONVENTIONS.md` (user-directed, separate from resume-mission flow).

## Findings — cleanup / orientation pass

User asked: understand where we are, record findings, before deciding on the uncommitted
test-file diff found in the working tree. Investigated two worktrees.

### 1. Working tree has uncommitted test-only changes (not production code)

`git status` showed 6 modified `_test.go` files, 1 deleted `_test.go` file, plus untracked
`.session/` and `docs/plans/analyzers/` docs. Zero non-test production files touched
(`git status --short | grep -v "_test.go\|\.session\|docs/plans"` → empty).

Traced the cause: commit `f20e06f9` (CT6) changed `runAnalyzersAndScore`'s return type from
`allocation.NamedAnalyzerResult` to `[]allocation.NamedAnalyzerResult` (confirmed via
`git log -L 103,114:internal/engines/steadystate/engine_v2.go`) and removed
`composeAnalyzerResults`/`rawAnalyzerResult` as vacuous pass-throughs (per
`.session/2026-09-01-s8.md` line 50-51). But the commit's `git add` never picked up the test
call sites — 6 files still did `result, err := e.runAnalyzersAndScore(...)` then `result.Name`
etc., which does not compile against a slice return (`.Name` is not a field/method on
`[]NamedAnalyzerResult`).

Verified: `go build ./...` and `go vet ./internal/engines/...` both pass cleanly **with** the
uncommitted working-tree edits applied. This confirms the edits are the correct, necessary
fix, not exploratory work — and that HEAD without them does not compile.

`engine_v2_compose_test.go` (deleted from `steadystate/`, re-added at
`internal/engines/allocation/multi_backup/engine_v2_compose_test.go` with `//go:build ignore`,
content byte-identical) tested the now-deleted `composeAnalyzerResults`, so it had to move —
matches the already-documented "pre-CT3b multi-entry originals" backup pattern
(STATE.md context line: `internal/engines/allocation/multi_backup/`).

**Conclusion: this is a missed piece of CT6 itself, not new/experimental work.** It's been
sitting uncommitted since 2026-08-31, unnoticed across 3 subsequent sessions (s8,
2026-09-06-single-analyzer-1, and now).

### 2. PR #34 is already MERGED — STATE.md was stale

`gh pr view 34 --repo ev-shindin/llm-scaler` shows `"state":"MERGED"`, `mergedAt:
2026-09-02T08:01:59Z`, containing exactly 2 commits: `1a0c23be` (CT1a nil-guard) and
`113fec1d` (CT2 CompositeSignal refactor). STATE.md said "open... awaiting review" — corrected
in this session (see STATE.md Status section).

### 3. Second worktree: `/home/dean/code/llm-d/worktrees/pr-single-analyzer`

Branch `pr/single-analyzer`, repo `deanlorenz/llm-scaler` (fork used for PR staging, distinct
from this sandbox monorepo). This branch/worktree **is** PR #34 — now merged, working tree
clean, nothing further pending there. It is not "in progress" anymore.

Checked `origin/single-analyzer` (same fork) via `git fetch` from that worktree: tip
`d90bd565`, confirmed (via `git merge-base --is-ancestor`) to be an ancestor of this session's
local `single-analyzer` HEAD (`23f5039b`) — i.e. origin already has CT3b (`b980f682`), CT5
(`fcf9c905`), the s7 stale-args fix (`b067642a`), and CT6 (`f20e06f9`) pushed. It is missing
only the latest housekeeping/ledger commits, not any code. **No PR branch or PR exists yet for
this payload** — this is the "PR branch in progress, not yet a PR" the user was recalling:
code is staged on origin, but never cut into its own `pr/...` branch or opened.

Full commit list, CT2 to HEAD, on mission branch (`git log --oneline e4106109..HEAD`):
non-doc code commits are `b980f682` (CT3b), `fcf9c905` (CT5), `b067642a` (stale-args fix),
`f20e06f9` (CT6). Everything else in that range is `docs(state)`/`docs(ledger)` housekeeping.

**Important: since the CT6 test-fix (finding #1) was never committed anywhere, origin's
pushed HEAD (`d90bd565` and descendants) does not build either** — same missing fix.

### 4. PR #2 design doc already exists, with unresolved open questions

`docs/plans/analyzers/compose-reduce-design-2026-08-30.md` (untracked) is a real design doc
for the engine-side reduce: floor invariant (composite never less aggressive than sat alone),
implied-replica-count as the common currency across analyzer units, sat-only fast path (zero
behavioral change when sat is the only entry). Four open questions block writing code:
- Q1: does per-variant `TotalDemand` get raised to match the recomputed model-level max, or
  stay as sat's for display?
- Q2: does a non-sat analyzer with no `RoleDemand` participate in the per-role reduce?
- Q3: does the composite keep the name `"saturation"` (so `isSaturationResult` at
  engine_v2.go:747 still matches), or does that check change?
- Q4: what Score does the composite carry — sat's, max across analyzers, or a fixed value?

### 5. Other untracked files inventory

`docs/plans/analyzers/`: 5 more files (`engine-call-map`, `engine-call-stack`,
`optimizer-call-map`, `optimizer-call-stack`, `single-analyzer-call-map`, all
`-2026-08-30.md`) — CT3 research artifacts, correctly placed per AGENTS.md's
`docs/plans/<area>/` convention, just never committed.

`.session/`: several untracked ledgers/reports from already-retired-and-verified sessions
(should have been committed at their own wind-down but weren't), plus `spec.md.local` (stale
pre-CT6 snapshot of the spec, superseded by `spec.md.wip`) and `fallback-trace.md.local` (a
separate, unrelated scale-from-zero investigation doc). `spec.md.wip` itself has a stale
`.wip` lock name (no `spec.md` counterpart) — flagged, not yet resolved, per
`conventions/wip-editing.md`; this is a stale abandoned lock from a prior session, not a live
concurrent editor.

## Open items handed to user (not yet decided) — as of first findings pass

1. Commit the CT6 test-fix (finding #1) — needed regardless of PR #2 scope, since CT6 as
   pushed doesn't build.
2. Task spec for the next PR: does it carry CT3b+CT5+fix+CT6 only (already on origin, modulo
   the fix), or wait for PR #2 (engine-side reduce) design questions (finding #4) to resolve
   first, or split into two PRs?
3. What to do with the "leftover" test changes — user flagged uncertainty about whether some
   of these are needed later. Needs clarification: the multi_backup move is a deliberate,
   permanent parking spot (already-documented pattern); the 6 modified test files are a
   required compile fix, not optional/experimental.
4. Untracked docs/plans/analyzers files and .session files — commit as-is, or clean up first?
5. Stale `spec.md.wip` lock — release/rename, or leave as active spec?

(Resolved below — items 1-2 got their own PR specs, 3-5 got resolved during the cleanup pass.)

## Docs cleanup pass (user directive: move all docs/plans artifacts to .session/ledgers)

- Confirmed via `ls .claude/worktrees/` that all 8 entries match `git worktree list` exactly —
  no orphaned/unregistered worktree directories. User asked "did you check .claude/worktrees
  too?" — yes, both `agent-*` dirs and all 6 named ones, none held CT6's coder work.
- Read `AGENTS.md`'s "Agents plans" definition verbatim for the user (it's genuinely terse:
  "For AI agent plans... in docs/plans/<area>/").
- Read `spec.md.wip` in full (922 lines). Found CT6's own header line and Status/Todo
  checkboxes still said "NOT STARTED" despite `f20e06f9` being committed and documented as
  done elsewhere (STATE.md, ct6-implementation-report.md) — this was the concrete evidence
  for "the spec is a mess."
- Created `.session/ledgers/` and moved all 6 untracked `docs/plans/analyzers/*-2026-08-30.md`
  files there (5 stale call-map/call-stack snapshots + `compose-reduce-design-2026-08-30.md`),
  plus `spec.md.local` (superseded pre-CT6 snapshot). Wrote `.session/ledgers/README.md` as
  a one-line-per-file index. The 4 pre-existing tracked files in `docs/plans/analyzers/`
  (README.md, analyzer-architecture-refactor.md, k2-capacity-model.md,
  kvcachethreshold-retirement.md) were left untouched — not part of this mission's untracked
  additions.
- Before archiving `compose-reduce-design-2026-08-30.md`: ported its design (floor invariant,
  implied-replica-count reduce, Q1-Q4) into a new CT7 section in `spec.md.wip`, per user
  correction that it was "a ledger file... design and open questions do not belong in a
  ledger."
- Fixed CT6's stale header/Status/Todo to DONE with commit `f20e06f9`, documented the
  test-fix gap explicitly in that section. Renamed `spec.md.wip` → `spec.md` (stale `.wip`
  suffix, no live editor). Added revision-history entries v6-v8 explaining the CT6/CT7 gap.
- Updated STATE.md: added Ledgers pointer, PR history section (initially still said "PR #34 =
  CT1a+CT2 only" — corrected later, see below), corrected spec pointer to `.session/spec.md`.
- Committed as `e88e48dc`.
- **Found a genuine protocol violation, not mine to fix retroactively:** `.session/STATE.p3-planner.md`
  and `.session/2026-09-06-p3-planner-1.md` appeared mid-session — a separate, deliberate
  spinoff planning sub-mission for "PR #3," explicitly scoped read-only against my STATE.md.
  User confirmed this is intentional and won't touch my files; I left its files alone. Its
  task file references `.session/compose-reduce-design-2026-08-30.md`, which no longer exists
  at that path after the move to `.session/ledgers/` — flagged to the user, not fixed by me
  (not my file to edit).

## PR-34-scope re-verification and two focused PR specs (user directive)

- Fixed `.session/spec.md`'s stale unchecked Todo boxes for CT1/CT2 (both long DONE) and the
  remaining CT6 items I'd missed in the first pass (only the last "run go test" checkbox had
  been fixed; the 7 items above it were still `[ ]` despite being done per
  `ct6-implementation-report.md`).
- **Verified PR #34's actual scope from source, not from commit-message bookkeeping:**
  `gh pr view 34 --json files` and `gh pr diff 34` show `analyzer_helpers.go`,
  `cost_aware_optimizer.go`, `greedy_score_optimizer.go`, and `multi_backup/*` are all in the
  PR — confirmed CT5's exact "Role-visibility contract" doc-comment text is present in the
  diff at `initRoleState`. **PR #34 = CT1a + CT2 + CT3b + CT5, not just CT1a+CT2** as I'd
  written in STATE.md earlier this session. The PR-prep branch squashed 3 mission-branch
  commits (`e4106109`/`b980f682`/`fcf9c905`) into one PR commit (`113fec1d`). Confirmed CT1b
  and CT6 are NOT in the diff (no sentinel-error text, no `SatDemand`/`normalizeToCompositeUnits`).
- This narrows "the next PR" to **CT6 only** — CT3b/CT5 are already merged.
- Wrote `.session/pr-spec-34-composite-signal.md` (merged PR, verified scope, with
  `gist_engine.md`/`gist_optimizer.md`'s content folded in verbatim as the "what changed"
  body, per explicit user request) and `.session/pr-spec-next-coverage-units.md` (CT6-only
  scope, the blocking test-fix, explicit exclusions: CT1b/CT4/CT7).
- Corrected STATE.md's PR history section with the verified facts. Committed as `ed502057`.

## The runAnalyzersAndScore loop diff Q&A

- User asked to see the diff of the engine loop for the next PR (CT6/`f20e06f9`). Showed the
  actual `git show f20e06f9` diff: return type change AND a structural change — the old code
  collected raw `(D,P)` pairs into `rawAnalyzerResult`, reduced via `composeAnalyzerResults`,
  then built once; CT6 removed the reduce step entirely and now calls `buildNamedResult`
  per-analyzer inside the loop, keeping the full slice, picking `[0]` at the call site instead.
- Showed `normalizeToCompositeUnits` and its call site in full (clean, non-diff) on request.
  Flagged unprompted: the `RoleDemand` fallback-to-`TotalDemand` branch inside it isn't
  documented in `spec.md`'s CT6 section — this observation led directly into the correctness-
  bug investigation below.
- User asked whether `buildNamedResult` runs independently per analyzer and whether it's
  sat-specific. Confirmed via code read: fully generic, no sat-specific logic anywhere in
  `buildNamedResult`/`buildCapacities`/`applyUniversalThreshold`/`buildRoleCapacities`; sat is
  just always called first, unconditionally, by the loop structure — `collectV2ModelRequest`
  picks `namedResults[0]` by construction of that ordering, not because the function knows
  it's sat.

## CT6 correctness bug — found, traced exhaustively, fix design confirmed

- User asked to verify: (1) `SatDemand` isn't accidentally using per-role demand, (2) every
  `NamedAnalyzerResult` field is accounted for post-normalization, and whether
  `buildNamedResult`'s pre-computed values (RC/SC/etc.) still mean the same thing after
  normalization.
- Traced `buildNamedResult` → `buildCapacities` → `applyUniversalThreshold` (runs first, on
  raw demand, inside the loop) against `normalizeToCompositeUnits` (runs later, in
  `collectV2ModelRequest`, only converts `PerReplicaCapacity`/`TotalDemand`/`RoleDemand`/
  `RoleCapacities[role].TotalDemand`). Found: `RequiredCapacity`, `SpareCapacity`, `Remaining`,
  `Spare`, `RoleCapacities[role].RequiredCapacity`/`.SpareCapacity` are computed from RAW
  demand and never revisited — `initRoleState` seeds `pickerState`/`Remaining` from these
  still-raw fields, then every downstream helper divides them against the now-fractional
  `PerReplicaCapacity`. Verified the arithmetic is genuinely wrong (`ceil(rawRC/PRC_fraction)`
  off by ~`1/PRC_fraction`), not just stale-looking.
- Verified why no test caught it: read `engine_v2_normalize_test.go` in full — all 7 CT6 unit
  tests construct `NamedAnalyzerResult` by hand, never touching RC/Remaining. Confirmed the
  16 pre-existing rescale tests use `satEntryFixture.named()`, which bypasses
  `buildCapacities`/`normalizeToCompositeUnits` entirely (already known from
  `ct6-implementation-report.md`). Found the only 2 tests exercising the real
  `collectV2ModelRequest` path (`engine_v2_test.go:479-532`) use an empty
  `&domain.AnalyzerResult{}` and check only `Disaggregated`. **No test anywhere exercises the
  real build-then-normalize pipeline with nonzero demand.**
- Verified `SatDemand` itself is fine (correctly model-scoped, used only as a per-model
  water-fill weight in `rescaleInputsForGroup`) and that `roleDemandGPUs`/`modelDemandGPUs`
  are also fine (they read `TotalDemand`/`rc.TotalDemand`, which *is* correctly normalized in
  lockstep with `PerReplicaCapacity`).
- Also found, while tracing: `composite := namedResults[0]` is a value copy, but since
  `Result` is a pointer and `RoleCapacities` is a map, `normalizeToCompositeUnits`'s mutation
  reaches `namedResults[0]`'s shared underlying data too. Confirmed not a live bug today
  (`namedResults` isn't read again after `collectV2ModelRequest` returns — metrics/logging
  already ran earlier, inside `runAnalyzersAndScore`) but flagged as fragile.
- Recorded all of this in `spec.md`/STATE.md/`pr-spec-next-coverage-units.md`, with 3
  candidate fixes (A: extend normalization to RC/SC/etc., changing what
  `wva_required_capacity`/`wva_spare_capacity` report; B: parallel coverage-space fields,
  more files touched, metrics untouched; C: reorder the pipeline). Committed as `18ea1f43`.
- **User pushed back hard on stopping at "no test fails" reasoning** — explicitly said don't
  leave fields untouched "just because you can't see a bug right now," be logically
  consistent, and that fixing a comment instead of the actual value is the wrong direction.
  Re-did the investigation properly: grepped every read site of `TotalDemand`,
  `RequiredCapacity`/`SpareCapacity` (model-level, not just Remaining/Spare),
  `TotalSupply`/`TotalAnticipatedSupply`, and both `Utilization` fields (there are two —
  model-level on `NamedAnalyzerResult`, and a different per-*variant* one on
  `domain.VariantCapacity` that feeds `decision.Utilization`; found no code that actually sets
  the per-variant one — a separate loose end, noted but not chased further) across the whole
  `internal/engines` tree, not just `allocation/`.
- This exhaustive pass showed: model-level RC/SC/Remaining/Spare/TotalDemand ARE live-read,
  but only in the non-disaggregated branch (every consumer branches to per-role values
  otherwise, so there's no real "what does TotalDemand mean with multiple roles" ambiguity for
  correctness — it's simply unused once `RoleCapacities` exists). `TotalSupply`/
  `TotalAnticipatedSupply`/model-level `Utilization` have zero consumers after
  `applyUniversalThreshold`/`logAnalyzerResult` complete (both run before normalization) — not
  "we can't see a bug," but a positively verified absence of any downstream reader.
- **User's final decision:** normalize everything anyway (RC/SC/Remaining/Spare + per-role,
  AND TotalSupply/TotalAnticipatedSupply + per-role, AND recompute Utilization) for structural
  consistency with the struct's own documented invariants, even though TotalSupply/
  TotalAnticipatedSupply/Utilization have no live consumer today. `Utilization` noted as
  "probably just coverage under a different name" — no semantic change, just recompute
  consistently. New field `SatRoleDemand map[string]float64` (mirrors `SatDemand` per-role).
  Logging the normalized composite (item 5) confirmed as part of this fix — currently nothing
  observes the actual post-normalization signal the optimizer receives.
- **TODO explicitly deferred, not this fix, not CT7:** user's intuition that model-level
  "non-role" fields shouldn't exist as a parallel representation at all — should require
  `role="both"` explicitly and always go through `RoleCapacities`/per-role access uniformly.
  One confirmed exception that stays role-aware regardless: cross-SO-same-model coverage
  combination differs between `"both"` and other roles (same-role adds, cross-role is
  `min(prefill,decode) + both`). Persisted as a note in `spec.md`'s CT6 section, not scoped
  into any current task.
- Persisted the confirmed design (not implemented — explicitly told not to implement yet) in
  `spec.md`, STATE.md, and `pr-spec-next-coverage-units.md`. Committed as `250f2e8e`.

## Wind-down

- User asked to wind down and retire. Attempted the `wind-down` skill directly via the Skill
  tool — blocked (`disable-model-invocation`); told the user to run `/wind-down` themselves,
  which they did.
- No code was written this session — every change was documentation/spec/state (STATE.md,
  spec.md, the two PR-spec docs, the ledgers move+index). The actual CT6 test-fix commit and
  the correctness-bug code fix are both still pending, explicitly deferred to a future session
  per user instruction ("Do not implement yet").

## Verified 2026-09-06

Ledger-capture pass. Confirmed all 6 referenced commits (`af55afb3`, `e88e48dc`, `ed502057`,
`18ea1f43`, `250f2e8e`, `13de7abf`) are present on `single-analyzer` and each touches the
durable files their commit messages claim (`git show --stat`). Cross-checked every
substantive ledger point against the actual content of `.session/STATE.md`, `.session/spec.md`
(CT6 and CT7 sections), `.session/pr-spec-34-composite-signal.md`, and
`.session/pr-spec-next-coverage-units.md` at `HEAD` — not just that a commit touched the file.

| Ledger point | Durable destination |
|---|---|
| Session start / ownership on agentbus / CONVENTIONS.md read | Process bookkeeping only; own Session log line in `STATE.md` is the durable record — no separate destination needed |
| Finding #1 — uncommitted test-fix, CT6 compile gap traced to `f20e06f9`, verified via `go build`/`go vet`, `engine_v2_compose_test.go` moved to `multi_backup/` | `STATE.md` "Known issues" ("CT6 does not compile as pushed") + `spec.md` CT6 section ("Outstanding gap") |
| Finding #2 — PR #34 already merged, STATE.md's "open/awaiting review" was stale | `STATE.md` PR history (corrected `af55afb3`, refined `ed502057`) + `pr-spec-34-composite-signal.md` |
| Finding #3 — `pr-single-analyzer` worktree is (merged) PR #34; `origin/single-analyzer` tip `d90bd565` confirmed ancestor of local HEAD; no PR branch cut yet for the next payload | `STATE.md` ("Next PR — not yet opened, no branch cut yet"; "PR isolation" key decision) + `pr-spec-next-coverage-units.md` ("Status: NOT YET OPENED... tip `d90bd565`") |
| Finding #4 — PR #2 design doc content, Q1-Q4 open questions | Ported verbatim into `spec.md` CT7 section (Q1-Q4 present); original archived at `.session/ledgers/compose-reduce-design-2026-08-30.md`, indexed in `.session/ledgers/README.md` |
| Finding #5 — untracked docs/plans + `.session/` inventory, stale `spec.md.wip` lock | `.session/ledgers/README.md` indexes all 6 archived files; `spec.md.wip` → `spec.md` rename (lock resolved) per `e88e48dc` |
| Open items 1-5 handed to user | All resolved in subsequent sections, each already checked above (CT6 test-fix status tracked in `STATE.md` Known issues/Next step; PR specs written; leftover-test-changes clarified as required, not optional, in Known issues; docs archived; `.wip` lock resolved) |
| Docs cleanup pass (archival, spec.md rename/fixes, STATE.md Ledgers pointer + PR history) | `e88e48dc` + `.session/ledgers/README.md` |
| p3-planner protocol-violation note (stale path reference to the moved design doc) | Not a single-analyzer durable-doc concern — flagged to the user in the moment, explicitly not this session's file to edit; no destination applicable |
| PR-34 rescope from source + two focused PR specs | `ed502057`; `pr-spec-34-composite-signal.md`; `pr-spec-next-coverage-units.md`; `STATE.md` PR history corrected |
| `runAnalyzersAndScore` loop diff Q&A (old reduce-then-build vs. new build-per-analyzer-then-pick-`[0]`; `buildNamedResult` generic, not sat-specific) | Underlying facts already stated in `spec.md` CT6 ("Outstanding gap") and CT7 ("Intent") sections; this was explanatory Q&A over already-committed code, not a new decision requiring its own entry |
| CT6 correctness bug — found, traced, fix design confirmed (RC/SC/Remaining/Spare/TotalSupply/TotalAnticipatedSupply/Utilization normalization, new `SatRoleDemand` field, aliasing note, deferred "role=both" TODO) | `18ea1f43` + `250f2e8e` into `STATE.md` "Known issues" and `spec.md` CT6 section ("Correctness bug found" / "Fix design — CONFIRMED"); `pr-spec-next-coverage-units.md` |
| Wind-down: no code written, test-fix and bug-fix deferred to next session | `13de7abf` updated `STATE.md`'s "Next step / resume point" accordingly |

**Gap found (flagged, not silently passed):** `13de7abf` (the wind-down commit) updated
`STATE.md`'s "Next step / resume point" but did **not** flip this session's own Session log
entry from `status=active` to `status=retired` — `STATE.md`'s Session log still reads
`2026-09-06T12:00 session=2026-09-06-single-analyzer-2 status=active
ledger=.session/2026-09-06-single-analyzer-2.md` as of current `HEAD`, even though this ledger
records the session as wound down and retired (per `conventions/resume-and-handoff.md`, an
entry is only "fully resolved" once `status=retired` **and** the ledger carries this
`## Verified` marker). This `## Verified` marker is being added now, but the `status=active`
line in `STATE.md` was left unfixed by this verification pass — per the ledger-capture
contract, this session's mandate is to verify and flag, not to edit `STATE.md`'s Session log
on another session's behalf. Someone still needs to flip that line to `status=retired`.

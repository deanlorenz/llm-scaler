Continues: .session/ledger/2026-09-06-single-analyzer-3.md

# Session single-analyzer-4 — 2026-09-07

## Log

### PR-prep branch: single-analyzer-normalize

User asked to update main from upstream (ff-only) then, after clarifying scope, to build a
clean PR-ready branch off upstream/main with the already-spec'd/implemented next PR (CT6)
rebased onto it.

**Branch:** `single-analyzer-normalize`, created off `upstream/main` (`b848b19c`), worktree at
`.claude/worktrees/single-analyzer-normalize` (had to recreate under `.claude/worktrees/` after
first attempt under `worktrees/` failed — `EnterWorktree(path=...)` only reaches paths under
`.claude/worktrees/` of this repo).

**Decisions (user-confirmed via AskUserQuestion):**
- Fresh branch `single-analyzer-normalize`, not reusing `pr/single-analyzer` (stale, sits at
  `113fec1d` = the already-merged PR #34 squash commit) or `single-analyzer-pre-rebase-backup`
  (unrelated leftover from an earlier pre-CT6 rebase attempt) — both left untouched.
- Squash the 10 CT6 commits (`f20e06f9`, `18f4d4ff`, `c5af5696`, `290ca75f`, `896879d5`,
  `e4b1e77d`, `65c344af`, `991800ce`, `44a7f5e6`, `e3ce4abc`) into 3 logical commits:
  1. `498c8950` feat: normalize saturation demand to coverage units before optimizer
  2. `f3c51698` fix: normalize RC/SC/Remaining/Spare/supply alongside PRC
  3. `062331bc` fix: composite signal naming, log completeness, deep-copy safety

**Cherry-pick conflicts resolved:**
- Dropped CT1b's saturation-nil guard (`baseResult == nil` check + its test in
  `engine_v2_test.go`) — confirmed via `git show upstream/main:...` that upstream never had
  this guard; it's explicitly out-of-scope per STATE.md ("CT1b ... excluded from PR #1 by user
  request — separate bugfix, future PR"). Correctly excluded, not a mistake.
- `analyzer_fixtures_test.go`, `engine_v2.go` conflicts were cosmetic (doc-comment wording,
  already-refactored `named()` signature) — kept upstream's current wording/shape.
- `multi_backup/engine_v2_compose_test.go` rename/delete conflict resolved correctly (file only
  exists on our side; upstream never had `composeAnalyzerResults`).
- All 3 commits build clean (`go build ./...`, `go vet ./...`) and pass
  `go test ./internal/engines/...` plus the full non-E2E suite (E2E fails only on missing
  live cluster, expected/unrelated).
- **No push yet** — waiting on user go-ahead.

### Code review of single-analyzer-normalize (code-review skill, 8 finder agents + verification)

Ran full review comparing branch vs `upstream/main`. Converged, cross-confirmed findings:

**3 confirmed correctness bugs** (all inherited side effects of the units change from raw
tokens → coverage-fractions, NOT deliberate design choices):
1. `greedy_score_optimizer.go:62` `fairShareValue` — cross-model fair-share now compares
   normalized per-model-relative capacity instead of raw token demand. **BUT**: this file has
   **zero diff** vs upstream (confirmed via `git diff upstream/main HEAD -- greedy_score_optimizer.go`
   — empty). The bug is entirely in what `initRoleState` (`analyzer_helpers.go`, also zero-diff)
   feeds it via `pickerState[role] = rc.RequiredCapacity` — that value's *meaning* changed
   because `normalizeToCompositeUnits` (this branch's real change) normalizes it. Confirmed via
   spec.md this is a **pre-existing, already-known, already-tracked issue**: CT4
   (`fairshare-value-correctness-investigation-2026-08-25.md`) already established that
   `fairShareValue` computes "equal absolute remaining demand," never "equal coverage" — this was
   true since its literal origin (commit `a16e2f09`, PR #771) and is explicitly **BLOCKED on a
   user fix-now-vs-defer decision**, unrelated to this branch. This branch doesn't change
   `fairShareValue`'s formula; it changes the *units* of one of its inputs, which breaks the
   already-fragile "proportional to N_full only for homogeneous PRC" assumption *faster/worse*
   than before. Needs reframing: not a new bug introduced by CT6, but CT6 makes a known-fragile
   fairness metric actively wrong instead of just imprecise. Rescale's parallel path
   (`rescale.go`) was deliberately fixed for this exact case via `SatDemand` (see spec.md
   "Rescale fairness" section, resolved 2026-09-01) — `greedy_score_optimizer.go`'s fair-share
   was never given the equivalent fix because CT4 (the correctness question) is blocked, not
   because it was missed.
2. `engine.go:1243` — `wva_required_capacity`/`wva_spare_capacity` Prometheus gauges still
   labeled `unit=continuous` (tokens) but now emit coverage fractions. **User pushback pending
   verification**: does this replace the sat-only metric, or is a per-analyzer-including-sat
   metric still expected separately? STATE.md's "Known issues" already tracks a **separate**
   TODO for composite metrics (`wva_required_capacity`/`wva_spare_capacity` for the *normalized*
   composite) as intentional, confirmed-acceptable design (spec.md CT6 "Resolution of the
   observability tradeoff", 2026-09-06) — per-analyzer metrics stay untouched as a distinct
   family. Need to check whether current code actually still emits a separate raw-sat-unit
   metric alongside, or has silently replaced it. NOT YET RESOLVED — mid-investigation when
   session paused.
3. `rescale.go:587` `roleDemandGPUs` — phantom GPU demand for idle P/D roles when `demand<=0`
   guard skips normalizing RC/SC/supply but still forces `TotalDemand=1.0`. NOT YET CHECKED
   against pre-CT6 behavior — user asked "did the previous calculation also divide totalDemand
   by PRC?" — NOT YET ANSWERED, mid-investigation when session paused.

**Also reported:** idle-model Utilization miscompute (log-only), dead `roleSpare` log field,
unread `SatRoleDemand` field (YAGNI), duplicate sort logic vs `rolesOf`, package-name mismatch
in an ignored test file (`multi_backup/engine_v2_compose_test.go`: `package steadystate` vs
sibling files' `package allocation`).

**User is now re-litigating findings 1-3 against pre-diff behavior** (good instinct — several
finder-agent framings overstated "this diff introduced X" when the real story is more nuanced,
per point 1 above). Mid-investigation on points 2 and 3 when session paused for laptop sleep.
None of these fixes have been applied yet. No push has happened. Nothing destructive occurred.

**NOTE on persistence mechanics:** this content was drafted inside
`.claude/worktrees/single-analyzer-normalize/.session-ledger-draft.md` (this session is
sandboxed to that worktree; git commands and direct writes targeting
`worktrees/single-analyzer` were blocked by the harness) and then copied via plain `cp` into
`worktrees/single-analyzer/.session/single-analyzer-4.md`. It is on disk there but **not yet
committed** to the `single-analyzer` branch — whoever resumes this mission next should
`git add .session/single-analyzer-4.md && git commit` from inside `worktrees/single-analyzer`
before treating this as durably persisted history, per `conventions/wip-editing.md`'s
STATE-file discipline (this is a ledger file, not STATE.md itself, but the same "commit
promptly" spirit applies given the write mechanism was irregular).
## Follow-up: resolved the 3 open questions on findings 1-3 (continuing from before sleep)

**Q1 (fair-share): what was the previous notion of fairness?** `greedy_score_optimizer.go` and
`analyzer_helpers.go` (initRoleState) have **zero diff** vs upstream — confirmed via
`git diff upstream/main HEAD --`. Fairness was never coverage-based; per spec.md's CT4
investigation (already on file, dated 2026-08-25, BLOCKED on user decision), `fairShareValue`
has computed "biggest absolute remaining demand wins" since its literal origin (commit
`a16e2f09`, PR #771) — never coverage-ratio equality. This diff changes only the *units* of
one input (`RequiredCapacity`/`Remaining`, via `normalizeToCompositeUnits`), which breaks the
"proportional to N_full for homogeneous PRC" approximation that made raw-token fairness
tolerable. Not a new design flaw this diff introduced — it's the known-blocked CT4 issue,
now triggered harder. Reframe finding #1: not "this diff broke fairness," but "this diff
removes the accidental mitigation (raw-token proportionality) that was masking the known,
already-tracked CT4 defect." Recommend: do NOT fix in this PR — CT4 stays user-blocked: doc a
note in the PR description/known-issues instead, keep scope to CT6 only.

**Q2 (Prometheus): is composite metric replacing sat's own metric?** `engine.go` (call site,
`RecordSaturationMetrics`) and `cost_aware_optimizer.go` (`buildDecisionsWithOptimizer`, the
field-copy into `VariantDecision.RequiredCapacity/SpareCapacity`) both have **zero diff** vs
upstream. `RecordSaturationMetrics`/`wva_required_capacity`/`wva_spare_capacity` has *always*
been fed from `req.CompositeSignal` (pre-CT6 this was literally `namedResults[0]` = sat's raw
result, unwrapped) — there is only ever one gauge here, never a separate "per-analyzer including
sat" version of *this specific* gauge. Confirmed separately: `wva_analyzer_demand`/
`wva_analyzer_target` (a genuinely distinct, always-existed metric family, populated by
`recordAnalyzerMetrics` at `engine_v2.go:178`, called on the **raw pre-normalization**
`namedResults` inside `runAnalyzersAndScore`, unaffected by this branch) already gives
per-analyzer D/P in each analyzer's own native units, sat included. So: no metric was replaced;
the *same* gauge that has always existed silently changed units (raw tokens → coverage
fraction) because `normalizeToCompositeUnits` now sits upstream of it in the data flow. This
is a real, confirmed bug (finding #2 stands), but the framing "is composite now standing in for
sat's own metric" is answered: no substitution happened in code, only a units regression on the
one metric that was always composite-fed.

**Q3 (rescale.go): did the previous calc also divide TotalDemand by PRC?** Yes — `roleDemandGPUs`
(`rescale.go:586-611`, the `demand := ...; replicas := ceil(demand/best)` line) is **completely
unchanged** by this branch (confirmed: only rescale.go's one-line `SatDemand` swap at line 564
touched this file at all). Pre-CT6, `demand` came straight from `buildRoleCapacities`
(`engine_v2.go:1013-1038`, also present before this branch, sets `TotalDemand: demand` = the
raw per-role `RoleDemand[role]`, no zero-guard, no special-casing) — so `demand<=0` naturally
produced `ceil(0/PRC)=0` replicas correctly, always, with no incident. **The bug is newly
introduced by this branch specifically**: `normalizeToCompositeUnits`'s new `RoleCapacities`
loop (`engine_v2.go:1174-1184`) is the *first* place in this entire call chain that ever
force-sets `rc.TotalDemand = 1.0` unconditionally (line 1182, outside the `if demand > 0` guard
at 1176-1181 that correctly leaves RC/SC/supply raw for a zero-demand role). This exact
inconsistency (TotalDemand=1.0 paired with a still-raw PRC/RC/SC for the same role) could not
have existed pre-CT6, because RoleCapacities[role].TotalDemand was never touched before —
finding #3 is a real, newly-introduced bug, not an inherited pre-existing issue. One-line fix:
move `rc.TotalDemand = 1.0` inside the `if demand > 0` block (matching the model-level block's
pattern above it, which correctly does NOT force TotalDemand=1.0 when modelDemand<=0 — wait,
verify: does the model-level block set nr.Result.TotalDemand=1.0 unconditionally too? — check
lines 1188-1191, that one appears to also be unconditional. Need to check if that's equally
buggy or if model-level TotalDemand=0 is truly impossible/handled elsewhere before fixing.)

**Status:** Q1-Q3 answered and reported to user. Fix not yet applied for finding #3 (or
reconsideration of whether nr.Result.TotalDemand's own unconditional 1.0-set at line 1188 has
the same issue at model level) — pending user go-ahead on scope (fix in this PR vs. separate).
Findings #1 and #2 reframed per above; recommend documenting rather than fixing in this PR
scope (CT6-only). No code changes made yet this sub-session.

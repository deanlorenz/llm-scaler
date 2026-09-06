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

- **Plan / spec:** `.session/spec.md.wip`
  *(do not read upfront — pull on demand only)*
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
- [ ] PR #2 — engine-side reduce to wire non-saturation analyzers into CompositeSignal

**Last completed:** CT6 — Normalize sat→composite to coverage units (commit `f20e06f9`, 2026-09-01)

**Next step / resume point:** Decide whether CT4 fairness fix is in scope for PR #2; then plan
PR #2 (engine-side reduce). Confirm with user before executing.

### Status

- PR #34 open at upstream (https://github.com/ev-shindin/llm-scaler/pull/34), marked ready for
  review 2026-09-01. CI fully green. Awaiting upstream review.
- CT4 blocked on user decision.
- CT1b deferred to future PR.

### Known issues

- **CT4 fairness:** `fairShareValue` equalizes absolute remaining demand, not coverage ratio.
  Fix-now vs. document-and-defer is the user's call. See spec CT4 section and
  `worktrees/session-tracking/missions/single-analyzer/fairshare-value-correctness-investigation-2026-08-25.md`.
- **Rescale weight long-term fix:** token weight is proportional to N_full only for homogeneous
  PRC; longer-term fix tracked in spec rescale-fairness section (separate CT).
- **`spec.md.wip`** has a stale `.wip` suffix — it is the active spec; rename to `spec.md`
  when next editing it.

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
  optimizer helpers for the planned engine-side reduce (PR #2).

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

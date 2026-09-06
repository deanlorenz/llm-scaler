# benchmark-extract

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
  *(read this first, before any other file)*
- **What / goal / mission:** Build and maintain `hack/benchmark/extract.py` — the extractor that reads raw benchmark run directories and produces structured JSON bundles consumed by the viz agent. Output bundles live at `hack/benchmark/results/<run>/` and at `<run>/extract/` (production).
- **Worktree:** `worktrees/benchmark-extract` (branch `benchmark-extract`)
- **Role / scope:** Mission owner — owns the extractor code, `.session/STATE.md`, and integration decisions.
- **Ledger / log:** `.session/<slug>.md`
  ⚠ DO NOT READ — not yours; new session creates its own ledger

## Task

- **Plan / spec:** `hack/benchmark/results/EXTRACTION-NOTES.md` — session-by-session summary of what changed; `plans/benchmark-viz/input-contract.md` in `benchmark-plan` worktree — viz input contract the extractor must satisfy.
  *(do not read upfront — pull on demand only)*
- **Context:**
  - `hack/benchmark/extract.py` — the extractor (v0.4.0)
  - `hack/benchmark/results/EXTRACTION-NOTES.md` — state of all 5 extracted runs
  - `hack/benchmark/results/CODE-REVIEW.md` — known bugs to fix (priority-ordered)
- **Refs:**
  - `hack/benchmark/results/burst-variant1/` — extracted bundle (inference-perf, largest)
  - `hack/benchmark/results/guidellm-decode-heavy/` — extracted bundle (guidellm)
  - `hack/benchmark/results/quick-smoke-inference-perf/`, `early-inference-perf/`, `burst-variant2/`
- **Expected output:** Fixed extractor; re-extracted bundles validating all fixes; updated EXTRACTION-NOTES.md
- **Done / completion criteria:** All HIGH bugs from CODE-REVIEW.md fixed; bundles re-extracted cleanly (exit 0); EXTRACTION-NOTES.md updated to reflect session 5 changes
- **Limits:** Do not touch any file outside `worktrees/benchmark-extract/`. Do not modify viz or runtools worktrees.

## Execution

### Steps / subtasks

- [x] Session 1–4: extractor v0.1→v0.4.0; all 5 runs extracted; histogram backfill complete
- [ ] Fix HIGH-3 — `IndexError` on exact-length metric lines (`startswith` + `len` guard)
- [ ] Fix HIGH-1 — `qwait_s` double-call and boolean coercion  *(check: may already be fixed)*
- [ ] Fix MEDIUM-1 — negative histogram sum not guarded (`ds < 0` check in `_hist_mean_ms`)
- [ ] Fix MEDIUM-2 — JSON array parser breaks on `{`/`}` inside strings (`iter_json_objects`)
- [ ] Fix HIGH-2 — dead/broken `remaining_deploy` variable
- [ ] Fix HIGH-4 — `--head` limit not cumulative across stage files
- [ ] Fix MEDIUM-3 — remove dead regex patterns `_SCALE_TARGET_RE` / `_SCALE_TARGET_RE2`
- [ ] Fix LOW-2 — stage file regex not end-anchored
- [ ] Fix LOW-1 — named constants for magic numbers in `compute_time_anchor`
- [ ] Re-extract all 5 runs; confirm exit 0 and bundle integrity
- [ ] Update `hack/benchmark/results/EXTRACTION-NOTES.md` for session 5
- [ ] Commit extractor changes on `benchmark-extract` branch

**Last completed:** Session 4 — histogram backfill (ttft_p50 from vllm-scrape); all 5 runs extracted at v0.4.0

**Next step / resume point:** Fix HIGH-3 (`IndexError` guard) — verify whether already applied; if not, patch lines 552, 567, 575, 584, 589 of extract.py; then proceed in priority order from CODE-REVIEW.md.

### Status

- Extractor: v0.4.0, functional, all 5 runs extracted
- Pending: bug-fix pass (CODE-REVIEW.md findings)
- No blockers

### Known issues

- EPP metrics FAIL (all runs): bearer token auth broken in runtools — tracked, not extractors fault
- IGW/Envoy access log 0 bytes: no collector wired in runtools
- Scaler log missing: quick-smoke, early-inference-perf have no `controller.log`

## Session log
- 2026-09-06 session=2026-09-06-benchmark-extract-1 status=retired ledger=.session/2026-09-06-benchmark-extract-1.md

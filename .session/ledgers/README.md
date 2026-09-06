# Ledgers index — single-analyzer

Point-in-time session captures: call-traces, design investigations, and superseded snapshots.
Not live reference — the durable decisions and current design live in `.session/spec.md` and
`.session/STATE.md`. Read an entry here only when digging into history (why a decision was
made, what a step actually changed), never as a source of current fact.

| File | What it is | Superseded / current status |
|---|---|---|
| `compose-reduce-design-2026-08-30.md` | Design notes for the engine-side analyzer reduce (floor invariant, implied-replica-count reduce, 4 open questions). Written as a design doc but is really a session capture. | **Ported into `spec.md`'s CT7 section** (2026-09-06). Read `spec.md` for the current version of this design; this file is the historical record only. |
| `engine-call-map-2026-08-30.md` | Call map of every changed site in `engine_v2.go` for the CT2→CT5 refactor. Base `c6e408c4`, tip `fcf9c905`. | **Stale.** Predates CT6 — its claim that `runAnalyzersAndScore` returns a single value is no longer true (CT6 changed it to `[]allocation.NamedAnalyzerResult`). |
| `engine-call-stack-2026-08-30.md` | Diff-style call stack for the engine-side CT2 change only (narrower scope than the call-map above). | **Stale**, same CT6 caveat — explicitly claims `runAnalyzersAndScore` "unchanged," which CT6 later changed. |
| `optimizer-call-map-2026-08-30.md` | Call map of every changed optimizer-side site for CT2/CT3b/CT5 (the 7 simplified helpers, `RolePairedState` type change, etc.). Tip `fcf9c905`. | Accurate for CT2/CT3b/CT5; doesn't cover CT6 (CT6 didn't touch these files further, so likely still current — not independently re-verified this session). |
| `optimizer-call-stack-2026-08-30.md` | Diff-style call stack for the same CT2/CT3b/CT5 optimizer changes, with an equivalence table per helper. Tip `fcf9c905`. | Same as above. |
| `single-analyzer-call-map-2026-08-30.md` | Combined engine+optimizer call map across all three then-current commits (`e4106109`, `b980f682`, `fcf9c905`). Includes §8: the 3 tests `Skip()`-ed pending the multi-analyzer reduce (T1.4 and two population tests), and §9: what wasn't simplified yet. | **Stale for the engine side** (predates CT6); §8's skipped-test list is still accurate and is referenced from CT7 in `spec.md`. |
| `spec.md.local` | An earlier snapshot of the spec, ending at CT5 (no CT6 section at all). | **Fully superseded** by `.session/spec.md`. Kept only in case CT6-era edits ever need to be diffed against the pre-CT6 spec text. |

## Why these moved here (2026-09-06)

All 6 were sitting untracked in `docs/plans/analyzers/` — AGENTS.md's `docs/plans/<area>/` is
meant for durable, current agent plans, not point-in-time session captures. These are
captures: several are already factually stale (superseded by CT6), and the one genuine design
doc (`compose-reduce-design-2026-08-30.md`) had unresolved open questions that belonged in the
spec, not parked in a ledger. Moved to `.session/ledgers/` to match the existing ledger
pattern (see also `worktrees/session-tracking/missions/single-analyzer/ledgers/` for this
mission's earlier ledgers, and the dated session ledgers directly under `.session/`).

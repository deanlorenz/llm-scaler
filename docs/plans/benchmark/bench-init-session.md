# bench-init — Session State (sleep save)

## Context

Working in worktree: `worktrees/benchmark-init` (branch: `benchmark-init`, base: `feat/wva-external-scaler`)

This is a planning session. No code has been written yet.

---

## Pipeline Position

bench-init is the first stage of a 4-stage benchmark pipeline:

```
bench-init → bench-runtools → bench-extract → bench-viz
```

Key constraint: the pipeline does NOT use `llmdbench.py` and does NOT require cloning the `llm-d-benchmark` repo.

---

## bench-init Mission (confirmed)

**bench-init initializes a benchmark run against an already-configured cluster.**

It does NOT touch cluster topology, deployments, or variants — those exist already.
It does NOT add variants.

It answers: *"is this cluster in a valid, ready state to run a benchmark right now?"*

### Owns:
- Environment contract — read/validate the env file for the target namespace
- Preflight checks — cluster healthy, workloads up, no drift
- ScaledObject verification — `modelID` in every trigger matches the live Deployment (the modelID-drift gate)
- Shared-cluster safety rules — guard against running when unsafe (GPU contention, another run in progress, etc.)
- Reset/clean state — ensure leftover state from a prior run doesn't contaminate this one
- GPU reservation — claim GPUs needed for the run before handing off to bench-runtools

### Does NOT own:
- Adding variants or changing cluster topology
- Running the workload (bench-runtools)
- Collecting metrics (bench-runtools)
- Extracting data (bench-extract)
- Visualizations/reports (bench-viz)

### Handoff contract:
bench-init exits cleanly → bench-runtools can trust the cluster is verified and ready.

---

## Commit Mapping (from benchmark-plan worktree)

Source: `/home/dean/code/llm-d/dean-llmd-scaler-sandbox/worktrees/benchmark-plan/plans/commit-mapping.md`

The `bench-prepare` section maps to this worktree (`worktrees/benchmark-init`, branch `benchmark-init`):

| # | Hash | Message | Note |
|---|------|---------|------|
| 1 | `6f4cbf1d` | port shared-cluster safety scaffolding (env_guard, env_wizard, preflight, reset_run) | Core safety/env contract |
| 2 | `ffa87255` | port GPU reservation/coupler tooling | Pre-run cluster prep |
| 3 | `52851b63` | fix gitignore llmdbenchmark workspace-dir pattern | Housekeeping |
| 4 | `ebbcdd50` | rescan/verify ScaledObject modelID before every benchmark run | Preflight gate |
| 5 | `88a9d75b` | add_variant.py support for optimized-baseline topology | Variant setup = cluster prep |

All 5 commits confirmed present in git history. All straight cherry-picks — no splits.

**Status of commit 5 (`88a9d75b`):** The `add_variant.py` script already exists in `hack/benchmark/`.
Need to confirm at resume whether this commit's changes are already present or still needed.

---

## Key Background Docs Read

- `worktrees/benchmark-plan/plans/commit-mapping.md` — the authoritative cherry-pick map
- `worktrees/benchmark-plan/plans/benchmark/worktree-tasks.md` — sibling worktree task definitions
- `worktrees/benchmark-plan/plans/benchmark/observability-gaps.md` — full observability audit
  - §5: modelID drift incident (the preflight gate this worktree implements)
  - §6: WVA demand signal analysis
- `worktrees/benchmark-plan/plans/benchmark/cycle-log.md` — WVA cycle log format reference
- `docs/plans/engine/keda-driven-discovery.md` — KEDA-driven discovery (current branch context)
- `hack/benchmark/` — existing benchmark tooling in this repo

---

## Open Questions (not yet answered)

1. Exact scope of the preflight checks — what conditions block a run vs warn only?
2. Full init sequence end-to-end — is there a Makefile target design to define?
3. Whether commit 5 (`88a9d75b` — `add_variant.py` optimized-baseline topology) is truly
   in scope given the confirmed mission (bench-init does NOT add variants). Needs clarification
   at resume — the commit-mapping includes it under "Variant setup = cluster prep" but the
   mission explicitly excludes variant management.

---

## Next Step at Resume

Continue defining the exact mission scope before writing the plan file. Specifically:
- Resolve open question #3 about commit 5
- Define the preflight check scope and pass/warn/fail semantics
- Define the Makefile target(s) interface
- Then write the plan file

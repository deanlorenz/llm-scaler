# bench-init — Session State (sleep save)

## Context

Working in worktree: `worktrees/benchmark-init` (branch: `benchmark-init`, base: `feat/wva-external-scaler`)

---

## Pipeline Position

```
bench-init → bench-runtools → bench-extract → bench-viz
```

Key constraint: the pipeline does NOT use `llmdbench.py` and does NOT require cloning the `llm-d-benchmark` repo.

---

## bench-init Mission (confirmed, final)

**bench-init is a read-only stack discovery script** that writes `bench-meta.json`.

It does NOT touch cluster topology, deployments, or variants.
It does NOT add variants (commit 5 is out of scope — `add_variant.py` already present).

Primary deliverable: `hack/benchmark/bench_init.sh`

### Handoff contract:
bench-init writes `bench-meta.json` → bench-runtools reads it and runs the workload.

---

## Commit Mapping

| # | Hash | Message | Status |
|---|------|---------|--------|
| 1 | `6f4cbf1d` | port shared-cluster safety scaffolding (env_guard, env_wizard, preflight, reset_run) | ❌ pending |
| 2 | `ffa87255` | port GPU reservation/coupler tooling | ❌ pending |
| 3 | `52851b63` | fix gitignore llmdbenchmark workspace-dir pattern | ❌ pending |
| 4 | `ebbcdd50` | rescan/verify ScaledObject modelID before every benchmark run | ❌ pending |
| 5 | `88a9d75b` | add_variant.py support for optimized-baseline topology | ✅ already present, out of scope |

Cherry-pick order: 3 → 1 → 2 → 4 (gitignore first, no conflicts)

---

## What's Done

All committed in `241f6185`:

- `hack/benchmark/bench_init.sh` — **COMPLETE, syntax-validated**
  - Guards: kubeconfig export, context match, namespace exists
  - Discovers: ScaledObjects → stacks[], WVA controller, HF token secret, Prometheus
  - Calls `resolve_router_endpoint.sh` (owned by bench-runtools)
  - Writes bench-meta.json atomically
  - `bash -n` passes, `--help` works

- `docs/plans/benchmark/bench-design-notes.md` — authoritative schema spec ✅
- `docs/plans/benchmark/bench-init-decisions.md` — decisions/findings log ✅
- `docs/plans/benchmark/bench-init-plan.md` — implementation plan ✅

---

## Key Background Docs Read

- `worktrees/benchmark-runtools/docs/plans/benchmark/bench-design-notes.md` — canonical schema (runtools authored)
- `worktrees/benchmark-runtools/docs/plans/benchmark/benchmark-runtools-plan.md` — full runtools plan
- `worktrees/benchmark-runtools/hack/benchmark/dhl-la-1708.env` — live env file
- `worktrees/benchmark-runtools/hack/benchmark/resolve_router_endpoint.sh` — confirmed exists, signature known
- `worktrees/benchmark-runtools/hack/benchmark/run_session.sh` — header style reference
- `worktrees/benchmark-runtools/hack/benchmark/run_scenario.sh` — header style reference

---

## Next Steps at Resume

1. **Cherry-pick the 4 commits** in order: `52851b63` → `6f4cbf1d` → `ffa87255` → `ebbcdd50`
   - These add env_guard, env_wizard, preflight, reset_run, GPU reservation, modelID verify
   - All straight picks, no splits

2. **Review cherry-picked scripts** — they were written for the legacy `benchmark-*` path.
   Check if they reference `BENCHMARK_NAMESPACE` (legacy) vs `BENCH_NAMESPACE` (new pipeline).
   May need minor adaptation to use the new env var names.

3. **Add Makefile target `bench-init`** — wires `bench_init.sh` as a make target:
   ```
   bench-init: ## Discover stack state and write bench-meta.json (BENCH_ENV_FILE=<file>)
   ```
   Following the `bench-*` naming convention (not `benchmark-*`).

4. **Validate end-to-end** against dhl-la-1708 once cluster is reachable.

---

## Known Issues / Notes

- The shebang line had a stray `i` prefix (user revision artifact) — fixed before commit.
- `bench-design-notes.md` has a stray `e` prefix on line 1 (user revision artifact) — cosmetic only, does not affect function.
- `bench-init-plan.md` has a stray `n` prefix on the first paragraph — cosmetic only.
- `bench_init.sh` has a redundant/broken intermediate attempt at parsing `wva_args` (lines ~103-109) — the correct path follows immediately after using `kubectl get ... -o json | python3`. Should be cleaned up.

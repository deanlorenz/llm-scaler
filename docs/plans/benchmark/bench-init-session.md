# bench-init — Session State

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

Primary deliverable: `hack/benchmark/bench_init.sh`

### Handoff contract:
bench-init writes `bench-meta.json` → bench-runtools reads it and runs the workload.

---

## Status: COMPLETE

All work is committed. Branch is ready for PR / cherry-pick into bench-runtools.

---

## Commit log (de917424..HEAD)

| Hash | Message |
|------|---------|
| `86b7f4a5` | fix(gitignore): correct llmdbenchmark workspace-dir pattern; add live smoke-test sample |
| `554ce453` | feat(benchmark): port shared-cluster safety scaffolding (env_guard, env_wizard, preflight, reset_run, Makefile targets, quick_smoke.yaml.in) |
| `596257a7` | feat(benchmark): port GPU reservation/coupler tooling |
| `f34f9947` | feat(benchmark): verify_wva_scaledobjects.sh + benchmark-verify-scaledobjects wired into benchmark-run |
| `2b1d8175` | fix(benchmark): remove dead wva_args first-attempt from bench_init.sh |
| `c5005359` | fix(benchmark): rewrite verify_wva_scaledobjects.sh — self-contained, no deploy/lib deps |
| `3a340fe9` | feat(benchmark): add --model targeted mode to verify_wva_scaledobjects.sh; wire into benchmark-run |
| `8346429e` | feat(benchmark): port resolve_router_endpoint.sh; bench_init.sh fully self-contained |

---

## Key design decisions made this session

- **verify_wva_scaledobjects.sh is self-contained**: 2 kubectl calls + stdlib Python. No deploy/lib sourcing, no env files, no install-tooling. Works against any llm-d+WVA install regardless of how it was created.
- **verify_wva_scaledobjects.sh has two modes**: `--model <id>` (targeted, for benchmark-run) and no-arg scan (for standalone benchmark-verify-scaledobjects). benchmark-run passes `--model $(BENCHMARK_MODEL_ID)`.
- **BENCHMARK_NAMESPACE vs BENCH_NAMESPACE**: scripts use `BENCHMARK_NAMESPACE` (Makefile-level); `bench_init.sh` uses `BENCH_NAMESPACE` (direct invocation). Different entry points — no adaptation needed.
- **fix/scaledobjects-cold-start**: scaledobjects-plan writes wrong scalerAddress when run cold (no WVA_NS/NAMESPACE set). Spec committed to `worktrees/fix-scaledobjects-cold-start/docs/plans/deploy/fix-scaledobjects-cold-start.md`. Out of scope here — dedicated agent.

---

## Cherry-pick message for bench-runtools agent

```
git cherry-pick 86b7f4a5 554ce453 596257a7 f34f9947 2b1d8175 c5005359 3a340fe9 8346429e
```

Watch for conflicts on Makefile and verify_wva_scaledobjects.sh — take benchmark-init's version of the latter.

---

## Open items / follow-on

- End-to-end validation against dhl-la-1708 (once cluster reachable)
- fix/scaledobjects-cold-start: wva_bootstrap_env cold-start discovery (separate agent, separate branch)

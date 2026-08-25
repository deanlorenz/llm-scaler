# bench-init Plan

## Context

**Worktree:** `worktrees/benchmark-init` (branch: `benchmark-init`, base: `feat/wva-external-scaler`)
**Pipeline position:** bench-init → bench-runtools → bench-extract → bench-viz

nbench-init prepares and verifies the cluster, then discovers the stack state and
writes `bench-meta.json` for bench-runtools to consume.

---

## Deliverables

| File | What |
|------|------|
| `hack/benchmark/bench_init.sh` | Read-only stack discovery script; writes `bench-meta.json` |
| `docs/plans/benchmark/bench-design-notes.md` | Authoritative schema spec (shared contract) ✅ done |
| `docs/plans/benchmark/bench-init-decisions.md` | Running decisions/findings log ✅ done |

Plus the 4 cherry-picked safety scaffolding scripts (see below).

---

## Cherry-Picks (4 commits, in order)

| Order | Hash | What | Status |
|-------|------|------|--------|
| 1 | `52851b63` | gitignore fix (llmdbenchmark workspace-dir pattern) | ❌ pending |
| 2 | `6f4cbf1d` | env_guard.sh, env_wizard.sh, preflight.sh, reset_run.sh | ❌ pending |
| 3 | `ffa87255` | gpu_reservation.sh / gpu_coupler tooling | ❌ pending |
| 4 | `ebbcdd50` | verify_scaledobject_modelid.py | ❌ pending |

Commit 5 (`88a9d75b` — `add_variant.py`) is **out of scope** — adds variants,
not pre-run initialization. Already present in this branch anyway.

---

## bench_init.sh — Implementation Plan

### Location
`hack/benchmark/bench_init.sh`

### What it does
Read-only stack discovery. No deploying, scaling, or modifying anything.
Writes `hack/benchmark/bench-scratch/<namespace>/bench-meta.json`.

### Inputs
- `$1` — namespace (also `BENCH_NAMESPACE` env)
- `$2` — optional output dir override
- `BENCH_KUBECONFIG` — if set, exported as `KUBECONFIG`
- `BENCH_KUBE_CONTEXT` — if set, verified against current context
- `BENCH_IMAGE_TAG` — if set, used for harness drift check

### Guards (fail fast)
1. Namespace arg present (`$1` or `BENCH_NAMESPACE`)
2. `BENCH_KUBECONFIG` set → export as `KUBECONFIG`
3. `BENCH_KUBE_CONTEXT` set → `kubectl config current-context` must match
4. Namespace exists → `kubectl get namespace $NS`

### Discovery sequence
1. List all ScaledObjects in namespace → one `stacks[]` entry per SO
2. For each SO:
   - `name`, `scaledobject` = SO name
   - `deployment` = `.spec.scaleTargetRef.name`
   - `model_id` = trigger `.metadata.modelID` → fallback: first positional arg of Deployment container args
   - `endpoint_url` = call `bash hack/benchmark/resolve_router_endpoint.sh $NS`
   - `epp_metrics_secret` = secret with label `app.kubernetes.io/name=workload-variant-autoscaler`
   - `vllm_pod_label` = first of `llm-d.ai/role=decode`, `app.kubernetes.io/component=decode`, `app=<deploy-name>` that returns ≥1 pod
   - `vllm_metrics_port` = 8200 (fixed)
   - `epp_metrics_port` = 9090 (fixed)
   - `min_replicas`, `max_replicas` = SO `.spec.minReplicaCount` / `.spec.maxReplicaCount`
   - `so_paused` = annotation `autoscaling.keda.sh/paused-replicas` present
   - `ready_replicas` = Deployment `.status.readyReplicas` (0 if absent)
3. WVA block: deployment + metrics service by label `app.kubernetes.io/name=workload-variant-autoscaler`, parse `--metrics-bind-address` and `--metrics-secure` from container args
4. `hf_token_secret`: secret name matching `*hf*token*`
5. Prometheus: Thanos → kube-prometheus-stack → in-namespace → unknown
6. Drift check: if harness pod running and `BENCH_IMAGE_TAG` set → warn on mismatch

### Output
Atomic write: temp file → `mv` to `hack/benchmark/bench-scratch/<namespace>/bench-meta.json`  
Final stdout line: output path.

### JSON assembly
Python3 inline heredoc — no `jq` dependency.

### Style
- `set -euo pipefail`
- Header comment block matching `run_session.sh` / `run_scenario.sh` style
- `--help` prints usage
- `bash -n` must pass

---

## Validation

```bash
# Syntax
bash -n hack/benchmark/bench_init.sh

# Usage
bash hack/benchmark/bench_init.sh --help

# Live cluster (dhl-la-1708) — expected assertions:
# stacks length = 2
# stacks[0].model_id = "Qwen/Qwen3-0.6B"
# stacks[1].so_paused = true
# wva.metrics_secure = true
# prometheus.type = "thanos"
```

---

## Reference

- Schema spec: [`docs/plans/benchmark/bench-design-notes.md`](docs/plans/benchmark/bench-design-notes.md)
- Decisions log: [`docs/plans/benchmark/bench-init-decisions.md`](docs/plans/benchmark/bench-init-decisions.md)
- `resolve_router_endpoint.sh` owned by bench-runtools worktree

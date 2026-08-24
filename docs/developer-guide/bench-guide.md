# bench-* Guide — Running Benchmarks Against an Already-Running Stack

This guide covers the `bench-*` Make targets, which run benchmarks against an already-running
llm-d stack **without touching the stack itself**. No `llmdbenchmark` Python CLI, no helmfile,
no clone of `llm-d-benchmark` is required — only `kubectl` and `bash`.

For the legacy full-lifecycle flow (standup → run → teardown via the `llmdbenchmark` CLI),
see [`benchmark-guide.md`](benchmark-guide.md).

---

## Prerequisites

1. A running llm-d stack with vLLM decode pods and an EPP in the target namespace.
2. The WVA controller deployed (installed by [`benchmark-init`](../../plans/benchmark/benchmark-runtools-plan.md) or manually).
3. `kubectl` pointing at the right cluster and namespace.
4. `MODEL_ID` (or `BENCH_MODEL_ID`) set to the model being served.

---

## Quick Start

Each namespace has a `hack/benchmark/<ns>.env` file that captures its full config.
The recommended workflow mirrors the `benchmark-run-only` convention:

```bash
# Option A — source the env file in your shell first (recommended):
set -a && source hack/benchmark/dhl-la-1708.env && set +a
make bench-run                              # uses defaults from the env file
make bench-run BENCH_WORKLOAD=symmetrical  # override a single variable

# Option B — single-line convenience with BENCH_ENV_FILE:
make bench-run BENCH_ENV_FILE=hack/benchmark/dhl-la-1708.env
make bench-run BENCH_ENV_FILE=hack/benchmark/dhl-la-1708.env BENCH_WORKLOAD=symmetrical

# Run all scenarios sequentially
make bench-run-all BENCH_ENV_FILE=hack/benchmark/dhl-la-1708.env

# Tear down the harness pod when done
make bench-teardown BENCH_ENV_FILE=hack/benchmark/dhl-la-1708.env
```

---

## Target Reference

| Target | Description |
|---|---|
| `bench-run-check` | Read-only preflight: namespace exists, scenario file present, model set |
| `bench-run` | Run one scenario; harness pod persists after the run |
| `bench-run-all` | Run all `test/benchmark/scenarios/*.yaml.in` sequentially |
| `bench-full` | Alias for `bench-run-all` (no automatic teardown) |
| `bench-teardown` | Explicitly tear down the harness pod and its RBAC |
| `bench-guard` | Internal prereq marker (parallel to `benchmark-guard`) |

### Key Variables

| Variable | Default | Description |
|---|---|---|
| `BENCH_ENV_FILE` | _(none)_ | Path to a per-NS env file; sourced at recipe start |
| `BENCH_NAMESPACE` | `$(BENCHMARK_NAMESPACE)` | Kubernetes namespace |
| `BENCH_HARNESS` | `guidellm` | Harness type: `guidellm` or `inference-perf` |
| `BENCH_WORKLOAD` | `prefill_heavy` | Scenario name (without `.yaml.in`) |
| `BENCH_MODEL_ID` | `$(BENCHMARK_MODEL_ID)` | Model ID forwarded into the scenario profile |
| `BENCH_ENDPOINT_URL` | _(auto-detect)_ | Override inference endpoint URL |
| `BENCH_EPP_METRICS_SECRET` | `epp-metrics-token` | EPP metrics secret name (override for non-standard installs) |
| `BENCH_SESSION_DIR` | `hack/benchmark/bench-scratch/<ns>-<ts>/` | Local results directory |
| `BENCH_IMAGE_TAG` | `$(BENCHMARK_REPO_REF)` | Harness image tag |
| `BENCH_INTER_SCENARIO_HOOK` | _(none)_ | Client-side script run between scenarios |
| `BENCHMARK_PROMETHEUS_URL` | _(auto-detect)_ | Prometheus/Thanos URL for range query |

---

## Session Lifecycle

```
bench-run-check    ← fast read-only preflight
bench-run          ← creates harness pod (if needed), runs one scenario
  │
  ├── run_session.sh ensure    ← create pod + RBAC if not already running
  ├── run_scenario.sh          ← render profile, exec harness, collect results
  │     ├── scrape_wva_metrics.sh start   (client-side WVA scraper)
  │     ├── kubectl exec llm-d-benchmark.sh  (load generator inside pod)
  │     ├── scrape_wva_metrics.sh stop
  │     ├── kubectl cp results → <session-dir>/<workload>/results/
  │     └── scrape_prometheus_range.sh    (post-scenario range query)
  └── pod remains running (cheap to keep idle; inspect results any time)

bench-teardown     ← explicit pod + RBAC removal; never automatic
```

The harness pod is created once and persists across all scenarios in a session.
Only `bench-teardown` removes it.

---

## What Each Scenario Run Produces

Under `<BENCH_SESSION_DIR>/<workload_name>/`:

```
results/                ← kubectl cp'd from pod's /requests/<harness>_<exp_id>_<stack>/
  results.json          ← guidellm benchmark results
  metrics/
    raw/                ← vLLM and EPP per-pod Prometheus scrapes (collected in-pod)
wva-metrics/            ← WVA controller /metrics (client-side port-forward scraper)
  wva-controller_<epoch>_metrics.log
prometheus_range.json   ← post-scenario Prometheus range query (all wva_*/vllm_* series)
scenario_meta.json      ← start/end epoch, parameters, experiment_id
```

---

## Harness Pod

The pod runs `ghcr.io/llm-d/llm-d-benchmark:<BENCH_IMAGE_TAG>` with:

- `serviceAccountName: llmdbench-harness-sa` (namespace-scoped Role with pods/log + EPP secret access)
- `runAsUser: 0` (required by harness entrypoint)
- Two in-pod patches applied after Ready:
  1. `process_epp_logs.py` — EPP float timestamp fix (upstream v0.7.8 bug)
  2. `guidellm-analyze_results.sh` — make report conversion non-fatal (upstream bug)

The pod spec and RBAC are idempotent; re-running `bench-run` on an already-running pod is a no-op.

---

## WVA Metrics Collection

`scrape_wva_metrics.sh` runs client-side, using a `kubectl port-forward` to the WVA controller
metrics service (authenticated HTTPS on port 8443). It scrapes every 15 seconds (override with
`WVA_SCRAPE_INTERVAL`) and writes `wva-controller_<epoch>_metrics.log` files in the same naming
convention that `extract_real_trace.py` already parses.

Set `BENCH_SKIP_WVA_SCRAPE=true` to disable if the WVA service is not reachable.

---

## Post-Scenario Prometheus Range Query

`scrape_prometheus_range.sh` runs after each scenario and queries Prometheus/Thanos for the
exact time window of the run. It collects `wva_*` and `vllm_*` series filtered to the namespace.

Auto-detection order:
1. `BENCHMARK_PROMETHEUS_URL` env / Makefile variable
2. OpenShift Thanos querier (`openshift-monitoring` namespace)
3. Prometheus service in the benchmark namespace
4. kube-prometheus-stack in the `monitoring` namespace

Set `BENCH_SKIP_PROMETHEUS=true` to skip if Prometheus is unreachable.
The step size is automatically tuned: `max(15s, range/120)`.

---

## Scenarios

All `.yaml.in` files under `test/benchmark/scenarios/` are available as `BENCH_WORKLOAD` values:

```
burst_4k1000    burst_4k250    bursty         decode_heavy   prefill_heavy
sharegpt_inferenceperf    static-baseline-gptoss120b    symmetrical
```

Tokens substituted before the profile reaches the harness:

| Token | Source |
|---|---|
| `__REQUEST_RATE__` | `BENCH_REQUEST_RATE` (default: 10) |
| `__MAX_DURATION__` | `BENCH_MAX_DURATION` (default: 600) |
| `REPLACE_ENV_LLMDBENCH_DEPLOY_CURRENT_MODEL` | `BENCH_MODEL_ID` |
| `REPLACE_ENV_LLMDBENCH_HARNESS_STACK_ENDPOINT_URL` | `BENCH_ENDPOINT_URL` |

---

## Multi-Scenario Sessions with an Inter-Scenario Hook

```bash
make bench-run-all \
    BENCH_NAMESPACE=my-namespace \
    BENCH_MODEL_ID=Qwen/Qwen3-0.6B \
    BENCH_INTER_SCENARIO_HOOK=./my-reset-script.sh
```

The hook receives `<workload_name> <namespace>` as positional arguments and is run
between each scenario pair. Use it to park GPUs, reset queue state, or sleep.
Hook failures are non-fatal (the session continues).

---

## Per-Namespace Env Files

Each namespace that has been used for benchmarking has a `hack/benchmark/<ns>.env` file:

| File | Cluster | Notes |
|---|---|---|
| `dhl-la-1708.env` | pokprod | Two-variant setup; v1 only (v2 node has broken GPU); EPP secret `wva-epp-metrics-token` |
| `dhl-e2e-231.env` | pokprod | Single-variant; used by `benchmark-run-only` |

The env files follow the same `VAR=value` (no `export`) convention as `dhl-e2e-231.env`,
making them valid for both `set -a && source` in a shell and `BENCH_ENV_FILE=` in Make.

### Creating a new env file

```bash
cp hack/benchmark/dhl-la-1708.env hack/benchmark/<new-ns>.env
# Edit: BENCH_NAMESPACE, BENCH_MODEL_ID, BENCH_ENDPOINT_URL, BENCH_EPP_METRICS_SECRET
# Verify live values with kubectl before committing
```

---

## Relationship to Legacy `benchmark-*` Targets

The `benchmark-*` targets use the full `llmdbenchmark` CLI workflow
(clone → install → standup → run → teardown). They are left completely untouched.

Use `bench-*` when:
- The stack is already running and you do not want to tear it down
- You need only `kubectl` + `bash` (no Python install, no helmfile)
- You want to iterate scenarios quickly against the same live stack

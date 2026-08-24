# Harness Internals — llm-d-benchmark v0.7.8

Research findings for sub-task 1. Source: bare clone of
`https://github.com/llm-d/llm-d-benchmark.git` at tag `v0.7.8`.

---

## 1. What is baked into the harness image vs. ConfigMap-only

From `build/Dockerfile`:

```
ADD workload/harnesses/          /usr/local/bin/
ADD llmdbenchmark/analysis/scripts/  /tmp/analysis/   → rsync'd to /usr/local/bin/
ADD llmdbenchmark/standup/preprocess/ /tmp/preprocess/ → rsync'd to /usr/local/bin/
COPY build/llm-d-benchmark.sh    /usr/local/bin/llm-d-benchmark.sh
```

**Every file under `workload/harnesses/` is baked directly into the image at `/usr/local/bin/`.**
This includes:
- `collect_metrics.sh` — **baked in at `/usr/local/bin/collect_metrics.sh`**
- `guidellm-llm-d-benchmark.sh`
- `inference-perf-llm-d-benchmark.sh`
- `aiperf-llm-d-benchmark.sh`, `lm-eval-llm-d-benchmark.sh`, etc.
- `process_metrics.py`, `process_epp_logs.py`, `fma_functions.py`

The `llmdbench-harness-scripts` ConfigMap (mounted at `/workspace/profiles/<harness>/`)
carries only the **workload profile YAML files**, not the scripts. The harness scripts are
already in the image.

**Implication for `bench-*`:** we do NOT need to build a harness-scripts ConfigMap to get
`collect_metrics.sh` into the pod — it is already there. We only need a ConfigMap for:
1. Our own additions/patches (e.g., `collect_wva_metrics.sh`)
2. Workload profile YAML files (one per scenario)

`patch_harness.sh`'s fixes (EPP float ts, guidellm report conversion, FMA affinity, GPU
memory fraction) all operate on files in the clone that are fed into the ConfigMap via the
full `llmdbenchmark` path. In the `bench-*` path, since scripts are baked into the image,
patches that need to reach the pod must be delivered differently:
- Fix 1 (EPP float ts) — `process_epp_logs.py` is baked in; **cannot be patched via
  ConfigMap**. Must either patch on startup via `kubectl exec` or build a patched image.
- Fix 2 (guidellm report conversion) — `guidellm-analyze_results.sh` is baked in; same issue.
- Fix 3 (FMA warmAffinity) — a Jinja2 template in the clone, only used at standup; not
  relevant for `bench-run`.
- Fix 4 (GPU memory fraction) — `defaults.yaml` in the clone, used at standup helm render;
  not relevant for `bench-run`.
- Fix 5 (duplicate PodMonitor) — template in the clone; not relevant for `bench-run`.

**Action required:** for fixes 1 and 2, the `bench-*` path needs a startup patch applied via
`kubectl exec` after the pod is running (overwrite the baked-in files). Add to `run_session.sh`
as a `patch_pod` step after the pod reaches Ready.

---

## 2. Scrape targets, auth requirements, and RBAC

There are three metrics scrape targets in a benchmark run. Each has different auth
requirements from inside the harness pod.

### vLLM `/metrics`
- Port 8200 (model services via WVA deploy) or 8000 (standalone).
- **No auth required.** Plain HTTP, direct pod IP. `collect_metrics.sh` curls pod IPs
  with no token. No secret or RBAC addition needed.

### EPP `/metrics`
- Port 9090 (default `LLMDBENCH_EPP_METRICS_PORT`).
- **Bearer token required.** The inferencepool chart secures this endpoint by default.
- WVA itself mounts the token at `/var/run/secrets/epp-metrics/token` in its own pod
  (`config/base/manager/deployment.yaml` — `secretName: epp-metrics-token`).
- `collect_metrics.sh` reads it differently — via `kubectl get secret` on the Secret
  named by `LLMDBENCH_EPP_METRICS_SECRET` (default: `inference-gateway-sa-metrics-reader-secret`).
- **WVA's deploy creates the secret as `epp-metrics-token`** (not the upstream default name).
  Set `LLMDBENCH_EPP_METRICS_SECRET=epp-metrics-token` as a pod env var.
- Harness pod Role needs `secrets get` on `epp-metrics-token` specifically.

### WVA `/metrics`
- Port 8080 (default controller-runtime metrics bind address).
- **No auth required by default.** Auth is controlled by `cfg.SecureMetrics()` (`cmd/main.go`).
  The default deployment does not set `--secure-metrics=true`, so the endpoint is plain HTTP.
- The harness pod can curl `<wva-controller-manager-metrics-service>.<namespace>.svc.cluster.local:8080/metrics`
  directly. No token, no extra RBAC.

### Prometheus / Thanos for post-run range query
- **OpenShift:** Thanos Querier at `https://thanos-querier.openshift-monitoring.svc.cluster.local:9091`.
  TLS + bearer token required. **Use the pod's own auto-mounted ServiceAccount token**
  at `/var/run/secrets/kubernetes.io/serviceaccount/token` — no extra secret needed.
  `snapshot.py` already uses this pattern (`--token-file`).
- **Vanilla k8s (kube-prometheus-stack):** `http://prometheus-operated.<ns>.svc.cluster.local:9090`.
  Plain HTTP, **no token needed** from inside the cluster.
- `scrape_prometheus_range.sh` must detect which case it is in and pass the SA token
  only for the HTTPS/OpenShift path.

### Complete harness pod Role

```yaml
rules:
- apiGroups: [""]
  resources: ["pods", "pods/log"]
  verbs: ["get", "list"]
- apiGroups: [""]
  resources: ["secrets"]
  resourceNames: ["epp-metrics-token"]
  verbs: ["get"]
```

No additional verbs needed. SA token for Thanos is auto-mounted — no Role entry required.

---

## 3. Results path inside the pod

From `run_only.sh`:
```bash
RESULTS_DIR_PREFIX=/requests
```

From `llm-d-benchmark.sh` (the image entrypoint/dispatcher):
```bash
LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR = $RESULTS_DIR_PREFIX / <harness>_<experiment_id>_<stack_name>
```

Each harness wrapper (`guidellm-llm-d-benchmark.sh`, `inference-perf-llm-d-benchmark.sh`)
uses `$LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR` as its output root and writes:
- `results.json` — load generator output
- `run_metadata.yaml` — start/stop timestamps, model, endpoint URL, harness version
- `stdout.log`, `stderr.log`
- `metrics/` subdirectory (when metrics collection is enabled)

`collect_metrics.sh` writes to `$LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR/metrics/`:
- `raw/<pod_name>_<epoch>_metrics.log` — per-pod Prometheus scrapes
- `raw/collection_debug.log`
- `processed/replica_status_timeseries.json`
- `processed/pod_startup_times.json`

**For `bench-*`:** results live at `/requests/<harness>_<experiment_id>_<stack_name>/`.
`kubectl cp` from `/requests/` to collect everything.

---

## 4. How metrics collection is triggered

`collect_metrics.sh` is NOT started automatically. It is started by the harness wrapper
script **if and only if** `LLMDBENCH_VLLM_COMMON_METRICS_SCRAPE_ENABLED=true` is set
as an env var in the pod.

From `guidellm-llm-d-benchmark.sh`:
```bash
if [[ "${LLMDBENCH_VLLM_COMMON_METRICS_SCRAPE_ENABLED:-false}" == "true" ]]; then
  /usr/local/bin/collect_metrics.sh start &
  METRICS_COLLECTOR_PID=$!
fi
# ... run guidellm ...
if [[ "${LLMDBENCH_VLLM_COMMON_METRICS_SCRAPE_ENABLED:-false}" == "true" ]]; then
  /usr/local/bin/collect_metrics.sh stop
  wait $METRICS_COLLECTOR_PID
  /usr/local/bin/collect_metrics.sh process
fi
```

Same pattern in `inference-perf-llm-d-benchmark.sh`.

**For `bench-*`:** set `LLMDBENCH_VLLM_COMMON_METRICS_SCRAPE_ENABLED=true` as a pod env
var. Metrics collection starts/stops automatically inside the harness wrapper.
We do NOT need to call `collect_metrics.sh` ourselves from outside the pod.

Additional env vars that configure collection (all from `collect_metrics.sh`):
- `LLMDBENCH_VLLM_COMMON_NAMESPACE` — namespace to scrape (must match the benchmark namespace)
- `LLMDBENCH_VLLM_COMMON_METRICS_PORT` — vLLM metrics port (default 8200)
- `LLMDBENCH_VLLM_COMMON_INFERENCE_PORT` — fallback port (default 8000)
- `LLMDBENCH_EPP_METRICS_PORT` — EPP metrics port (default 9090)
- `LLMDBENCH_EPP_METRICS_SECRET` — EPP bearer token secret name
- `METRICS_COLLECTION_INTERVAL` — scrape interval in seconds (default 15)

---

## 5. How the harness wrapper is invoked

From `llm-d-benchmark.sh` (the image ENTRYPOINT/dispatcher):
```bash
# Sets up env, then:
/usr/local/bin/${LLMDBENCH_RUN_EXPERIMENT_HARNESS}
# e.g. /usr/local/bin/guidellm-llm-d-benchmark.sh
#   or /usr/local/bin/inference-perf-llm-d-benchmark.sh
```

Key env vars the dispatcher sets up (and the harness wrappers expect):
- `LLMDBENCH_HARNESS_NAME` — `guidellm` or `inference-perf`
- `LLMDBENCH_RUN_EXPERIMENT_ID` — set by `run_only.sh` to a timestamp
- `LLMDBENCH_RUN_EXPERIMENT_HARNESS_WORKLOAD_NAME` — e.g. `prefill_heavy.yaml`
- `LLMDBENCH_RUN_WORKSPACE_DIR` — `/workspace`
- `LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR_PREFIX` — `/requests`
- `LLMDBENCH_HARNESS_STACK_NAME` — stack name (used in result dir naming and replica filter)
- `LLMDBENCH_HARNESS_STACK_ENDPOINT_URL` — endpoint URL (written into `run_metadata.yaml`)
- `LLMDBENCH_DEPLOY_CURRENT_MODEL` — model name (written into `run_metadata.yaml`)
- `LLMDBENCH_VLLM_COMMON_NAMESPACE` — namespace

Profile path: the guidellm wrapper reads the profile from:
```
/workspace/profiles/guidellm/${LLMDBENCH_RUN_EXPERIMENT_HARNESS_WORKLOAD_NAME}
```
The inference-perf wrapper reads from:
```
/workspace/profiles/inference-perf/${LLMDBENCH_RUN_EXPERIMENT_HARNESS_WORKLOAD_NAME}
```

**For `bench-*`:** invoke `llm-d-benchmark.sh` via `kubectl exec`, with all env vars set
either in the pod spec (permanent) or injected per-scenario via env override in the exec
command. The `LLMDBENCH_RUN_EXPERIMENT_ID` changes per scenario to give each run a unique
results directory.

---

## 6. GPU reservation mechanism

From commit `ffa87255` (ported, never activated):
- `hack/benchmark/gpu-reservation.yaml` — a `Deployment` of `registry.k8s.io/pause:3.9`
  holding GPU resources. Scales to 0 by default.
- `hack/benchmark/gpu-reservation-coupler.sh` — a polling loop that watches one specific
  decode Deployment's replica count and adjusts the reservation inversely to maintain a
  constant total GPU hold: `reservation = HOLD_TOTAL - (decode_replicas × GPUS_PER_DECODE_REPLICA)`.

**Mechanism:** pre-reserve GPUs before the benchmark starts. When WVA/KEDA scales decode
up, the coupler releases the reservation, freeing pre-held GPUs for the new pod rather
than racing other tenants.

**Known gaps in the current implementation:**
1. **Hardcoded to one Deployment.** Should watch all ScaledObject-managed Deployments in
   the namespace, not just one hardcoded name. Generalizing requires either watching all
   SOs or being told the full list at start time.
2. **Polling lag race.** The coupler polls every 5s. KEDA's scale-up → pod scheduling →
   GPU claim can happen faster, so the reservation may not be released before the new pod
   tries to acquire the GPU, causing it to pend briefly anyway.
3. **Interaction with WVA limits.** WVA's GPU limiter reads actual free GPU capacity. A
   reservation pod holding GPUs reduces `effectiveAvailable`, so WVA will not scale beyond
   what the reservation allows. This is actually **correct cooperative behaviour** — WVA
   and the reservation agree on the GPU budget. The coupler's job is only to ensure
   releasing the reservation matches scale-up events promptly.
4. **Idle-GPU sweep agents** (shared clusters like pokprod) will evict a pause-container
   reservation pod as idle GPU usage. Do not use this without confirming exemption.

**Decision for `bench-*`:** keep as an advanced opt-in, disabled by default.
`bench-run` never invokes it automatically. The coupler script and YAML are retained in
`hack/benchmark/` as reference tooling for controlled environments.

---

## 7. Impact on sub-tasks 2–6

| Sub-task | Implication |
|---|---|
| 2 (`run_session.sh`) | After pod Ready: `kubectl exec` patch script to overwrite `process_epp_logs.py` and `guidellm-analyze_results.sh`. Set `LLMDBENCH_EPP_METRICS_SECRET=epp-metrics-token` as pod env var (canonical name from WVA deploy). |
| 3 (`collect_wva_metrics.sh`) | Deliver via `kubectl cp` to `/usr/local/bin/` after pod startup. WVA `/metrics` at port 8080 is plain HTTP — no token needed. |
| 4 (`run_scenario.sh`) | Set `LLMDBENCH_VLLM_COMMON_METRICS_SCRAPE_ENABLED=true` in pod spec — collection is automatic inside harness wrapper. `kubectl cp` profile into `/workspace/profiles/<harness>/`. `kubectl exec llm-d-benchmark.sh --harness=<name> --workload=<name>.yaml`. |
| 5 (Prometheus range) | OpenShift: use pod's auto-mounted SA token for Thanos TLS. Vanilla k8s: plain HTTP, no token. Detect platform in `scrape_prometheus_range.sh`. |
| 6 (Makefile) | No changes needed from harness internals findings. |

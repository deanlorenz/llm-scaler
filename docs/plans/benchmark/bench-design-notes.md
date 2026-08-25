# bench-* Architecture: env file, metadata, and init/run handoff

**Status:** implementation complete — decisions captured here as the authoritative reference

---

## Decisions

| # | Decision |
|---|---|
| 1 | **Minimal env file** — identity only: kubeconfig path + context + namespace + workload list. Everything else is discovered by bench-init and written to bench-meta.json. |
| 2 | **bench-init creates bench-meta.json** — discovers the full stack state and writes it. bench-run reads and validates it. bench-run calls bench-init automatically on first use (no manual ordering required). |
| 3 | **Context is enforced as a guard** — bench-run fails immediately if the live context does not match the env file's `BENCH_KUBE_CONTEXT`. |
| 4 | **bench-init discovers EPP metrics secret name** — harness RBAC must name the exact secret; init finds it, writes into metadata. |
| 5 | **Stack setup is a separate concern** — bench-init does not deploy WVA, EPP, or model. It assumes the stack exists and describes it. |
| 6 | **BENCH_WORKLOADS is a space-separated list of workload names** — each name matches a `.yaml.in` filename under `test/benchmark/scenarios/`. The first entry is the `bench-run` default. |
| 7 | **BENCH_IMAGE_TAG is pinned in the env file** — a specific tested version, not "latest". A warning is emitted if the running harness pod image tag differs from the pinned value. |
| 8 | **BENCH_INTER_SCENARIO_HOOK is a script path** — bench-run-all calls it between scenarios with `<workload> <namespace> <session-dir>` as arguments. Two canonical examples live in `hack/benchmark/hooks/`. |
| 9 | **Model ID is discovered from ScaledObject trigger metadata** — `spec.triggers[].metadata.modelID` is the authoritative source. Deployment container args are a fallback. |
| 10 | **bench-run reads bench-meta.json; falls back to direct env vars** — backward compatible: if `bench-meta.json` doesn't exist, bench-run behaves as before (explicit vars required). Recommended path: always run bench-init first. |

---

## The env file (identity only)

One file per named benchmark setup. Small, hand-authored, committed to the repo.
Its only job: identify the target cluster and prevent running against the wrong one.

```bash
# hack/benchmark/dhl-la-1708.env
#
# Identity guard for bench-* targets on pokprod.
# Source before invoking any bench-* target, or pass as BENCH_ENV_FILE=<file>.
# bench-run will refuse if the live kubectl context != BENCH_KUBE_CONTEXT.

BENCH_KUBECONFIG=/home/dean/.kube/la-test
BENCH_KUBE_CONTEXT=dhl-la-1708/api-pokprod001-ete14-res-ibm-com:6443/DEAN@il.ibm.com
BENCH_NAMESPACE=dhl-la-1708

# Workloads for bench-run-all. Space-separated; first is the bench-run default.
# Each name must match test/benchmark/scenarios/<name>.yaml.in
BENCH_WORKLOADS="prefill_heavy symmetrical burst_4k250"

# Pinned harness image tag. bench-init warns if the cluster pod differs.
BENCH_IMAGE_TAG=v0.7.8
```

That is the complete env file. Five variables. Everything else — model IDs, endpoints,
secret names, replica counts, Prometheus URL — is discovered by bench-init.

---

## The metadata file: `hack/benchmark/bench-scratch/<ns>/bench-meta.json`

bench-init discovers and writes this file. bench-run reads it at session start,
verifies the stack is still what the metadata says, and uses it to populate workload
templates and configure the harness pod.

### Schema

```json
{
  "schema_version": "1",
  "created_at": "<ISO8601>",
  "identity": {
    "kubeconfig": "/home/dean/.kube/la-test",
    "kube_context": "dhl-la-1708/api-pokprod001...",
    "namespace": "dhl-la-1708"
  },
  "stacks": [
    {
      "name": "optimized-baseline",
      "deployment": "optimized-baseline-nvidia-gpu-vllm-decode",
      "scaledobject": "optimized-baseline-nvidia-gpu-vllm-decode-wva",
      "model_id": "Qwen/Qwen3-0.6B",
      "endpoint_url": "http://optimized-baseline-epp.dhl-la-1708.svc.cluster.local:80",
      "epp_metrics_secret": "wva-epp-metrics-token",
      "vllm_pod_label": "llm-d.ai/role=decode",
      "vllm_metrics_port": 8200,
      "epp_metrics_port": 9090,
      "min_replicas": 1,
      "max_replicas": 10,
      "so_paused": false,
      "ready_replicas": 1
    }
  ],
  "wva": {
    "controller_deployment": "wva-controller-manager",
    "metrics_service": "wva-controller-manager-metrics-service",
    "metrics_port": 8443,
    "metrics_secure": true
  },
  "hf_token_secret": "llm-d-hf-token",
  "prometheus": {
    "type": "openshift-thanos",
    "url": "https://thanos-querier.openshift-monitoring.svc.cluster.local:9091"
  }
}
```

### How bench-init builds this

bench-init runs a sequence of `kubectl` queries — one pass through the namespace — and writes
the JSON. Every field maps to a specific kubectl call:

| Field | Source |
|---|---|
| `stacks[].deployment` | `kubectl get scaledobject -o jsonpath '{.spec.scaleTargetRef.name}'` |
| `stacks[].model_id` | SO trigger `metadata.modelID` first; fallback: deploy container args grep `--model` or first positional arg |
| `stacks[].endpoint_url` | `resolve_router_endpoint.sh $NS` — produces in-cluster DNS URL |
| `stacks[].epp_metrics_secret` | `kubectl get secret -l app.kubernetes.io/name=workload-variant-autoscaler` |
| `stacks[].vllm_pod_label` | Try `llm-d.ai/role=decode` first (confirmed on dhl-la-1708); record which label matched |
| `stacks[].so_paused` | SO status condition `type=Paused status=True` OR annotation `autoscaling.keda.sh/paused-replicas` |
| `stacks[].min_replicas` | `kubectl get scaledobject -o jsonpath '{.spec.minReplicaCount}'` |
| `stacks[].max_replicas` | `kubectl get scaledobject -o jsonpath '{.spec.maxReplicaCount}'` |
| `stacks[].ready_replicas` | `kubectl get deploy <name> -o jsonpath '{.status.readyReplicas}'` |
| `wva.metrics_secure` | deploy `wva-controller-manager` args → `--metrics-secure=true` present? |
| `hf_token_secret` | `kubectl get secret -n <ns>` → first name matching `*hf*token*` or `*hf-token*` |
| `prometheus.url` | Try OpenShift Thanos, then kube-prometheus-stack, then same-namespace svc (same logic as scrape_prometheus_range.sh) |

### How bench-run uses this

bench-run reads `bench-meta.json` and:

1. **Verifies context** — `kubectl config current-context` must match `identity.kube_context`. Hard fail.
2. **Selects stack** — by default `stacks[0]`; `BENCH_STACK=<name>` overrides.
3. **Checks stack health** — `so_paused=false` and `ready_replicas >= min_replicas`. Warn if not.
4. **Populates workload templates** — substitutes `model_id`, `endpoint_url` from the chosen stack.
5. **Configures harness pod** — passes `epp_metrics_secret`, `wva.metrics_service`, prometheus URL.

---

## Order of operations: who calls who

```
make bench-init BENCH_ENV_FILE=hack/benchmark/dhl-la-1708.env
  └── bench_init.sh
        ├── guard: context == BENCH_KUBE_CONTEXT
        ├── guard: namespace exists
        ├── discover: ScaledObjects → stacks[]
        ├── discover: model IDs (SO trigger metadata → deploy args fallback)
        ├── discover: endpoint URL (resolve_router_endpoint.sh)
        ├── discover: EPP metrics secret
        ├── discover: WVA controller deployment + metrics service
        ├── discover: HF token secret
        ├── discover: Prometheus URL
        └── write: hack/benchmark/bench-scratch/<ns>/bench-meta.json

make bench-run BENCH_ENV_FILE=hack/benchmark/dhl-la-1708.env
  ├── load BENCH_ENV_FILE (identity + BENCH_WORKLOADS + BENCH_IMAGE_TAG)
  ├── read bench-meta.json (MODEL_ID, ENDPOINT_URL, EPP_SECRET, PROM_URL)
  │   └── if bench-meta.json absent: error "run bench-init first"
  ├── run_session.sh ensure <ns>   (harness pod lifecycle)
  └── run_scenario.sh <scenario> <ns> <session-dir>
        ├── render .yaml.in → substituted profile
        ├── upload profile to pod
        ├── kubectl exec: run harness
        ├── collect results (kubectl cp)
        ├── scrape_prometheus_range.sh
        └── collect_igw_logs.sh

make bench-run-all BENCH_ENV_FILE=hack/benchmark/dhl-la-1708.env
  └── for each workload in BENCH_WORKLOADS:
        bench-run <workload>
        [inter-scenario hook if BENCH_INTER_SCENARIO_HOOK set]

make bench-teardown BENCH_ENV_FILE=hack/benchmark/dhl-la-1708.env
  └── run_session.sh stop <ns>
```

**Key rule:** bench-run always reads bench-meta.json. It does NOT call bench-init automatically.
The user runs bench-init once before their session, then bench-run/bench-run-all as many times
as needed. Re-running bench-init refreshes the metadata (e.g., after replica count changes).

---

## BENCH_WORKLOADS: workload names and scenario file matching

`BENCH_WORKLOADS` in the env file is a space-separated list of workload names. Each name
must match a file at `test/benchmark/scenarios/<name>.yaml.in`.

```bash
BENCH_WORKLOADS="prefill_heavy symmetrical burst_4k250"
```

- `bench-run` uses the first name as default (`BENCH_WORKLOAD=prefill_heavy`).
- `bench-run-all` iterates all names in order.
- A name not matching any `.yaml.in` file causes bench-run-check to fail immediately with
  "scenario file not found" and the list of available names.
- There is intentionally no glob expansion: exact names only, no `*` wildcards.

Available scenarios (as of this writing, `test/benchmark/scenarios/`):
- `burst_4k1000`, `burst_4k250`, `bursty` — burst / scale-up cycling
- `decode_heavy`, `prefill_heavy` — stress-test decode or prefill path
- `symmetrical` — balanced input/output tokens
- `sharegpt_inferenceperf` — ShareGPT distribution, inference-perf harness
- `smoke_2min` — minimal smoke (2 minutes, low rate)
- `static-baseline-gptoss120b` — static baseline, large model

---

## BENCH_IMAGE_TAG: version pinning and drift detection

`BENCH_IMAGE_TAG` is pinned in the env file. It controls which harness image
`run_session.sh ensure` creates the pod with.

```bash
BENCH_IMAGE_TAG=v0.7.8
```

**Drift detection:** `bench-init` queries the running harness pod (if any) and warns if its
image tag differs from `BENCH_IMAGE_TAG`. This catches:
- A previous session left a pod with an older tag (just delete it with `bench-teardown`).
- The env file was not updated after an image upgrade.

**Latest-tag check:** bench-init checks `BENCHMARK_REPO_REF` from the Makefile default and
warns if `BENCH_IMAGE_TAG` is older, e.g.:
```
bench-init: WARNING: BENCH_IMAGE_TAG=v0.7.8; Makefile default is v0.8.0 — consider updating
```
This is advisory only; the pinned tag is always used.

---

## BENCH_INTER_SCENARIO_HOOK: between-scenario scripts

`bench-run-all` calls the hook script between every scenario:

```bash
bash "$BENCH_INTER_SCENARIO_HOOK" "$workload" "$namespace" "$session_dir"
```

The hook receives three positional arguments:
1. `$1` — workload name just completed (e.g. `prefill_heavy`)
2. `$2` — namespace (e.g. `dhl-la-1708`)
3. `$3` — session directory (e.g. `hack/benchmark/bench-scratch/dhl-la-1708-20260825-143000`)

The hook's exit code is ignored (always continues to the next scenario).

Two canonical examples in `hack/benchmark/hooks/`:

### `hooks/wait_scale_down.sh` — wait for replicas to return to minReplicas

Use this between burst scenarios to ensure the autoscaler has fully scaled back down
before the next burst starts from a clean state.

```bash
# Usage: wait_scale_down.sh <workload> <namespace> <session-dir>
# Waits up to SCALE_DOWN_TIMEOUT seconds (default 300) for the primary
# stack's decode deployment to return to minReplicas ready pods.
```

### `hooks/snapshot_replicas.sh` — record replica count at inter-scenario boundary

Use this to capture a timestamped replica count snapshot between scenarios.
Written to `<session-dir>/replica_snapshots.jsonl` — one JSON line per call.

```bash
# Usage: snapshot_replicas.sh <workload> <namespace> <session-dir>
# Appends {"workload":"<w>","timestamp":<epoch>,"replicas":<n>} to replica_snapshots.jsonl
```

---

## What bench-init does NOT do

- Deploy WVA, EPP, KEDA, llm-d model serving — use `benchmark-standup` or your own deploy flow.
- Unpause ScaledObjects or wait for replicas — that is the `--prepare` flag (deferred, not implemented).
- Scale replicas to scenario-specific starting counts — that is the runner's prewarm phase (deferred).
- Set up monitoring stack or Prometheus.
- Build or push images.

---

## Implementation: `hack/benchmark/bench_init.sh`

See the script itself for the definitive implementation. High-level flow:

```
1.  Load identity: BENCH_KUBECONFIG, BENCH_KUBE_CONTEXT, BENCH_NAMESPACE
2.  Guard: kubectl config current-context == BENCH_KUBE_CONTEXT
3.  Guard: kubectl get namespace $BENCH_NAMESPACE
4.  Discover ScaledObjects → iterate, building stacks[] JSON
5.  For each SO: deployment, model_id (trigger meta → args fallback), endpoint,
    epp_metrics_secret, pod labels, min/max replicas, so_paused, ready_replicas
6.  Discover WVA controller deployment + metrics service
7.  Discover HF token secret
8.  Discover Prometheus URL
9.  Drift check: running harness pod image tag vs BENCH_IMAGE_TAG
10. Write bench-meta.json (atomic: temp file → mv)
11. Print summary
```

Output path: `hack/benchmark/bench-scratch/<namespace>/bench-meta.json`
(created by the script; safe to re-run, overwrites the previous file)

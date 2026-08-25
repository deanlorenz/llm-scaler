e # bench-init Design Notes

## Overview

`bench_init.sh` is a **read-only stack discovery script**. It inspects an
already-deployed llm-d stack and writes `bench-meta.json` — the shared contract
that every downstream pipeline stage reads.

Pipeline position:

```
bench_init.sh → bench-meta.json → bench-runtools → bench-extract → bench-viz
```

No deploying, scaling, patching, or modifying anything on the cluster.

---

## Decisions

| # | Decision |
|---|---|
| 1 | **Minimal env file** — identity only: kubeconfig path + context + namespace + workload list. Everything else is discovered by bench-init and written to bench-meta.json. |
| 2 | **bench-init creates bench-meta.json** — discovers the full stack state and writes it. bench-run reads and validates it. |
| 3 | **Context is enforced as a guard** — bench-init fails immediately if the live context does not match the env file's `BENCH_KUBE_CONTEXT`. |
| 4 | **bench-init discovers EPP metrics secret name** — harness RBAC must name the exact secret; init finds it, writes into metadata. |
| 5 | **Stack setup is a separate concern** — bench-init does not deploy WVA, EPP, or model. It assumes the stack exists and describes it. |
| 6 | **Model ID is discovered from ScaledObject trigger metadata** — `spec.triggers[].metadata.modelID` is the authoritative source. Deployment container args are a fallback. |
| 7 | **BENCH_IMAGE_TAG drift check is advisory** — warn if running harness pod image tag differs; never fail. |

---

## The env file (identity only)

One file per named benchmark setup. Small, hand-authored, committed to the repo.
Its only job: identify the target cluster and prevent running against the wrong one.

```bash
# hack/benchmark/dhl-la-1708.env
BENCH_KUBECONFIG=/home/dean/.kube/la-test
BENCH_KUBE_CONTEXT=dhl-la-1708/api-pokprod001-ete14-res-ibm-com:6443/DEAN@il.ibm.com
BENCH_NAMESPACE=dhl-la-1708

# Workloads for bench-run-all. Space-separated; first is the bench-run default.
BENCH_WORKLOADS="prefill_heavy symmetrical burst_4k250"

# Pinned harness image tag. bench-init warns if the cluster pod differs.
BENCH_IMAGE_TAG=v0.7.8
```

Five variables. Everything else — model IDs, endpoints, secret names, replica counts,
Prometheus URL — is discovered by bench-init.

---

## The metadata file: `hack/benchmark/bench-scratch/<ns>/bench-meta.json`

bench-init discovers and writes this file. bench-run reads it at session start.

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

#### Inputs
- `$1` — namespace (also `BENCH_NAMESPACE` env)
- `$2` — optional output dir override (default: `hack/benchmark/bench-scratch/<namespace>/`)
- `BENCH_KUBECONFIG` — if set, exported as `KUBECONFIG`
- `BENCH_KUBE_CONTEXT` — if set, verified against current context
- `BENCH_IMAGE_TAG` — if set, used for harness drift check (warn only)

#### Order of operations

1. Parse args: `NS=${1:-$BENCH_NAMESPACE}`, fail fast if unset
2. If `BENCH_KUBECONFIG` set → `export KUBECONFIG=$BENCH_KUBECONFIG`
3. Guard: if `BENCH_KUBE_CONTEXT` set → `kubectl config current-context` must match — hard fail
4. Guard: `kubectl get namespace $NS` — hard fail if namespace doesn't exist
5. Discover ScaledObjects → one `stacks[]` entry per SO
6. For each SO: resolve all stack fields (see field sources below)
7. Discover WVA controller deployment + metrics service
8. Find HF token secret
9. Detect Prometheus URL
10. Drift check: running harness pod image tag vs `BENCH_IMAGE_TAG` — warn only
11. `mkdir -p $OUT_DIR`
12. Write JSON to `$OUT_DIR/bench-meta.json.tmp` → atomic `mv` to `bench-meta.json`
13. Print output path as final stdout line

#### Field sources

| Field | Source |
|-------|--------|
| `stacks[].name` | SO name with trailing `-wva` (and `-v2` etc.) stripped; e.g. `optimized-baseline-nvidia-gpu-vllm-decode-wva` → `optimized-baseline` |
| `stacks[].deployment` | SO `.spec.scaleTargetRef.name` |
| `stacks[].scaledobject` | SO `.metadata.name` |
| `stacks[].model_id` | SO trigger `.metadata.modelID` → fallback: first positional arg of Deployment container args (vLLM `serve <model>`) |
| `stacks[].endpoint_url` | `bash hack/benchmark/resolve_router_endpoint.sh <namespace>` — same URL written to all stacks (EPP is shared) |
| `stacks[].epp_metrics_secret` | `kubectl get secret -l app.kubernetes.io/name=workload-variant-autoscaler` → first match `.metadata.name` |
| `stacks[].vllm_pod_label` | First of these that returns ≥1 pod: `llm-d.ai/role=decode`, `app.kubernetes.io/component=decode`, `app=<deployment-name>` |
| `stacks[].vllm_metrics_port` | `8200` (fixed) |
| `stacks[].epp_metrics_port` | `9090` (fixed) |
| `stacks[].min_replicas` | SO `.spec.minReplicaCount` |
| `stacks[].max_replicas` | SO `.spec.maxReplicaCount` |
| `stacks[].so_paused` | `true` if SO status condition `type=Paused status=True` OR annotation `autoscaling.keda.sh/paused-replicas` present |
| `stacks[].ready_replicas` | `kubectl get deploy <name> -o jsonpath '{.status.readyReplicas}'` (0 if absent) |
| `wva.controller_deployment` | Deployment with label `app.kubernetes.io/name=workload-variant-autoscaler` → `.metadata.name`; fallback: `wva-controller-manager` |
| `wva.metrics_service` | Service with same label → `.metadata.name`; fallback: `wva-controller-manager-metrics-service` |
| `wva.metrics_port` | Parse `--metrics-bind-address=:<port>` from WVA Deployment container args; default `8443` |
| `wva.metrics_secure` | `true` if `--metrics-secure=true` in WVA Deployment container args, else `false` |
| `hf_token_secret` | First secret whose name matches `*hf*token*` or `*hf-token*`; empty string if not found |
| `prometheus.type` / `.url` | Detection order: OpenShift Thanos → kube-prometheus-stack → in-namespace → unknown (see below) |
| `created_at` | `date -u +%Y-%m-%dT%H:%M:%SZ` |
| `identity.kubeconfig` | `$BENCH_KUBECONFIG` (empty string if unset) |
| `identity.kube_context` | `kubectl config current-context` |
| `identity.namespace` | `$NS` |

#### `stack.name` derivation

Strip the `-wva` suffix (and any trailing `-v2`, `-v3`, etc. variant suffix before it):

```
optimized-baseline-nvidia-gpu-vllm-decode-wva      → optimized-baseline-nvidia-gpu-vllm-decode
optimized-baseline-nvidia-gpu-vllm-decode-wva-v2   → optimized-baseline-nvidia-gpu-vllm-decode-v2
```

Used by bench-run for `BENCH_STACK=<name>` selection.

#### Prometheus detection order

1. **openshift-thanos**: `kubectl get svc thanos-querier -n openshift-monitoring` exists →
   `type: "openshift-thanos"`, `url: "https://thanos-querier.openshift-monitoring.svc.cluster.local:9091"`
2. **kube-prometheus-stack**: svc with label `app.kubernetes.io/name=prometheus` in
   `monitoring` or `prometheus` namespace →
   `type: "kube-prometheus-stack"`, `url: "http://<svc>.<ns>.svc.cluster.local:9090"`
3. **in-namespace**: same label in target namespace →
   `type: "in-namespace"`, `url: "http://<svc>.<namespace>.svc.cluster.local:9090"`
4. **unknown**: `type: "unknown"`, `url: ""`

Same detection order as `scrape_prometheus_range.sh`.

---

## JSON assembly

Python3 inline heredoc: collect all discovered values in bash variables, pass into
a Python dict, `json.dumps(indent=2)`, write to temp file, `mv` to final path.

No `jq` dependency. Only `kubectl` + `bash` + `python3`.

---

## Dependency on bench-runtools

`resolve_router_endpoint.sh` is owned by bench-runtools and lives at
`hack/benchmark/resolve_router_endpoint.sh`. bench-init calls it as a subprocess.
Must be present at that path at merge time.

Call signature: `bash hack/benchmark/resolve_router_endpoint.sh <namespace>`
Returns: one line `http://<svc>.<namespace>.svc.cluster.local:<port>`, exits non-zero if not found.

---

## Drift check

If `BENCH_IMAGE_TAG` is set and a pod matching label `app=llmdbench-harness` (or
`app.kubernetes.io/name=llmdbench`) is Running in the namespace, extract its image tag
and **warn** (do not fail) if it differs from `BENCH_IMAGE_TAG`.

---

## Style

- Header comment block matching `run_session.sh` / `run_scenario.sh` style (see those files)
- `set -euo pipefail`
- `--help` / `-h` prints header comment and exits 0
- `bash -n` must pass

---

## Validation

```bash
bash -n hack/benchmark/bench_init.sh
bash hack/benchmark/bench_init.sh --help
```

Live cluster verification (dhl-la-1708):

| Assertion | Expected |
|-----------|----------|
| `stacks` length | 2 |
| `stacks[0].model_id` | `Qwen/Qwen3-0.6B` |
| `stacks[0].so_paused` | `false` |
| `stacks[1].so_paused` | `true` |
| `wva.metrics_secure` | `true` |
| `prometheus.type` | `openshift-thanos` |
| `hf_token_secret` | `llm-d-hf-token` |

# bench-* Architecture: env file, metadata, and init/run handoff

**Status:** design settled — decisions captured here, implementation follows

---

## Decisions (from design discussion)

| # | Decision |
|---|---|
| 1 | **Minimal env file** — identity only: kubeconfig path + context + namespace. Everything else is discovered, not configured. |
| 2 | **bench-init creates a metadata file** — discovers the full stack state and writes it. bench-run reads and validates it. |
| 3 | **Context is enforced as a guard** — bench-run fails immediately if the live context does not match the env file. |
| 4 | **bench-init discovers HF token secret name** — harness may need it for tokenization; init finds it, writes name into metadata. |
| 5 | **Stack setup is a separate concern** — bench-init does not deploy WVA, EPP, or model. It assumes the stack exists and describes it. |
| 6 | **Workloads support a list** — `BENCH_WORKLOADS` in the env file can be a space-separated list for `bench-run-all`. |
| 7 | **Replica readiness is workload-driven** — if a workload specifies `starting_replicas: 3`, the runner forces that state with a prewarm phase. bench-init ensures min ≥ 1; the runner handles scenario-specific initial state. |

---

## The env file (minimal — identity only)

One file per named benchmark setup. Small, hand-authored, committed to the repo.
Its only job: prevent running against the wrong cluster.

```bash
# hack/benchmark/dhl-la-1708.env
#
# Identity guard for bench-* targets. Source this before invoking any bench-* target.
# bench-run will refuse to proceed if the live kubectl context does not match BENCH_KUBE_CONTEXT.

# Which kubeconfig file to use for this setup.
BENCH_KUBECONFIG=/home/dean/.kube/la-test

# The exact context within that kubeconfig that this env is for.
# bench-run enforces this: if `kubectl config current-context` != BENCH_KUBE_CONTEXT, it exits.
BENCH_KUBE_CONTEXT=dhl-la-1708/api-pokprod001-ete14-res-ibm-com:6443/DEAN@il.ibm.com

# Namespace to operate in.
BENCH_NAMESPACE=dhl-la-1708

# Default workload(s). Space-separated list for bench-run-all; first entry is the bench-run default.
# Each name must match a file under test/benchmark/scenarios/<name>.yaml.in
# (or test/benchmark/scenarios/<name>/<harness>.yaml for multi-harness workloads -- TBD).
BENCH_WORKLOADS="prefill_heavy symmetrical burst_4k250"
```

That is the entire env file. Four variables. Everything else — model names, endpoints, secret
names, replica counts, pod labels — is discovered dynamically by bench-init and written to the
metadata file.

---

## The metadata file: `<session-dir>/bench-meta.json`

bench-init discovers and writes this. bench-run reads it at session start, verifies the stack
is still what the metadata says, and uses it to populate workload templates.

### Schema (what bench-init must discover and write)

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
      "epp_pod_label": "llm-d-router-gateway=optimized-baseline-epp",
      "vllm_pod_label": "llm-d.ai/role=decode",
      "vllm_metrics_port": 8200,
      "epp_metrics_port": 9090,
      "epp_metrics_secret": "wva-epp-metrics-token",
      "min_replicas": 1,
      "max_replicas": 10,
      "so_paused": false,
      "ready_replicas": 1
    },
    {
      "name": "optimized-baseline-v2",
      "deployment": "optimized-baseline-nvidia-gpu-vllm-decode-v2",
      "scaledobject": "optimized-baseline-nvidia-gpu-vllm-decode-wva-v2",
      "model_id": "Qwen/Qwen3-0.6B",
      "endpoint_url": "http://optimized-baseline-epp.dhl-la-1708.svc.cluster.local:80",
      "vllm_pod_label": "llm-d.ai/role=decode,wva.llmd.ai/variant=v2",
      "vllm_metrics_port": 8200,
      "epp_metrics_port": 9090,
      "epp_metrics_secret": "wva-epp-metrics-token",
      "min_replicas": 1,
      "max_replicas": 10,
      "so_paused": true,
      "ready_replicas": 0,
      "note": "pokprod-b93r38s1 GPU requires reset; keep paused"
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
  },
  "quirks": {
    "vllm_pod_selector_fallback": "llm-d.ai/role=decode",
    "note": "cluster does not use llm-d.ai/inferenceServing=true label"
  }
}
```

### How bench-init builds this

bench-init runs a sequence of `kubectl` queries — one pass through the namespace — and writes
the JSON. Every field maps to a specific kubectl call:

| Field | Source |
|---|---|
| `stacks[].deployment` | `kubectl get scaledobject -o jsonpath='{.spec.scaleTargetRef.name}'` |
| `stacks[].model_id` | `kubectl get deploy <name> -o jsonpath='{.spec.template.spec.containers[0].args}'` → grep `--served-model-name` or first positional arg |
| `stacks[].endpoint_url` | `resolve_router_endpoint.sh` or `wait_serving.sh` detection |
| `stacks[].epp_metrics_secret` | `kubectl get secret -l app.kubernetes.io/name=workload-variant-autoscaler` → find token secret |
| `stacks[].vllm_pod_label` | Try `llm-d.ai/inferenceServing=true` first; fall back to `llm-d.ai/role=decode` |
| `stacks[].so_paused` | `kubectl get scaledobject -o jsonpath='{.metadata.annotations.autoscaling\.keda\.sh/paused-replicas}'` |
| `wva.metrics_secure` | `kubectl get deploy wva-controller-manager -o jsonpath` → look for `--metrics-secure=true` in args |
| `hf_token_secret` | `kubectl get secret -n <ns>` → grep for names containing `hf-token` or matching known patterns |
| `prometheus` | Try Thanos (OpenShift), then kube-prometheus-stack, then same-namespace svc |

### How bench-run uses this

bench-run reads `bench-meta.json` and:
1. **Verifies context** — `kubectl config current-context` must match `identity.kube_context`. Hard fail.
2. **Verifies stack is alive** — for each stack in `stacks[]`: deployment exists, SO not in error.
   Configurable: `--verify-quick` (just existence) vs `--verify-full` (wait for ready replicas).
3. **Populates workload templates** — substitutes `model_id`, `endpoint_url` from the chosen
   stack's metadata into the `.yaml.in` template. Caller picks which stack to target.
4. **Passes all config to the harness pod** — via env vars in pod spec or exec wrapper, no
   `--env` flag (the flag is not available in older kubectl versions).

---

## What bench-init must do (contract for bench-run)

bench-init is a single script/target that takes a sourced env file and produces a `bench-meta.json`.
It does NOT deploy anything. It discovers what is there and optionally prepares it for running.

### Discovery phase (always runs, read-only)

1. Verify kubectl context matches `BENCH_KUBE_CONTEXT`. Fail if not logged in or wrong context.
2. Verify namespace exists and is accessible.
3. Discover all ScaledObjects in the namespace → one entry per SO in `stacks[]`.
4. For each ScaledObject: resolve deployment, model, labels, ports, secrets, replica counts.
5. Discover WVA controller deployment and metrics service.
6. Discover HF token secret name.
7. Detect Prometheus/Thanos endpoint.
8. Detect pod label selector that finds vLLM pods (try known labels, record what works).
9. Write `bench-meta.json`.

### Preparation phase (optional, `--prepare` flag)

10. Unpause any ScaledObjects that are paused (remove the `paused-replicas` annotation).
11. Wait for each unpaused SO's deployment to reach `minReplicas` Ready pods.
    - Timeout configurable; default 300s per deployment.
    - If a deployment never reaches minReplicas (e.g., broken GPU node), warn with details
      and mark that stack as `available: false` in the metadata. bench-run skips unavailable stacks.
12. Verify endpoint responds to `GET /v1/models` for each available stack.
13. Update `stacks[].ready_replicas` and `stacks[].available` in the metadata.

### Not bench-init's job

- Deploying WVA, EPP, KEDA, llm-d model serving — that is the legacy `benchmark-standup` or
  the user's own deploy flow.
- Setting up monitoring stack.
- Building or pushing images.
- Scaling replicas to scenario-specific starting counts — that is the runner's prewarm phase.

---

## Workload files: schema and naming

Each workload is a `.yaml.in` file under `test/benchmark/scenarios/`. The filename is the
workload name. The harness type is declared inside the file (or by directory if we move to
`test/benchmark/scenarios/<name>/<harness>.yaml`).

Current flat layout (keep for now):
```
test/benchmark/scenarios/prefill_heavy.yaml.in       → harness: guidellm (implicit from spec shape)
test/benchmark/scenarios/sharegpt_inferenceperf.yaml.in  → harness: inference-perf
```

**Proposal:** add a `harness:` field to the metadata section of the `.yaml.in` file so the
runner can determine the harness without guessing from the filename.

```yaml
# test/benchmark/scenarios/prefill_heavy.yaml.in
metadata:
  labels:
    name: prefill-heavy
    harness: guidellm          # ← add this
    description: |
      ...
spec:
  ...
```

Each workload run saves its complete resolved config (substituted YAML, all parameters used,
metadata snapshot, start/end times) under `<session-dir>/<workload-name>/run_config.json`.

---

## Replica readiness and prewarm

**bench-init's job:** ensure the stack is at `minReplicas` Ready when it finishes.
**bench-run's job:** if a workload specifies a `starting_replicas` value above minReplicas,
force-scale to that count and wait for a prewarm phase before starting the load generator.

Workload prewarm spec (proposed addition to `.yaml.in`):
```yaml
metadata:
  labels:
    name: prefill-heavy
    harness: guidellm
run_config:
  starting_replicas: 1         # bench-run scales/waits to this before starting load
  prewarm_seconds: 60          # wait this long after replicas are ready before sending traffic
```

If `starting_replicas` is absent, bench-run starts immediately from whatever state is current.
If `starting_replicas` > current replicas, bench-run patches the deployment and waits.
If `starting_replicas` < current replicas (e.g., starting a scale-down test from 3→1),
bench-run patches down and waits for pods to terminate before starting load.

---

## What needs to change in existing code

### `run_scenario.sh` — fix kubectl exec env var passing

`kubectl exec ... --env=KEY=VALUE` is not available in older kubectl versions.
Replace with a wrapper approach: write a small shell script to the pod via `kubectl exec cat >`,
then call it. Specifically:

```bash
# Instead of: kubectl exec $POD -- --env=FOO=bar llm-d-benchmark.sh ...
# Do:
kubectl exec $POD -- bash -c '
  export LLMDBENCH_HARNESS_EXPERIMENT_ID="'"$EXPERIMENT_ID"'"
  export LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR_PREFIX=/requests
  ...
  exec llm-d-benchmark.sh --harness=guidellm --workload=prefill_heavy.yaml
'
```

Single `bash -c` with exported vars inline — no `--env` flag needed, works on all kubectl versions.

### `run_session.sh` — read EPP secret name from metadata, not env var

Currently hardcoded to `epp-metrics-token` with `BENCH_EPP_METRICS_SECRET` override.
After bench-init exists: read the secret name from `bench-meta.json` instead.

### Makefile — simplify `bench-run` recipe

After metadata file exists, the recipe becomes:
```makefile
bench-run: bench-guard
    @bash hack/benchmark/bench_init.sh --verify $(BENCH_META) $(BENCH_NAMESPACE)
    @bash hack/benchmark/run_session.sh ensure $(BENCH_NAMESPACE) $(BENCH_META)
    @bash hack/benchmark/run_scenario.sh $(WORKLOAD) $(BENCH_NAMESPACE) $(BENCH_META)
```

---

## Implementation order

1. **Fix `run_scenario.sh`** — replace `--env` flag with `bash -c 'export ...; exec ...'`.
   This is a bug fix, not a design decision. Do it now.

2. **Write `bench_init.sh`** — discovery + metadata write + optional prepare.
   This is the new work. Do it after the design is agreed.

3. **Update `run_session.sh` and `run_scenario.sh`** to read from metadata instead of
   individual env vars.

4. **Update Makefile** — simplify recipes now that metadata carries the config.

5. **Update `.env` files** — strip down to identity-only schema.

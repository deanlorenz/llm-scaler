# bench-init — Decisions & Findings Log

Append-only. Each entry: date-stamp (approximate), decision or finding, reasoning.

---

## Session 1 — initial scoping

**Finding:** bench-init is NOT a layer on top of the legacy `benchmark-standup`/`benchmark-run`
llmdbench machinery. It is a completely independent pipeline stage.

**Key constraint confirmed:** The pipeline does NOT use `llmdbench.py` and does NOT
require cloning the `llm-d-benchmark` repo.

**Finding:** The 4-stage pipeline is:
```
bench-init → bench-runtools → bench-extract → bench-viz
```

---

## Session 1 — commit 5 scope decision

**Decision:** Commit 5 (`88a9d75b` — `add_variant.py` optimized-baseline topology) is
**OUT OF SCOPE** for bench-init.

**Reasoning:** bench-init operates against an already-configured cluster. `add_variant.py`
adds a variant (cluster topology change). The confirmed mission explicitly excludes
"adding variants or changing cluster topology." The commit-mapping's label
"Variant setup = cluster prep" is misleading — variant setup precedes bench-init,
it is not part of it.

**Commits in scope: 4 (not 5):**
- `6f4cbf1d` — env_guard, env_wizard, preflight, reset_run
- `ffa87255` — GPU reservation/coupler
- `52851b63` — gitignore fix
- `ebbcdd50` — ScaledObject modelID rescan/verify

---

## Session 1 — mission re-scoped by bench-runtools contract

**Finding:** bench-runtools defines the exact contract bench-init must satisfy.
The runtools spec (passed by user) says:

> "Your job: write a bash script that discovers the state of an already-deployed
> llm-d stack and writes bench-meta.json. Purely read-only."

**Decision:** bench-init's primary deliverable is `hack/benchmark/bench_init.sh` —
a read-only stack discovery script that writes `bench-meta.json`.

The 4 cherry-pick commits (env_guard, preflight, reset_run, GPU reservation,
modelID rescan) are the *safety scaffolding* around bench-init, not bench-init
itself. They gate whether `bench_init.sh` is allowed to run at all.

---

## Session 1 — schema source of truth

**Decision:** `docs/plans/benchmark/bench-design-notes.md` in THIS worktree is the
authoritative schema spec, consumed by both bench-init and bench-runtools.

**Finding:** bench-runtools confirmed the exact `bench-meta.json` schema (A3).
Key fields consumed at runtime by runtools:
- `identity.kube_context` — context guard
- `stacks[0].model_id` → `MODEL_ID`
- `stacks[0].endpoint_url` → `BENCH_ENDPOINT_URL`
- `stacks[0].epp_metrics_secret` → `BENCH_EPP_METRICS_SECRET`
- `wva.metrics_service` → `WVA_METRICS_SERVICE`
- `prometheus.url` → `BENCHMARK_PROMETHEUS_URL`
- Stack selection: `stacks[0]` default; `BENCH_STACK=<name>` selects by `stacks[].name`

---

## Session 1 — resolve_router_endpoint.sh ownership

**Decision:** `resolve_router_endpoint.sh` is owned by **bench-runtools**.
bench-init calls it as a subprocess (`bash hack/benchmark/resolve_router_endpoint.sh <ns>`).
No duplication. Must be present at the same path at merge time.

**Call signature confirmed:** `bash hack/benchmark/resolve_router_endpoint.sh <namespace>`
Returns: `http://<svc>.<namespace>.svc.cluster.local:<port>` on stdout, one line.
Exits non-zero if no router/EPP service found.

---

## Session 1 — vllm_pod_label strategy

**Decision:** Probe candidate label selectors in order, write first that returns ≥1 pod:
1. `llm-d.ai/role=decode`
2. `app.kubernetes.io/component=decode`
3. `app=<deployment-name>`

**Reasoning:** bench-runtools confirmed this field is for downstream consumers
(bench-extract, post-processing), not consumed at run time by runtools itself.
Probing rather than hardcoding is more robust across different llm-d stack shapes.

---

## Session 1 — fixed port values

**Decision:** `vllm_metrics_port` = `8200` (fixed), `epp_metrics_port` = `9090` (fixed).

**Reasoning:** These are stable conventions in llm-d stacks. Confirmed by live
cluster facts (dhl-la-1708: EPP service port `http-metrics` = 9090).
No discovery needed — hardcode in schema, document in design notes.

---

## Session 1 — JSON assembly approach

**Decision:** Use Python3 inline heredoc to assemble and write JSON.
No `jq` dependency. Python3 is available wherever kubectl is.

---

## Session 1 — atomic write

**Decision:** Write to `bench-meta.json.tmp` then `mv` to `bench-meta.json`.
Prevents bench-runtools reading a partial file if bench-init is interrupted.

---

## Session 1 — `so_paused` detection

**Finding:** Live cluster fact — paused SO has annotation
`autoscaling.keda.sh/paused-replicas: 0` on the ScaledObject.

**Decision:** `so_paused = true` if annotation `autoscaling.keda.sh/paused-replicas`
is present on the ScaledObject (regardless of value).

---

## Session 1 — Prometheus detection order

**Decision:** Detection order for `prometheus` block:
1. OpenShift Thanos: `thanos-querier` svc in `openshift-monitoring`
2. kube-prometheus-stack: svc label `app.kubernetes.io/name=prometheus` in `monitoring`/`prometheus` ns
3. In-namespace: same label in target namespace
4. Unknown: `type: "unknown"`, `url: ""`

**Reasoning:** Matches detection order used by existing `scrape_prometheus_range.sh`
(as referenced in runtools spec). Thanos first because the target cluster
(dhl-la-1708) is OpenShift.

# Plan: benchmark-runtools — Benchmark Run Execution Layer

**Status:** planning

## Context and Mission

`benchmark-runtools` sits between `benchmark-init` (cluster/WVA setup, preflight) and
`benchmark-extract`/`benchmark-viz` (data extraction, visualisation). Its job:

1. Snapshot run identity metadata (ScaledObjects, Deployments, InferencePools → `run_metadata.json`)
2. Manage the harness pod lifecycle (create once, reuse across scenarios)
3. Drive workload execution, scenario by scenario
4. Monitor the run from inside the pod (in-pod scraping) and with a post-scenario Prometheus range query
5. Collect results to the local machine after each scenario
6. Optional inter-scenario cleanup (pod-internal state; cluster-level cleanup delegated to `benchmark-init` or a user-supplied script)
7. Repeat 3–6 for multi-scenario sessions

**What is NOT this layer's job:** parsing collected data, building visualisations, generating
reports — those belong to downstream layers.

**Boundary with `benchmark-init`:** `benchmark-init` owns cluster setup, WVA install, preflight,
ScaledObject verification, and cluster-level cleanup (GPU parking/reservation, scale-to-0
and back). `benchmark-runtools` owns everything from "cluster is ready" to "results are on disk".

---

## Design Principles

1. **No client-side Python install required.** A run on an existing stack uses only `make` +
   `kubectl` + `bash`. No `llmdbenchmark` Python CLI.
2. **Harness pod is ours to manage.** We create, patch, monitor, and teardown the harness pod.
   We do not call `llmdbenchmark run` or rely on its standup/teardown orchestration.
3. **Load generator binaries are from the harness image.** `guidellm` and `inference-perf`
   binaries are baked into `ghcr.io/llm-d/llm-d-benchmark:<ref>`. We use the default image
   and patch it (via ConfigMap-mounted scripts) when we need to add or fix behaviour.
4. **Metric scraping is in-pod, not client-side.** `collect_metrics.sh` already runs inside the
   harness pod and scrapes vLLM/EPP pod IPs directly. We extend this pattern — WVA metrics are
   just another scrape target. Client-side scraping (`scrape_wva_metrics.sh`,
   `sample_replicas.sh`) is a fallback/supplement, not the primary path.
5. **Post-run Prometheus range query is mandatory.** Belt-and-suspenders: in-pod scrapes during
   the run, Prometheus range query immediately at run end. Works even if the pod crashes
   mid-run or retention is short.
6. **Results live locally as individual run directories.** Each scenario produces a standard
   result directory via `kubectl cp`. Multi-scenario sessions are independent result dirs in a
   shared session directory, not merged.
7. **Multi-harness / parallel runs are deferred.** Design for single-harness sequential scenarios
   now; the architecture must not foreclose parallel harnesses later.
8. **`patch_harness.sh` mechanism is retained.** Upstream bugs are patched via idempotent
   Python edits to the clone's scripts before they're mounted as a ConfigMap. We extend this
   rather than replacing it.

---

## Architecture

### Run session lifecycle

```
bench-run-check    ← pre-run validation (namespace, endpoint, scenario file present)
bench-run          ← session entry point for a single scenario
  ├── snapshot_metadata  ← kubectl-query namespace → run_metadata.json (once per session)
  ├── harness_ensure     ← create harness pod + RBAC + ConfigMap if not already running
  ├── run_scenario       ← kubectl exec: drive load generator, in-pod metrics collection
  ├── collect_results    ← kubectl cp results to local machine
  └── scrape_prometheus  ← post-scenario Prometheus range query

bench-run-all      ← iterate all scenarios, reusing the same harness pod
bench-full         ← bench-run-all (no automatic teardown)
bench-teardown     ← explicit harness pod teardown (idempotent; never automatic)
```

**Target naming:** `bench-*` is the new run path. The existing `benchmark-*` targets
(`benchmark-run`, `benchmark-run-all`, `benchmark-standup`, `benchmark-teardown`, etc.)
are left completely untouched as the legacy path for environments using the full
`llmdbenchmark` standup flow.

**Harness pod lifetime:** the pod is created on first `benchmark-run` and persists until
`benchmark-run-teardown` is called explicitly. It is cheap to keep idle. Automatic teardown
never happens. The pod provides a stable access point between runs (e.g., for inspecting results
still in the pod filesystem).

**Inter-scenario cleanup (optional):** `benchmark-run-all` accepts a `BENCHMARK_INTER_SCENARIO_HOOK`
pointing to a client-side script run between scenarios. Pod-internal cleanup (clear results/metrics
dirs) is done by `run_scenario.sh` at the start of each scenario. Cluster-level cleanup
(parking GPUs, scaling to 0, cache flush) is the caller's responsibility — delegate to
`benchmark-init` targets or the user's hook script.

### Harness pod ownership

`hack/benchmark/run_session.sh` (new) manages the pod.

**Key finding from sub-task 1:** all harness scripts (`collect_metrics.sh`,
`guidellm-llm-d-benchmark.sh`, `inference-perf-llm-d-benchmark.sh`, etc.) are **baked
into the harness image at `/usr/local/bin/`** via `ADD workload/harnesses/ /usr/local/bin/`
in the Dockerfile. The `llmdbench-harness-scripts` ConfigMap in the legacy path carries
only workload profile YAMLs, not scripts.

Pod setup steps (confirmed from `run_only.sh`):

1. Set EPP metrics secret name: WVA installs its own token secret `epp-metrics-token`
   (`config/base/rbac/epp-metrics-token-secret.yaml`), bound to the `epp-metrics-reader`
   ServiceAccount with `get /metrics` on the EPP. This is the canonical name to use;
   set `LLMDBENCH_EPP_METRICS_SECRET=epp-metrics-token` in the pod env. No auto-detection
   needed for standard WVA installs. The harness Role gets `secrets get` on this one name.
2. Create namespaced ServiceAccount + Role + RoleBinding (pods/log get/list + secrets get on `epp-metrics-token`)
3. Create workload profiles ConfigMap (`<harness>-profiles`) with rendered scenario YAML
4. Create harness pod with: image, profiles ConfigMap mount at `/workspace/profiles/<harness>/`, explicit `runAsUser: 0`, all required env vars
5. Wait for pod Ready
6. Apply in-pod patches via `kubectl exec`: overwrite baked-in `process_epp_logs.py` and `guidellm-analyze_results.sh` with our fixed versions (replaces `patch_harness.sh`'s role for the `bench-*` path)
7. `kubectl cp` our additions (`collect_wva_metrics.sh`) to `/usr/local/bin/` in the pod

**Script sharing with legacy path:**
- `wait_serving.sh` — endpoint detection; reused by `run_scenario.sh`, not duplicated
- `sample_replicas.sh` — client-side fallback; still wired in `bench-run`
- `patch_harness.sh` — legacy only; not used in `bench-*` (replaced by in-pod exec patches)

**Metrics collection is automatic:** set `LLMDBENCH_VLLM_COMMON_METRICS_SCRAPE_ENABLED=true`
in the pod env and the harness wrapper starts/stops `collect_metrics.sh` automatically.
No external orchestration of collect_metrics.sh needed from `run_scenario.sh`.

### Metrics collection layers

| Layer | Scope | Mechanism | When |
|---|---|---|---|
| In-pod interval scrape | vLLM/EPP pod IPs | `collect_metrics.sh` (existing, ConfigMap-mounted) | During each scenario |
| In-pod WVA scrape | WVA controller `/metrics` | `collect_wva_metrics.sh` (new, ConfigMap-mounted) | During each scenario |
| Post-scenario Prometheus range | all metrics | `scrape_prometheus_range.sh` (client-side) | After each scenario |
| Replica sampler | Deployment replicas | `sample_replicas.sh` (client-side fallback) | During session (low-frequency) |

**In-pod WVA scraping:** WVA controller is reachable by service name within the same namespace
for namespace-scoped installs. Whether `/metrics` requires authentication must be confirmed in
sub-task 1 (WVA already sets up some scraping RBAC; EPP bearer token may differ). RBAC
additions to the harness Role will be added if needed.

### Run identity metadata

Before the first scenario, `benchmark-runtools` snapshots the namespace state:
- All ScaledObjects: `so_name`, `scaleTargetRef` → `deploy_name`, `metadata` → `model_id`
- All Deployments with `llm-d.ai/inferenceServing=true`: `deploy_name`, pod label selectors
- InferencePools: pool name → endpoint URL mapping
- WVA controller service endpoint (for in-pod scraping)

Written to `<session_dir>/run_metadata.json`. This is the canonical cross-reference for
downstream layers (`benchmark-extract`, `benchmark-viz`) to resolve `so_name ↔ deploy_name
↔ pod_prefix ↔ replica_ts_name ↔ model_id`.

### GPU reservation (from old WVA benchmark)

Sub-task 1 will locate and read the old GPU reservation/coupler code (mock deployment that
reserves GPUs at run start and releases them when scale-up events fire). Once read, decide
whether to include in the new design. Candidate location: `benchmark-init` owns
reservation/release; `benchmark-runtools` calls the release hook when a scale-up is detected.

### Scenario file format

Current: `test/benchmark/scenarios/<name>.yaml.in` — single stage, tokens substituted at render.

Extended (later): a session file describing multiple scenarios with timing and harness
assignments for multi-harness parallel runs. **Deferred — not in this plan.**

---

## Sub-Tasks

### Sub-task 1: Research — llmdbenchmark harness internals and GPU reservation

**Status:** `[ ] pending`

**Intent:** Pure research task. Read the actual `llm-d-benchmark` scripts from source before
any code is written. Findings will reshape sub-tasks 2–6 — do not proceed to those until
this sub-task is complete and the plan updated.

**Expected Outcomes:**
Documented in `docs/plans/benchmark/harness-internals.md`:
- Inventory: which scripts are baked into the harness image vs. only in the ConfigMap
- Confirmed: does `collect_metrics.sh` arrive only via ConfigMap, or is it also in the image?
- Confirmed: exact RBAC the pod needs (pods, pods/log, secrets — which ones, why)
- Confirmed: where results are written inside the pod (path, format per harness)
- Confirmed: what `llmdbenchmark run` does step by step that we must not silently drop
  (helmfile rendering, namespace setup, monitoring stack wiring, per-scenario state, etc.)
- Confirmed: how `collect_metrics.sh` discovers vLLM/EPP pod IPs (label selector used)
- Confirmed: whether WVA `/metrics` requires authentication from inside the pod
- Read: old GPU reservation/coupler code from git history; document the mechanism and decide
  whether to carry it forward

**Todo List:**
1. Clone `llm-d-benchmark` at `v0.7.8` into `/tmp/llm-d-benchmark-ref` (bare `--depth 1`, not installed)
2. Read `existing_stack/run_only.sh` in full
3. Read `existing_stack/config_template.yaml`
4. Read `workload/harnesses/collect_metrics.sh` in full
5. Read `workload/harnesses/inference-perf-llm-d-benchmark.sh`
6. Read `workload/harnesses/guidellm-llm-d-benchmark.sh`
7. List all files under `workload/harnesses/` and `llmdbenchmark/`; note which are baked into
   the image (check `Dockerfile` or image build scripts if present) vs. ConfigMap-only
8. Trace `llmdbenchmark run` end-to-end: read the Python CLI dispatcher and every step it calls
9. Find and read old GPU reservation code: `git -C <parent-repo> log --all --oneline --grep="GPU reservation"` and `git -C <parent-repo> log --all --oneline --grep="coupler"`, then read the relevant commits/files
10. Write `docs/plans/benchmark/harness-internals.md` with all findings
11. Update this plan (sub-tasks 2–6) based on findings before switching to agent implementation

**Relevant Context:**
- `hack/benchmark/patch_harness.sh` — existing patch mechanism; fixes 1–5 are already documented
- Previous session analysis in benchmark-plan worktree (`run-only-metrics-gap.md`) — treat as
  a starting point but verify every claim from source; some conclusions were flagged as suspect
- `hack/benchmark/sample_replicas.sh` — existing client-side replica sampler

---

### Sub-task 2: Design and implement `run_session.sh`

**Status:** `[ ] pending` — **blocked on sub-task 1**

**Intent:** Replace `llmdbenchmark run` as the run entrypoint. Manages the harness pod lifecycle:
creates it on first use, keeps it alive across all scenarios, never tears it down automatically.

**Expected Outcomes:**
- `hack/benchmark/run_session.sh` — harness pod lifecycle manager
- Subcommands: `ensure` (create if not running), `stop` (explicit teardown), `status`
- `ensure` is idempotent: re-running on an existing healthy pod is a no-op
- Creates: ServiceAccount, Role (RBAC determined from sub-task 1 findings), RoleBinding,
  `llmdbench-harness-scripts` ConfigMap (built from patched clone), harness pod
- Parameterised: namespace, image tag, session directory
- `patch_harness.sh` always runs before the ConfigMap is rebuilt
- `bash -n` lint passes

**Todo List:** *(refine after sub-task 1)*
1. Read sub-task 1 findings; confirm pod spec, RBAC, ConfigMap contents, and any steps
   `llmdbenchmark run` does that we must replicate
2. Write `hack/benchmark/run_session.sh` with `ensure|stop|status` subcommands
3. Pod spec: harness image, scripts ConfigMap at `/scripts/`, results staging at `/results/`
4. RBAC: namespace-scoped only; idempotent apply
5. `bash -n` lint; dry-run RBAC apply
6. Wire into Makefile: `benchmark-run-session-ensure`, `benchmark-run-teardown`

**Relevant Context:**
- Sub-task 1 findings in `docs/plans/benchmark/harness-internals.md`
- `hack/benchmark/patch_harness.sh` — must run before ConfigMap is built
- Makefile `benchmark-*` targets — follow existing `##` doc-comment conventions

---

### Sub-task 3: Design and implement in-pod monitoring scripts

**Status:** `[ ] pending` — **blocked on sub-task 1**

**Intent:** In-pod scraping of WVA controller metrics, complementing `collect_metrics.sh`
which already scrapes vLLM/EPP. Mounted via ConfigMap so no new image is needed.

**Expected Outcomes:**
- `hack/benchmark/collect_wva_metrics.sh` — scrapes WVA `/metrics` from inside the harness pod
- Interface: `start|stop|process` matching `collect_metrics.sh`'s own interface
- Output: `metrics/raw/wva-controller_<epoch>_metrics.log`
- RBAC additions (if needed) to the harness ServiceAccount Role — confirmed from sub-task 1
- Added to scripts ConfigMap construction in `run_session.sh`
- Start/stop wired around the per-scenario load generator exec in `run_scenario.sh`

**Todo List:** *(refine after sub-task 1)*
1. From sub-task 1 findings: confirm pod IP discovery method in `collect_metrics.sh`,
   whether WVA `/metrics` needs auth, what RBAC additions are needed
2. Write `hack/benchmark/collect_wva_metrics.sh` with `start|stop|process` subcommands
3. Discover WVA controller service endpoint from inside the pod (service DNS in same namespace)
4. Add to scripts ConfigMap in `run_session.sh`; wire into `run_scenario.sh`
5. `bash -n` lint

**Relevant Context:**
- `hack/benchmark/scrape_wva_metrics.sh` (commit `65a922fa`) — client-side reference for
  the curl-to-service pattern; in-pod version drops the port-forward
- `docs/plans/benchmark/harness-internals.md` (sub-task 1 output) — authoritative source

---

### Sub-task 4: Design and implement scenario execution (`run_scenario.sh`)

**Status:** `[ ] pending` — **blocked on sub-task 1**

**Intent:** Drive a single scenario inside an already-running harness pod. Handles profile
rendering (no `yq` patches needed — image and UID are in the pod spec), in-pod metrics
collection lifecycle, load generator exec, and results collection.

**Expected Outcomes:**
- `hack/benchmark/run_scenario.sh` — scenario driver
- Pod-internal cleanup at start: clear previous results/metrics dirs
- Render `.yaml.in` → substituted profile (tokens: `__REQUEST_RATE__`, `__MAX_DURATION__`,
  `REPLACE_ENV_LLMDBENCH_DEPLOY_CURRENT_MODEL`, `REPLACE_ENV_LLMDBENCH_HARNESS_STACK_ENDPOINT_URL`)
- `kubectl cp` rendered profile into pod profile directory
- Start in-pod collection: `collect_metrics.sh start`, `collect_wva_metrics.sh start`
- `kubectl exec` appropriate harness wrapper inside the pod
- Stop in-pod collection after load generator exits
- `kubectl cp` results to `<session_dir>/<scenario>/`
- Append `scenario_start_epoch`, `scenario_end_epoch` to `<session_dir>/run_metadata.json`

**Todo List:** *(refine after sub-task 1)*
1. From sub-task 1: confirm profile path in pod, results path, harness wrapper invocation
2. Write `hack/benchmark/run_scenario.sh`
3. Token substitution: `__REQUEST_RATE__`, `__MAX_DURATION__`, `REPLACE_ENV_*`
4. Endpoint URL: call `wait_serving.sh` detection — not duplicated
5. Results: `kubectl cp <pod>:<results_path> <session_dir>/<scenario>/`
6. `bash -n` lint

**Relevant Context:**
- `test/benchmark/scenarios/*.yaml.in` — scenario templates
- `hack/benchmark/wait_serving.sh` — endpoint URL detection; shared, not copied
- `docs/plans/benchmark/harness-internals.md` (sub-task 1 output) — profile paths, results paths

---

### Sub-task 5: Post-scenario Prometheus range query

**Status:** `[ ] pending`

**Intent:** Mandatory post-scenario scrape of Prometheus/Thanos immediately after each scenario.
Belt-and-suspenders alongside in-pod scraping. Must run before results are considered complete.

**Expected Outcomes:**
- `hack/benchmark/scrape_prometheus_range.sh` — inputs: `start_epoch`, `end_epoch`,
  `namespace`, `prometheus_url`; output: `<session_dir>/<scenario>/metrics/raw/prometheus_range.json`
- Auto-detects Prometheus URL if `BENCHMARK_PROMETHEUS_URL` unset (reuse existing detection)
- Degrades gracefully: warns and writes empty file if Prometheus unreachable — never fails the run
- Queries `wva_*`, `vllm_*`, `llm_d_*` label-filtered to namespace and time window
- Wired as the final step of `run_scenario.sh` after `kubectl cp`

**Todo List:**
1. Locate existing Prometheus URL detection in Makefile / deploy scripts
2. Write `hack/benchmark/scrape_prometheus_range.sh`
3. Define initial metric selector set (extensible); document shape for `benchmark-extract`
4. Wire into `run_scenario.sh` as the final step

**Relevant Context:**
- `BENCHMARK_PROMETHEUS_URL` Makefile variable — existing detection path to reuse
- `hack/benchmark/dump_wva_full_timeseries.py` — downstream consumer; document expected output
  shape in `harness-internals.md` so `benchmark-extract` knows what to read

---

### Sub-task 6: Makefile targets and integration

**Status:** `[ ] pending` — **blocked on sub-tasks 2–5**

**Intent:** Wire new scripts into `bench-*` Makefile targets. All existing `benchmark-*`
targets are left completely untouched.

**Expected Outcomes:**
- `bench-run-check` — fast read-only preflight (namespace exists, endpoint reachable,
  scenario file present)
- `bench-run` — calls `run_session.sh ensure` then `run_scenario.sh`; harness pod persists
  after the run. Accepts: `BENCHMARK_NAMESPACE`, `BENCH_WORKLOAD`, `BENCH_HARNESS`,
  `MODEL_ID`, `BENCH_SESSION_DIR`, `BENCH_IMAGE_TAG`
- `bench-run-all` — iterate all `test/benchmark/scenarios/*.yaml.in` sequentially, reusing
  the same harness pod; accepts optional `BENCH_INTER_SCENARIO_HOOK`
- `bench-full` — `bench-run-all` (no automatic teardown)
- `bench-teardown` — explicit harness pod teardown via `run_session.sh stop`; never automatic
- All existing `benchmark-*` targets untouched

**Todo List:**
1. Define new Makefile variables: `BENCH_WORKLOAD`, `BENCH_HARNESS`, `BENCH_SESSION_DIR`,
   `BENCH_IMAGE_TAG`, `BENCH_INTER_SCENARIO_HOOK`, `BENCH_HARNESS_POD_NAME`
2. Write `bench-run-check` target
3. Write `bench-run` target
4. Write `bench-run-all` target with optional hook between scenarios
5. Write `bench-full` and `bench-teardown` targets
6. Add `docs/developer-guide/bench-guide.md` describing the new flow
   (separate from existing `benchmark-guide.md`, which documents the legacy path)

**Relevant Context:**
- Makefile lines 1391–1540 — existing `benchmark-run` (reference only, not modified)
- Makefile lines 1648–1685 — existing `benchmark-run-all` (reference only, not modified)
- Follow existing `##` doc-comment conventions for all new targets

---

### Sub-task 7: Cherry-pick and validate prior work

**Status:** `[ ] pending`

**Intent:** Apply the 12 commits from `commit-mapping.md` onto this branch. Commit `49135192`
is a SPLIT — Makefile hunks only, skip `extract_real_trace.py` hunks.

**Expected Outcomes:**
- All 12 commits applied cleanly
- `49135192` Makefile-only partial applied via `git checkout -p`
- Branch builds cleanly (`go build ./...`, `make test`)

**Todo List:**
1. Cherry-pick commits 1–4: `db198b50 eaccd3bb 5eb585f8 65a922fa`
2. Cherry-pick commit 5 (SPLIT): `git cherry-pick -n 49135192` then
   `git checkout HEAD -- hack/benchmark/extract_real_trace.py` to drop that file's changes,
   commit with original message
3. Cherry-pick commits 6–12: `2c787a73 9de1fbd2 a6129e28 77ea1b03 14bf01de 5fbf9fdb 04b70602`
4. Verify `go build ./...` and `make test` pass

**Note:** Sub-task 7 can be done independently, before or after sub-tasks 1–6. It brings in the
prior session's `run_only.sh`, `render_run_only_config.sh`, etc. — which sub-tasks 2–4 will
either supersede or integrate.

**Relevant Context:**
- `docs/plans/benchmark/commit-mapping.md` (benchmark-plan worktree) — commit list and SPLIT note
- `hack/benchmark/extract_real_trace.py` — belongs to benchmark-extract, not here

---

## Open Questions / Decisions Deferred

- **`collect_metrics.sh` in image vs. ConfigMap**: confirmed by sub-task 1. If only in
  ConfigMap, patching is straightforward. If baked into image, we mount a replacement that
  shadows it.
- **WVA `/metrics` authentication**: confirmed by sub-task 1. RBAC additions to harness Role
  added in sub-task 2 if needed.
- **Prometheus endpoint auto-detection**: existing logic in deploy scripts; extraction into
  a callable form is part of sub-task 5.
- **Multi-harness / parallel runs**: deferred. Single-harness sequential design must not
  foreclose this; pod-per-harness + session-level orchestrator is the natural extension.
- **Cloud object store for results**: deferred; `llmdbenchmark` has partial support, noted.
- **`run_only.sh` from commit `04b70602`** (arrives via sub-task 7): treated as a reference
  implementation. Sub-tasks 2–4 produce a superset; the older script will be retired or kept
  as documentation once the new design is complete.
- **GPU reservation mechanism**: located and read in sub-task 1; decision on whether to carry
  forward made before sub-task 2 begins.
- **Run identity metadata schema**: the canonical identity mapping is:
  ```
  so_name          ← ScaledObject.metadata.name
  deploy_name      ← ScaledObject.spec.scaleTargetRef.name
  pod_prefix       ← deploy_name (pods named <deploy_name>-<hash>)
  replica_ts_name  ← deploy_name (harness replica tracking)
  model_id         ← ScaledObject trigger metadata.modelID
  endpoint_url     ← InferencePool service / EPP service
  pool_name        ← InferencePool.metadata.name
  ```
  Written to `run_metadata.json` by `run_scenario.sh` before the first scenario (sub-task 4).

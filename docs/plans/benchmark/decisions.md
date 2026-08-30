# Benchmark Runtools — Decisions Log

## Image pinning (2026-08-30)

**Decision:** Pin harness image by digest, use `IfNotPresent` pull policy.

**Reason:** `imagePullPolicy: Always` with a mutable tag (`v0.7.8`) caused the
pod to silently receive a different image on each recreate if the tag was
re-pushed upstream. Observed symptom: `benchmark-report KeyError: 'args'`
appeared mid-session after working runs, indicating the image changed between
pod recreations.

**Implementation:**
- `run_session.sh`: `imagePullPolicy: Always` → `IfNotPresent`
- Image reference changed from `image:tag` to `image@sha256:digest`
- Pinned digest: `sha256:6c8be427777df57fc6ef8da18ba4a7e6311a0c4ad26daee2fd306ae591fccc63`
  (ghcr.io/llm-d/llm-d-benchmark:v0.7.8, pinned 2026-08-30)
- `BENCH_IMAGE_DIGEST` env var to override; set empty to fall back to tag
- To update digest when moving to a new image version:
  ```bash
  kubectl get pod <pod> -o jsonpath='{.status.containerStatuses[0].imageID}'
  # or
  docker inspect ghcr.io/llm-d/llm-d-benchmark:<tag> --format '{{index .RepoDigests 0}}'
  ```

---

## In-pod patches — delivery mechanism (2026-08-30)

**Decision:** Deliver in-pod Python patches as standalone `.py` files via
`kubectl cp`, not as heredocs embedded in bash.

**Reason:** `kubectl exec -- python3 - <<'PYEOF'` does not forward the local
heredoc to the remote process. Both patches silently received no stdin and
exited 0 without doing anything. The bug was invisible because the exit code
was 0 and the "applied" message was never printed (output was empty, not an
error).

**Implementation:**
- Patch scripts live in `hack/benchmark/patches/`
- `_apply_patch()` in `run_session.sh`: `kubectl cp` + `kubectl exec python3`
- Each script is idempotent (checks for a MARK string before applying)
- Each script exits non-zero with a clear message if the anchor is missing
  (upstream shape changed)

**Patches:**
- `fix1_epp_float_ts.py` — `process_epp_logs.py`: handle numeric epoch
  timestamps. EPP logs carry float seconds not ISO strings; upstream `re.sub()`
  raises `TypeError` and silently drops the entire log file.
- `fix2_conversion_non_fatal.py` — `guidellm-analyze_results.sh`: zero out
  `LLMDBENCH_RUN_EXPERIMENT_CONVERT_RC` so `benchmark-report` failure (upstream
  `KeyError: 'args'`) does not cause the entire run to be marked failed.
  `results.json` is complete and valid regardless.

---

## Pre/post-run SO and deployment management (2026-08-30)

**Decision:** Before each scenario: unpause SO if paused, scale deployment to
`min_replicas`, wait for rollout ready. After each scenario: pause SO, scale
deployment to 0.

**Reason:** The cluster idle cleanup script parks deployments at 0. WVA
respects `minReplicaCount` but only after a KEDA reconcile cycle; starting load
against a 0-replica deployment produces only timeouts in the first stage. Freeing
GPUs between runs is correct behavior on a shared cluster — other workloads
should not be starved while the harness pod sits idle between scenarios.

**Source:** `min_replicas` and `scaledobject` name come from `bench-meta.json`
`stacks[0]` (written by `bench_init.sh` from `SO.spec.minReplicaCount`).

**Implementation:** `run_scenario.sh` pre-run and post-run blocks reading
`$SESSION_DIR/bench-meta.json`.

**Not implemented yet:** warmup stage marking (ignore first N stages of data).
Legacy FMA path had this; not yet in bench-workloads or extract.py. Noted for
a future session.

---

## guidellm `harness:` field stripping (2026-08-30)

**Decision:** Strip the `harness:` top-level field from the workload YAML
before uploading to the pod.

**Reason:** `harness:` is tooling metadata used by `bench_run.sh` to select
the harness binary. guidellm rejects unknown top-level keys in its scenario
YAML with `Error: Invalid value for '--harness': Extra inputs are not
permitted (at 'harness')`, causing rc=2 and no results.

**Implementation:** `sed -e '/^harness:[[:space:]]*/d'` in the token
substitution step of `run_scenario.sh`.

---

## Harness exit code handling (2026-08-30)

**Decision:** Capture harness exit code with `|| true`; always continue to
results collection and post-run SO cleanup regardless of harness rc.

**Reason:** `set -euo pipefail` caused `run_scenario.sh` to abort immediately
on non-zero harness exit, skipping both `kubectl cp` of results and the
post-run SO pause+scale-to-zero. GPUs were not freed and results were lost.

**Implementation:** `kubectl exec ... || true` followed by `HARNESS_RC=$?` in
`run_scenario.sh`.

---

## Legacy workload porting (pending)

The following legacy workloads from `test/benchmark/scenarios/` have not yet
been ported to `hack/benchmark/bench-workloads/`:
- `bursty.yaml.in`
- `sharegpt_inferenceperf.yaml.in`
- `quick_smoke.yaml.in`
- `burst_4k1000.yaml.in`
- `burst_4k250.yaml.in`

They use guidellm format with `REPLACE_ENV_*` tokens and `/workspace` storage
path. They need to be adapted to the bench-workloads format (`harness:` field,
`# auto` fields, `/requests` path) before they can run via `bench_run.sh`.

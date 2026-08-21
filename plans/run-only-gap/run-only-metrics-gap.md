# Closing `run_only.sh`'s metrics-collection gap

Status: **implemented and committed (`04b70602`, branch `worktree-run-only-gap`,
pushed to origin); live verification against `dhl-e2e-231` found and fixed one
real bug, then hit a second, unresolved one — see "Live verification" and
"Handoff / next steps" below before continuing this in a new session.**
Working doc for the task in `TASK.md` (repo root) — see
`standalone-cli-install.md`'s "Update 2026-08-20" section for the fuller
history of how this port arrived at `run_only.sh` in the first place
(disproving the standalone-pip-install premise, discovering `run_only.sh`,
and the original gap analysis: `run_only.sh`'s bare pod spec has neither the
kubeconfig nor the `collect_metrics.sh` script the full CLI's own pod spec
uses to scrape vLLM/EPP metrics) and that doc's own "Update 2026-08-21" for
the short version of everything below.

## Decision, revised: in-pod scraping via the pod's OWN ServiceAccount — not option (b), not upstream's option (a) either

First pass (below, superseded) leaned on option (b) — a client-side scraper
extending `scrape_wva_metrics.sh`'s port-forward pattern per-pod. Dean's
steer: since we're writing our **own** version of `run_only.sh` anyway (not a
verbatim import — "you can write your own version of run_only. It all stays
here"), do the scraping *inside* the harness pod, the same place
`collect_metrics.sh` already runs for the full-CLI path.

This is **not** upstream's option (a) as originally scoped, though. The
original worry about (a) was "injecting a kubeconfig into a namespace-scoped
pod" — a real discomfort, since that's a full user/admin credential sitting
in a pod. But `run_only.sh` already creates its own tightly-scoped
ServiceAccount (`llmdbench-harness-sa`) + namespaced Role/RoleBinding
(`pods`, `pods/log` get/list) and assigns it to the harness pod. Any pod with
a ServiceAccount gets an **in-cluster** token auto-mounted
(`/var/run/secrets/kubernetes.io/serviceaccount`) — `kubectl` inside the pod
already authenticates with that, no kubeconfig file, no injection, needed at
all. Confirmed the harness image already ships `kubectl`
(`build/Dockerfile:31-34`, `/usr/local/bin/kubectl`) — Dean confirmed
separately the image is generally "several benchmark git repos and their
tools + wrapper scripts," easy to run what we want inside. `collect_metrics.sh`
itself is *not* baked into the image — the full-CLI path mounts it via a
`llmdbench-harness-scripts` ConfigMap at pod-spec time (`step_07`), which
`run_only.sh` doesn't create. So the design is: keep doing that (mount our
own scripts via ConfigMap), skip the kubeconfig-injection step entirely, and
run collection through the pod's own ServiceAccount instead.

Net effect: real in-pod metrics collection, matching the full-CLI path's own
mechanism almost exactly, with a *smaller* credential footprint than
upstream's own approach (scoped SA token vs. injected kubeconfig) — not a
tradeoff at all, strictly better on the "how comfortable are we with this"
axis the original doc raised.

RBAC needs to grow beyond what `run_only.sh` grants today, though: our
collector also needs `get` on the EPP metrics secret
(`inference-gateway-sa-metrics-reader-secret` by default). All additions stay
namespace-scoped, same Role, no cluster-scoped verbs.

**Scope decision (Dean): minimum required to use `run_only.sh`, not full
`collect_metrics.sh` parity.** `collect_metrics.sh` bundles two independent
collectors — vLLM/EPP Prometheus scrape (Collector A, direct pod-IP curl) and
replica-status/pod-startup-time via `kubectl get deployments,statefulsets`/
pod conditions (Collector B, a completely different source — the k8s API,
not `/metrics` at all). `TASK.md`'s flagged gap is specifically panels 3
(running/waiting), 4 (KV% heatmap), and 5 — all fed by Collector A alone.
Collector B feeds only `postprocess.py`'s "Avg pod startup" report stat and
`replica_status_timeseries.json` — the latter already has two working
client-side fallbacks with zero in-pod component (`sample_replicas.sh` →
`wva_replica_samples.json`; the WVA controller's own gauges via
`scrape_wva_metrics.sh`, which `extract_real_trace.py` already synthesizes
replicas from when the harness file has no matching controller). It also
carries a known, already-diagnosed bug (the `LLMDBENCH_HARNESS_STACK_NAME` vs
`llm-d.ai/model` label mismatch that's the exact reason those two fallbacks
exist). **Decision: build Collector A only.** Pod-startup-time has no
substitute anywhere in this port and will read `?` for `run_only.sh`-driven
runs — same graceful degradation every other currently-missing stat already
gets, not a new failure mode, and not required to close the flagged gap. A
fuller causal timeline (WVA decision → KEDA/HPA actuation → pod
scheduling/ready → metrics-first-seen → WVA sees the live pod) came up while
discussing this and is real future value, but is out of scope here — written
up as a new gap in `observability-gaps.md` §7 instead of built now.

Lifecycle: per Dean, `run_only.sh` creates **one** harness pod and keeps it
alive across every workload in the run (only recreated once, at the top, via
`start_harness_pod`'s `delete --ignore-not-found` + `apply`) — so **the pod
itself** needs no restart/repatch between workloads, its ConfigMap mounts and
ServiceAccount are set up once and reused. Checked how the full CLI actually
drives the collector, though, to get this right rather than assume: every
harness wrapper (`workload/harnesses/*-llm-d-benchmark.sh`, e.g.
`inference-perf-llm-d-benchmark.sh:12,26,31`) starts `collect_metrics.sh
start` at the top and `stop`+`process` at the bottom of **each workload
run**, not once for the pod's lifetime — because `METRICS_DIR` derives from
that workload's own `$LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR`, which changes
per workload. So our own collector follows the same per-workload start/stop,
bracketing the existing per-workload `kubectl exec` block in `run_only.sh`'s
loop (the `run_workload=$(cat <<RUN_WORKLOAD ...)` construct) — still zero
pod restarts, just matching the finer-grained lifecycle upstream actually
uses. (Also noted: `run_only.sh` only supports `harness_parallelism=1` — one
pod. Not relevant to `quick_smoke`, worth remembering if a future scenario
needs concurrent load generators — those would need to be created one by
one, not via this script's own parallelism knob, which doesn't exist.)

### Superseded first pass, kept for the record

`collect_metrics.sh` discovers vLLM/EPP pods via `kubectl get pods -l ...`
and then curls **pod IPs directly** (`http://${pod_ip}:${port}/metrics`) —
that only works from inside the cluster network. A client-side scraper
(`scrape_wva_metrics.sh`'s pattern) would have needed one `kubectl
port-forward` per discovered pod rather than per Service — doable, no
concrete blocker found, but more moving parts client-side than doing it
where `collect_metrics.sh` already expects to run.

## A second gap, still real regardless of where scraping happens: `REPLACE_ENV_*` substitution

This one belongs to the *workload profile*, not the pod spec, and only shows
up once you compare our own `test/benchmark/scenarios/*.yaml.in` files
against how the full CLI actually resolves them.

- Our `quick_smoke.yaml.in` (and siblings) use
  `REPLACE_ENV_LLMDBENCH_DEPLOY_CURRENT_MODEL` /
  `REPLACE_ENV_LLMDBENCH_HARNESS_STACK_ENDPOINT_URL` tokens.
- Substitution for those tokens is **client-side only**, done by
  `llmdbenchmark/utilities/profile_renderer.py` before the CLI ever builds
  the ConfigMap — nothing runs inside the pod to resolve them, and
  `run_only.sh` performs no substitution at all: it takes `.workload.<key>`
  from the config file verbatim (`yq ... | explode(.)`) into the ConfigMap.
- `config_template.yaml`'s own example workload avoids the problem entirely
  by using literal YAML anchors (`&model`/`&url`) instead of `REPLACE_ENV_*`
  tokens.

So a straight copy of our `.yaml.in` files into the run-only config's
`workload:` block would ship literal, unresolved `REPLACE_ENV_...` strings as
`model_name`/`base_url`. Needs the same treatment as the
`__REQUEST_RATE__`/`__MAX_DURATION__` substitution `benchmark-run` already
does today (Makefile:1426-1431), extended to also resolve these two tokens
before the profile is written into the run-only config's `workload.<name>:`
block. Answers open question #2 from the original (superseded)
standalone-install plan: yes, our own substitution step is required,
unconditionally.

## Awareness item: one harness per config/invocation, not per workload

Checked whether workloads for different harnesses (`guidellm`, `inference-perf`)
could be combined in a single `run_only.sh` config file — **no.** `harness.name`
is a single top-level scalar, flattened once via `yq -o shell` into
`$harness_name`, used identically for every workload in the loop
(`${HARNESS_EXECUTABLE} --harness="${harness_name}" --workload="${workload}"`,
`run_only.sh:554`) and for the one ConfigMap/mount path
(`${harness_name}-profiles` → `/workspace/profiles/${harness_name}`). Mixing
harnesses in one file's `workload:` block would mount both under one
harness's path and invoke both with the same `--harness=` — wrong for
whichever doesn't match, not a degraded case. Not a new constraint this
design introduces: `benchmark-run` already takes one `BENCHMARK_HARNESS` per
invocation today. A future multi-harness session needs two separate
`benchmark-run-only` invocations (two rendered configs, two pods).

## Design

1. **`hack/benchmark/run_only.sh`** — **our own version**, based on upstream
   but not a verbatim import (revised from the original plan). Starting
   point is still the upstream script (credit/provenance comment naming the
   source path + pinned ref it was adapted from), modified to:
   - keep `llmdbench-harness-sa`'s Role but extend its rules with `secrets`
     get (EPP bearer token only) — namespace-scoped, matching the existing
     Role/RoleBinding shape. No `deployments`/`statefulsets` RBAC: that's
     Collector B, out of scope per the decision above.
   - mount a ConfigMap of our own collector script into the harness pod
     (parallel to `${harness_name}-profiles`, not replacing it) — a trimmed
     script carrying only `collect_metrics.sh`'s vLLM/EPP scrape functions
     (`get_pod_info`, `get_epp_pod_info`, `_scrape_pod`,
     `_get_epp_auth_header`, `collect_metrics_snapshot`), not
     `collect_replica_status`/`collect_pod_startup_times`.
   - after `start_harness_pod` succeeds and before the first workload runs,
     `kubectl exec -d` (or a backgrounded exec) to start the collector loop
     once; stop it once after the last workload, before results are copied
     out.
2. **Run-only config renderer** — takes one of our
   `test/benchmark/scenarios/*.yaml.in` files plus `MODEL_ID`/`NAMESPACE`/
   endpoint URL, does:
   - existing `__REQUEST_RATE__`/`__MAX_DURATION__` substitution (unchanged),
   - new `REPLACE_ENV_LLMDBENCH_DEPLOY_CURRENT_MODEL` /
     `REPLACE_ENV_LLMDBENCH_HARNESS_STACK_ENDPOINT_URL` substitution,
   - renders a full `config_template.yaml`-shaped file, `workload.<name>:`
     holding the substituted profile, `endpoint.*` from real values,
     `harness.results_pvc` **defaulting to `-o <local-dir>` output, not PVC
     mode** — `workload-pvc` exists on `dhl-la-1708` but not on
     `dhl-e2e-231`, and local output needs no cluster prerequisite at all.
   - endpoint/router service resolution reuses `wait_serving.sh`'s existing
     detection logic (`router-epp|inference-gateway|epp$` match + HTTP-port-
     by-name lookup) instead of a third hardcoded copy.
3. **Makefile targets**: `benchmark-run-only-check` (prereq validation,
   read-only) and `benchmark-run-only` (the real invocation), house `##`
   doc-comment conventions.

**All of the above is implemented** (commit `04b70602`): `hack/benchmark/run_only.sh`,
`hack/benchmark/run_only_collect_metrics.sh`, `hack/benchmark/render_run_only_config.sh`,
`hack/benchmark/resolve_router_endpoint.sh`, the two Makefile targets, and
`hack/benchmark/dhl-e2e-231.env`. Offline-validated before touching the
cluster: `bash -n` on every script, the RBAC YAML server-dry-run applied
cleanly, `render_run_only_config.sh` produced a correct, fully-substituted
config for `quick_smoke` by hand inspection, `resolve_router_endpoint.sh`
correctly resolved `http://optimized-baseline-epp.dhl-e2e-231.svc.cluster.local:80`,
and `detect_epp_metrics_secret` correctly found `wva-epp-metrics-token` (this
repo's own WVA deploy names it differently than `llm-d-benchmark`'s own
default `inference-gateway-sa-metrics-reader-secret` assumes — confirmed live,
not assumed).

## Cluster access hygiene (new, from Dean's steer)

- Worktree-local kubeconfig: `.kube/config` (gitignored, `/.kube/` entry
  added), produced via `kubectl config view --minify --flatten
  --context=<full-context-name>` against the shared `~/.kube/la-test` file so
  it carries exactly one context (`dhl-e2e-231/api-pokprod001-.../DEAN@il.ibm.com`)
  and nothing else — verified reachable (`kubectl get pods -n dhl-e2e-231`
  through it). The shared file has 20+ contexts across many people's
  namespaces on the same cluster; a worktree-local, single-context copy means
  no other concurrent session on this machine can change which cluster/
  namespace this worktree's commands hit by flipping shared current-context.
- Every new script and Makefile target defaults `KUBECONFIG` to this
  worktree-local file (override-able), rather than relying on ambient
  `$KUBECONFIG`/`~/.kube/config` state.
- All cluster commands stay namespace-scoped (`-n dhl-e2e-231` / a context
  that only has that one namespace) — never cluster-scoped, per Dean's
  standing rule for this account (cluster-admin rights on a shared cluster).

## Verification target

`dhl-e2e-231`, `quick_smoke` scenario (switched from `dhl-la-1708`, per Dean).
Pre-run state (read-only checks, 2026-08-21): HF secret `llm-d-hf-token`
present; EPP pod (`optimized-baseline-epp`) and WVA controller running; **no
`workload-pvc`** (confirms local-output-by-default is the right call, not
just a nicety); vLLM decode Deployment
(`optimized-baseline-nvidia-gpu-vllm-decode`) parked at 0/0, already serving
`Qwen/Qwen3-0.6B` (confirmed via its pod spec args) — the small-model
default, so no `MODEL_ID` override needed. Un-parked via the sanctioned
`make so-resume SO=optimized-baseline-nvidia-gpu-vllm-decode-wva
NAMESPACE=dhl-e2e-231` (never hand-patched), per explicit authorization to
run the full live verification, including this step, autonomously.

## Live verification, attempt 1: `harness_results_pvc: unbound variable`

`make benchmark-run-only-check` passed (yq present, scenario file present, HF
secret present, endpoint resolvable). `make benchmark-run-only` got through
config rendering, RBAC creation (ServiceAccount + Role scoped to `pods`/
`pods/log` get/list + `secrets` get on exactly `wva-epp-metrics-token`,
auto-detected), both ConfigMaps, and the harness pod — then crashed at
`run_only.sh:570`-ish with `harness_results_pvc: unbound variable`.

**Root cause**: `run_only.sh` — the pinned upstream `v0.7.8` script too, not
something this fork introduced — references `${harness_results_pvc}`
unconditionally in one status announce, regardless of `_storage_type`. Our
renderer omitted `harness.results_pvc` entirely for local-output mode (since
`run_only.sh`'s own storage-type branching never reads it there), which is
correct reasoning about the CONTROL FLOW but missed this one unconditional
reference outside any storage-type branch. Confirmed by grepping
`run_only.sh` for every `harness_results_pvc` reference (4 total: one gated
by PVC-mode, one gated by PVC-mode, two NOT gated).

**Fix**: `render_run_only_config.sh` now always emits `harness.results_pvc`
(a placeholder string, `"unused-local-output-mode"`, when not using PVC
storage) — matches upstream's own `config_template.yaml`, which always
populates this field as a matter of course. Comment in the renderer records
why. Re-ran; this specific crash did not recur.

## Live verification, attempt 2: `inference-perf` thread exhaustion — UNRESOLVED

RBAC, ConfigMaps, pod creation, and model verification (`HTTP/1.1 200 OK`
against `/v1/completions`) all passed again. The workload itself started
(`Stage 0 - run started`, confirmed in the pod's own log via
`kubectl exec ... logs`-equivalent capture through `run_only.sh`'s own
`tee`), then ~15s in:

```
RuntimeError: can't start new thread
  File ".../inference_perf/metrics/request_collector/multiprocess.py", line 37, in record_metric
    self.queue.put(metric)
  File ".../multiprocessing/queues.py", line 190, in _start_thread
    self._thread.start()
RuntimeError: can't start new thread
... - ERROR - A worker process died unexpectedly!
```

Diagnosed live before aborting (`kubectl exec` into the still-running pod):

```
pids.max: max
pids.current: 3876
ps -eo comm | sort | uniq -c | sort -rn:
    222 inference-perf
      4 tee
      4 bash
      ...
```

**222 separate `inference-perf` processes** is wildly disproportionate to a
single load-generator run — looks like `inference-perf`'s own multiprocess
metrics-collector is respawning worker processes after each death without
reaping the dead ones, snowballing until *something* (not this container's
own leaf cgroup, which reports `pids.max: max` — likely a higher-level
kubelet/node `podPidsLimit`, commonly defaulted around 4096 on OpenShift, not
confirmed) refuses a new thread.

**Not root-caused. Specifically NOT yet determined**:
- Whether this is caused, even partially, by our collector's own background
  processes (bounded — a `curl` per discovered vLLM/EPP pod, once per 15s
  interval, `wait`ed before the next interval starts) competing for the same
  process/thread budget as `inference-perf`'s multiprocessing, or whether
  `inference-perf` alone (with NO collector running at all) hits this same
  wall in this specific pod/image/cluster combination regardless. **No clean
  baseline was run** (a `run_only.sh` invocation with the collector
  disabled, or the full CLI's own path, on this same cluster/image) to
  isolate this.
- Whether `hack/benchmark/patch_harness.sh` — which already documents two
  other known, upstream-confirmed bugs in this exact harness image (EPP log
  timestamp parsing; a non-fatal guidellm-report conversion bug) — has a
  third, undocumented one covering this, or whether this is genuinely new.
  Was mid-check (`grep -n "thread\|multiprocess" patch_harness.sh` — no
  match, but only checked for exact keyword hits, not the full context of
  each of its 3 documented fixes) when this session was closed out; **pick
  up here first**.
- Whether this is specific to `inference-perf` (this scenario's harness) or
  would also reproduce with `guidellm` — not tried, since `quick_smoke.yaml.in`
  is inference-perf-shaped and switching harness means a different profile
  entirely.

**Aborted per standing risk tolerance** ("abort and re-park immediately on
any ambiguity rather than push through a failure"): killed the backgrounded
`make benchmark-run-only` process, deleted the harness pod, ran
`make so-park SO=optimized-baseline-nvidia-gpu-vllm-decode-wva
NAMESPACE=dhl-e2e-231`, and confirmed via `kubectl get pods -n dhl-e2e-231`
(not just ScaledObject status) that the decode pod actually terminated.
Namespace back to baseline (EPP, WVA controller, Grafana only) — same as
before this session touched it. No cleanup done on the RBAC/ConfigMap
objects (`llmdbench-harness-sa`, its Role/RoleBinding, `inference-perf-profiles`,
`run-only-collector`) — harmless, namespace-scoped, no-GPU, and
idempotently recreated by the next `run_only.sh` invocation regardless.

## Handoff / next steps (read this first in a new session)

1. **Root-cause the thread exhaustion** before trying another live run.
   Cheapest isolating experiment: re-run `benchmark-run-only` with the
   collector's `start`/`stop` calls in `run_only.sh`'s per-workload heredoc
   commented out (or gated behind an env var) — if `inference-perf` still
   dies the same way with zero collector processes running, that clears our
   own code and points squarely at `inference-perf`/the harness image/this
   cluster's pod PID limits. If it does NOT reproduce, our collector is
   implicated and needs a fix (candidates: lower `METRICS_COLLECTION_INTERVAL`'s
   concurrency, or scrape pods sequentially instead of backgrounding every
   `_scrape_pod` call with `&`).
2. Check `hack/benchmark/patch_harness.sh` fully (all three fix blocks, not
   just a keyword grep) for anything related, and check upstream
   `llm-d-benchmark` issues for `"can't start new thread"` or
   `multiprocess.py` — this may already be a known, reported bug.
3. Once a fix (or a confirmed "not us") is in hand, redo the live
   verification end to end, including the part never reached: does
   `extract_real_trace.py` read the resulting `metrics/raw/*_metrics.log`
   files cleanly (this is the actual point of the whole task — confirming
   panels 3/4/5 populate from a `run_only.sh`-driven run).
4. Only after a clean end-to-end run: update `TASK.md`'s "when done" items
   (this doc's outcome is already folded into `standalone-cli-install.md`'s
   own "Update 2026-08-21", but that entry currently says "one still open" —
   revise it once resolved) and consider whether `TASK.md` itself should be
   removed or archived.
5. Cluster is at baseline as of this handoff — no un-parking needed to pick
   this up unless resuming the live-run investigation, at which point repeat
   the un-park → verify → re-park + confirm-termination cycle this doc
   already documents.

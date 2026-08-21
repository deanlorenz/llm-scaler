# Closing `run_only.sh`'s metrics-collection gap

Status: **design revised after Dean's steer, not yet implemented**. Working
doc for the task in `TASK.md` (repo root) — see `standalone-cli-install.md`'s
"Update 2026-08-20" section for the fuller history of how this port arrived
at `run_only.sh` in the first place. This file is the incremental work log;
the final outcome gets folded back into that doc's own "Update" section when
this is done.

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

`dhl-e2e-231`, `quick_smoke` scenario. Current state there (read-only checks,
2026-08-21): HF secret `llm-d-hf-token` present; EPP pod
(`optimized-baseline-epp`) and WVA controller running; **no `workload-pvc`**
(confirms local-output-by-default above is the right call, not just a
nicety); vLLM decode Deployment (`optimized-baseline-nvidia-gpu-vllm-decode`)
currently **scaled to 0/0**, parked from a prior session — already serving
`Qwen/Qwen3-0.6B` (confirmed via its pod spec args), the same small-model
default `BENCHMARK_MODEL_ID` already uses, so no `MODEL_ID` override will be
needed once it's un-parked. A real verification run needs it un-parked first
via the sanctioned ScaledObject path (never hand-patched), and re-parked
after (with pod termination confirmed via `kubectl get pods`, not just
ScaledObject status) — both require coordinating with Dean first, per
`TASK.md`'s cluster-safety section, applying even if a step fails or is
abandoned partway.

## Status

Design above is proposed, not yet implemented or approved. Next: get
sign-off, then implement per the numbered list above, one commit per
sub-task.

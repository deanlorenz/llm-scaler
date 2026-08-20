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
(`inference-gateway-sa-metrics-reader-secret` by default) and `get`/`list` on
`deployments`/`statefulsets` (for replica-status snapshots, if we carry that
part of `collect_metrics.sh` too — TBD scope, see below). All additions stay
namespace-scoped, same Role, no cluster-scoped verbs.

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

## Design

1. **`hack/benchmark/run_only.sh`** — **our own version**, based on upstream
   but not a verbatim import (revised from the original plan). Starting
   point is still the upstream script (credit/provenance comment naming the
   source path + pinned ref it was adapted from), modified to:
   - keep `llmdbench-harness-sa`'s Role but extend its rules for the metrics
     collector's needs (secrets get for the EPP token; deployments/
     statefulsets get/list if replica-status snapshots are carried over) —
     namespace-scoped only, matching the existing Role/RoleBinding shape.
   - mount a ConfigMap of our own collector script(s) into the harness pod
     (parallel to `${harness_name}-profiles`, not replacing it) — reusing or
     adapting `collect_metrics.sh` itself where practical rather than
     reinventing its pod-discovery/scrape logic from scratch.
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

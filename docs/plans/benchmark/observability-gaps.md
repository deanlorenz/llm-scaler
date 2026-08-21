# Benchmark observability: collection audit, and gaps to track as issues

Written while porting benchmark run/collection/extraction/visualization tooling
from `llm-d-workload-variant-autoscaler`'s `benchmark`/`autoscaling-viz`
branches into this repo (see the `worktree-benchmark` branch history for the
individual pieces). This doc is the audit trail for that work's observability
half: what the new scaler actually emits, what the collection pipeline now
captures, and what's left open — split into "build later" (in scope, just not
done yet) and "someone else's code" (out of scope for this port; needs an
issue against the scaler itself).

**Explicit scope boundary**: instrumenting the scaler's own code (adding new
log lines, new metrics, changing existing ones) is out of scope for this port.
Everything here is operator-side collection/extraction tooling reading what
the scaler *already* emits. Where the existing signal set is insufficient,
that's recorded below as a gap for a future issue, not patched from here.

## 0. A pre-existing, actively-maintained tool this port must not collide with

`hack/benchmark/dump_k2_decisions.py` + `docs/benchmark-k2-decisions-example.md`
(Ofer/biran + Evgeny Shindin, 7 commits, `05da6398`..`084eedb3`, most recent
2026-08-18) landed on `feat/wva-external-scaler` after this worktree branched
off, so it was simply absent here until brought in verbatim (commit
`0c642f50`) — not authored by this port. It reads a **materially different**
signal from what this port's own extraction captures: the saturation_v2
engine's internal k1/k2 capacity-tier lines (`k2-decision`,
`replica-capacity-decision`, `replica-capacity-skipped`,
`replica-capacity-store-fallback`, `variant-capacity-source`,
`zero-replica-capacity-estimate`, `scheduler-queue-demand`) plus the final
`"Applied saturation decision via shared cache"` line — none of which
overlap with `analyzer-result`/`scaling-decision` (which *this* tool captures
and Ofer's does not). **None of the §1 audit below caught these**: they're
logged via `logger.V(logging.DEFAULT).Info(...)`, and a `logger\.Info\(`-style
grep does not match a `.V(...)`-wrapped logger — a real blind spot in the
audit method, not just a missed message name.

No file/path collision: Ofer's tool writes `metrics/processed/k2_decisions.json`
+ `metrics/reports/k2_decision_report.md`; this port's writes
`metrics/processed/wva_decision_table.{txt,json}`. Confirmed intentional
duplication, not yet fully consolidated: **update — the k1/k2 signal is now
folded into this port's own tooling** (`extract_real_trace.py`'s
`scan_saturation_v2_events`/`build_k2_decision_table`, generalized
`CTRL_LOG_LINE`, ported `assign_cycles`/`cycles_merged`/`resolve_cycles` from
`dump_k2_decisions.py` rather than relying on this port's weaker
nearest-timestamp join), exposed as `derived.k2_decision_table` and rendered
by `dump_wva_decision_table.py`. `dump_k2_decisions.py` itself is untouched
and still produces its own separate report — the duplication is intentional
and not yet retired, per the stated long-term direction.

This was originally deferred with the reasoning "last night's real run never
exercised any of these code paths, so there's no data to validate against" —
**that reasoning was wrong and got corrected directly**: absence of exercise
in one smoke test is not evidence a real, currently-used code path is
low-priority (see `feedback_dont_deprioritize_unexercised_paths.md` in
memory). The extraction was built anyway, immediately, and validated against
synthetic log lines constructed to match the exact field names read directly
from `internal/engines/analyzers/saturation_v2/analyzer.go` (a scale-up
cycle, a memory-bound cycle, and a zero-replica variant with no per-replica
data all produced correct, distinctly different rows) — a real substitute
for live validation, not a reason to skip building it. **Still open**: no
*real* controller.log has exercised these paths yet, so live validation
(confirming the parser handles whatever real log lines actually look like,
not just the synthetic approximation) remains outstanding — get a run that
hits them (likely needs a cold-start / zero-replica-variant scenario, per
`variant-capacity-source`/`zero-replica-capacity-estimate`'s own docstrings)
and re-check.

## 1. What the scaler actually emits (audit)

- **~100 structured `logger.Info/Error/Warn` call sites** across `internal/`
  and `cmd/`. Of these, exactly two carry the decision-relevant structured
  payload this port's extraction depends on: `analyzer-result` and
  `scaling-decision` (shipped via PR #1318, merged 2026-06-25). Both are
  parsed by `extract_real_trace.py`'s `read_controller_log()`.
- Several other log lines *look* decision-adjacent but are redundant with the
  two above, at a coarser or duplicate grain: `"V2 optimizer produced
  decisions"` (aggregate count only), `"Applying scaling decisions"`,
  `"Processing decision for VA"`, `"Applied saturation decision via shared
  cache"`. Not parsed — no information in them that the two structured lines
  don't already carry per-variant.
- **Scale-from-zero is a separate decision surface, not currently parsed.**
  `internal/engines/scalefromzero/engine.go` logs `"Published scale-from-zero
  activation for Target Workload"` (variant, model, inferencepool, servingSet)
  and `"Scale-from-zero decision written to cache"` (variant, namespace,
  targetReplicas, reason) — a different code path from the steadystate
  optimizer's `analyzer-result`/`scaling-decision` lines, triggered by KEDA
  activation (`internal/scaler`'s `StreamIsActive`/`IsActive`) rather than the
  periodic reconcile loop. **Gap**: not captured by anything in this port. A
  benchmark that specifically exercises 0→1 cold-start behavior (this
  scaler's actual external-scaler architecture, not just steady-state
  scaling) would currently have no decision-log signal for that transition at
  all. Build later.
- **The controller's own `/metrics` was never collected at all** before
  tonight — only vLLM/EPP pod metrics were. Fixed: `scrape_wva_metrics.sh`
  scrapes it now (authenticated HTTPS, its own port-forward + bearer token),
  parsed by `extract_real_trace.py`'s `scan_wva_metrics()`. Currently reads 8
  of the ~28 `wva_*` metrics (`wva_desired_replicas`, `wva_current_replicas`,
  `wva_desired_ratio`, `wva_saturation_utilization`, `wva_spare_capacity`,
  `wva_required_capacity`, `wva_kv_cache_tokens_used/capacity`). **Gap**: the
  rest — `wva_replica_scaling_total`, `wva_errors_total`,
  `wva_decisions_limited_total`, `wva_available_gpus`,
  `wva_variant_at_max_replicas`, `wva_unattributed_gpus`,
  `wva_analyzer_demand`/`wva_analyzer_target`, `wva_scale_from_zero_queue_fallback_active`,
  `wva_optimizer_active`, `wva_config_info`, etc. — are scraped (the raw file
  has them) but not yet parsed into the bundle. Build later, as the specific
  analysis that needs them comes up — parsing everything unconditionally
  bloats the bundle with numbers nothing reads yet.
- **A real, live-observed anomaly, not a parsing gap**: `wva_errors_total{error_type="Failed to scrape pod"}`
  read **11738** during a ~15-minute idle window on `dhl-la-1708` (single
  scrape, not a rate). Not investigated further tonight — flag for whoever
  owns collector error budgets; could be benign (scrape retries against a
  cold pod) or a real problem on this cluster.
- **A real, live-observed metric-lifecycle gap**: `wva_desired_replicas`/
  `wva_current_replicas` were still reporting `1`/`1` for the
  `optimized-baseline-nvidia-gpu-vllm-decode-wva` variant well after its
  `ScaledObject` was paused and the Deployment was actually at `0` replicas —
  the gauge is not cleared when a variant becomes inactive. Someone else's
  code (scaler instrumentation) — open an issue, don't patch from here.
- **A late, unmerged PR is evolving the log format further**: `#1506`
  ("Inject trace_id/span_id into structured logs", `internal/tracing/`, OPEN,
  companion to `#1508`'s OpenTelemetry tracing PR, also OPEN) would add
  `trace_id`/`span_id` fields to every structured log line, including
  `analyzer-result`/`scaling-decision`. `read_controller_log()`'s
  `json.loads()` + `.get()` pattern ignores unknown keys gracefully, so this
  will not break extraction when it lands — but once merged, those IDs become
  available for cross-referencing a decision against an OTel trace/span, which
  could be a real addition to the decision table. Watch for the merge; not
  actionable before then.

## 2. Is the old "in-flight log capture" design still needed?

The original plan (`benchmark-observability-plan.md`, Parts 1-2, superseded
before implementation) wanted in-flight capture of the controller log for
three reasons: (1) get the right info out of a noisy log, (2) survive kubelet
log rotation, (3) survive an end-of-run failure (a failed write or a failed
after-the-fact `kubectl logs` fetch).

**Answer: yes, still needed — and this port's current approach is more
fragile than the old design anticipated, not less.** Tonight's collection was
a single foreground `oc logs -f --since=1s > controller.log &` on my own
laptop, started before the run and killed after. It happened to work, but it
carries every risk the old design was built to avoid, with none of the
mitigations:

- **No reconnect.** If the port/connection to the API server drops mid-run
  (the exact kind of interruption this session actually hit twice tonight —
  once from a laptop sleep, once from a worktree-cwd change that orphaned a
  background process), `oc logs -f` does not resume; it silently stops
  receiving new lines. Nothing detects this — the file just stops growing,
  and the only symptom is a truncated capture discovered after the run.
- **No durability independent of the client machine.** The old repo's
  `benchmark` branch used a different pattern for this exact problem — an
  **in-cluster follower** (`gateway-log-follower.sh`/`.yaml`, originally built
  for the Envoy access log, same idea applies to the controller log): a small
  Deployment inside the target namespace that tails the pod's log via the k8s
  API and appends to a PVC, with an at-least-once watermark design so a
  restart doesn't lose or duplicate lines. That pattern survives the
  operator's laptop sleeping, losing network, or the session being killed —
  none of which this port's simple background-process approach survives.
- **(1), "get the right info," is at least handled**: `read_controller_log()`
  filters to the two structured tags it wants, so a full raw capture is fine
  to keep as an intermediate rather than needing a live grep filter.

**Recommendation (build later, not done tonight)**: port an adapted
in-cluster follower for the controller log, matching
`gateway-log-follower.sh`'s namespace-scoped, read-only, no-GPU-request
safety profile (see that file's own header for the shared-cluster rationale),
rather than hardening the client-side `oc logs -f` approach in place. A
client-side capture is fine for a short, attended smoke test; it is the wrong
tool for anything unattended or longer than a few minutes.

## 3. `analyze_wva_decisions.py` equivalent — design, not yet built

Confirmed lower priority per direction received: not useful for a
single-variant run (nothing to compare a decision *against*), but needed for
multi-variant scenarios — exactly the two-variant efficiency-aware benchmark
this repo already documents (`docs/developer-guide/two-variant-wva-benchmark.md`).
No multi-variant run exists yet to validate against, so this is a design
sketch, not an implementation:

- **Input**: the same `wva_decision_table.json` `dump_wva_decision_table.py`
  now produces, which already has `variant`, `prc` (per-replica capacity /
  cost proxy), `curr`/`tgt`/`action` per row.
- **Check**: at each timestamp with more than one variant active, the
  optimizer should prefer scaling up the variant with the best
  serving-capacity-per-unit-cost first (this repo's own V2 saturation engine
  design, per `docs/benchmark.md`'s two-variant section: "scales the most
  efficient variant first ... routes spillover to the cheaper secondary").
  The check is: whenever two variants both have `action != no-change` in the
  same cycle, or one is scaled while a cheaper/more-efficient one sits idle
  with spare capacity, flag it — that's a candidate cost-inefficiency, not a
  hard failure (thresholds, cooldowns, and GPU availability all legitimately
  override pure efficiency ordering).
- **Output shape**: matching `preflight_shared_cluster.py`'s `Report` pattern
  (PASS/WARN/FAIL rows with a one-line detail), not a hard exit-code gate —
  this is an analysis aid for a human reading a multi-variant run, not a CI
  check.

## 4. Summary: what to open issues for (scaler code, not this repo)

1. `wva_desired_replicas`/`wva_current_replicas` (and likely other per-variant
   gauges) are not cleared when a variant becomes inactive — stale last-known
   values persist indefinitely. Confirmed live.
2. No decision-log-equivalent structured line for scale-from-zero activation
   (only for the steadystate optimizer path) — makes cold-start/0→1 behavior
   unobservable through the same pipeline as steady-state scaling.
3. `wva_errors_total{error_type="Failed to scrape pod"}` was very high (11738)
   during an idle window on a real cluster — unexplained, worth a look by
   whoever owns the collector.
4. (Not a bug, a heads-up) PR #1506/#1508 will add `trace_id`/`span_id` to
   every structured log line once merged — worth revisiting the decision
   table to include them for trace cross-referencing once that lands.
5. See §5 below — a manual model change on a Deployment leaves the
   ScaledObject's `modelID` trigger stale, and WVA silently computes zero
   decisions forever with no warning. Whoever owns `deploy/lib/scaledobject.sh`
   (or the standup flow that calls it) should decide whether that script
   should re-derive `modelID` from the live Deployment instead of being
   handed a value once at creation time, and/or whether the controller should
   warn when a ScaledObject's `modelID` trigger never matches any scraped
   metric.
6. See §6 below — `waitingQueueDemand`'s per-request KV charge uses the
   request's full `I + O` (prompt + complete generation) as its "last decode
   step" planning size. Dean: quite possibly should be `I + 0.5*O` instead —
   but there is no ground truth in this port's own data to say which is
   right (no token-by-token KV occupancy trace exists, only each request's
   final `in_tok`/`out_tok`). Whoever owns `saturation_v2/analyzer.go` is in
   a better position to judge this than benchmark tooling reading its output
   after the fact.

## 5. Run finding: stale ScaledObject `modelID` after a manual model change (dhl-la-1708, 2026-08-19)

Extracting the `quick_smoke` run captured overnight
(`hack/benchmark/results/20260820-modelid-drift/`) turned up a real,
reproducible gap — not an extraction bug. Recorded here because it's a
scaler/deploy-side gap, out of scope for this port to fix (see this doc's
scope boundary at the top), and it explains why that run's decision table
came back genuinely empty rather than under-extracted.

**What happened**: the `optimized-baseline-nvidia-gpu-vllm-decode` Deployment
was hand-patched the night before (with explicit one-time permission, to work
around the OOM documented in `dhl-la-1708.env`) to serve `Qwen/Qwen3-0.6B`
instead of `Qwen/Qwen3-32B`. That patch changed the container's `vllm serve`
model argument, but touched neither:

- the Deployment's own pod-template label `llm-d.ai/model: Qwen3-32B`, nor
- the `optimized-baseline-nvidia-gpu-vllm-decode-wva` ScaledObject's
  `spec.triggers[0].metadata.modelID: Qwen/Qwen3-32B` — written by
  `deploy/lib/scaledobject.sh` (`llm-d.ai/created-by` annotation) at
  ScaledObject creation time and never revisited.

**Observed effect**: the WVA controller log captured during the run
(`/tmp/wva-decode-heavy-capture/controller.log`, 19:35–19:50 UTC) shows the
steadystate engine processing `"modelID": "Qwen/Qwen3-32B"` for the entire
window — a model no metric on this deployment reports under anymore. Result:
zero `analyzer-result`/`scaling-decision`/k1-k2 lines the whole run
(`"decisionsApplied": 0` throughout), and panel 2 of the rendered trace shows
desired replicas flat at 1 for the full run *despite* panels 1a/3/5 showing a
real load ramp (arrival rate 3→10 req/s) that visibly builds EPP queue depth
to ~15 and in-system requests to 20+ — exactly the kind of pressure a working
saturation analyzer should react to. WVA never saw it, silently.

**Why this isn't fixed here**: the Deployment belongs to llm-d standup, not
benchmark/WVA tooling — out of scope for this repo's benchmark port to patch,
full stop (a one-time, explicitly-granted exception was used for the OOM
workaround; not a standing permission). The ScaledObject trigger is likewise
not benchmark tooling's to hand-patch: it's written by the scaler's own
deploy code, driven by whatever process re-points a Deployment at a different
model — that process is what should keep `modelID` in sync, not an
after-the-fact patch from the analysis side. This doc records the finding for
whoever owns that code path (see item 5 above); this port's job stays
analysis of what's actually emitted, which correctly reported an empty
decision table rather than fabricating one.

**Status: resolved on dhl-la-1708, 2026-08-20.** Fixed through the code that
owns this config, not by hand-patching: `make scaledobjects-plan` (read-only,
re-derives `modelID` from the live container) confirmed the discovered
`Qwen/Qwen3-0.6B` still showed `apply: no` against the existing ScaledObject,
then `make scaledobjects-apply` with that entry's `apply:` set to `adopt`
repointed `optimized-baseline-nvidia-gpu-vllm-decode-wva`'s `modelID` trigger
to match — `ScaledObjects: 0 created, 1 adopted, 0 not applied`. Verified with
the new gate below: `hack/benchmark/verify_wva_scaledobjects.sh dhl-la-1708`
now reports `1 ok, 0 drift, 0 unregistered, 0 unresolved`.

Also added `hack/benchmark/verify_wva_scaledobjects.sh`, wired as a hard gate
into `benchmark-run` (and standalone as `make benchmark-verify-scaledobjects`)
so this class of drift is caught and blocks *before* a run wastes GPU time on
a WVA that cannot produce a decision, rather than being found afterward by
manual log archaeology. It reuses `deploy/lib/scaledobject.sh`'s own discovery
(`install_default_scaledobjects` in `plan` mode, `so_plan_rows`) rather than
reimplementing model detection, and never mutates anything — same read-only
boundary this doc's scope note describes above.

## 6. Run finding: WVA's `demand` signal runs well above observed concurrency, by design (decode_heavy, dhl-la-1708, 2026-08-20)

Adding a supply/demand overlay to panel 5 (`render_real_trace.py`, converting
the analyzer-result records' `demand`/`supply` fields from KV-cache-token
units into this panel's own request units via this run's own measured mean
KV-tokens-per-running-request) turned up something worth flagging on the
`decode_heavy` run: `demand` peaks at ≈3400 request-equivalents, while
`in_system L(t)` (the per-request-derived concurrency reconstruction) peaks
at only ≈1600 at roughly the same time — demand running more than 2x above
observed concurrency.

**Not a conversion bug** — checked directly: the divisor is stable across
the run (2442 whole-run average vs 2501 computed in just the 30-60s window
around the peak, a 2.4% difference), and `supply` independently validates
against `usable slot capacity` (both represent ready-replica capacity,
computed two different ways, and track each other closely on every run
checked).

**Traced to `internal/engines/analyzers/saturation_v2/analyzer.go` directly**,
`waitingQueueDemand`'s own doc comment names three explicit, deliberate
biases, all toward over-provisioning:

1. Each request's KV footprint is charged at its *last* decode step — `I + O`
   (full prompt + full generation), a peak/no-preemption planning size, not
   its footprint at any single real instant. Confirmed numerically at the
   `decode_heavy` peak (t≈221s): 1549 requests actually active, mean
   `in_tok=1000`, mean `out_tok`-so-far `=2438` of a 4000-token target — real
   footprint ≈3438/request, charged as if every one would reach the full
   1000+4000=5000.
2. This term and a second "resident" term are each their own 1-minute
   maximum (`max_over_time`), summed even though the two maxima need not
   have occurred at the same real instant.
3. A queued (not yet running) request is priced into the same total ahead of
   time.

The doc comment's own stated reasoning: "under-provisioning decode capacity
causes preemption and recompute thrash, which costs more than a spare
replica" — i.e. deliberate, not an oversight.

**Dean's follow-up question, recorded rather than answered here**: item 1's
charge uses the request's full `I + O`. Quite possibly `I + 0.5*O` (roughly
the request's *mean* footprint over its lifetime, rather than its peak)
would be a better planning size — but this port has no ground truth to
judge that against: no token-by-token KV occupancy trace exists for any
captured run, only each request's final `in_tok`/`out_tok`. Whoever owns
`saturation_v2/analyzer.go` is in a much better position to know what the
right charge is (and why `I + O` was chosen over it) than benchmark tooling
reading the analyzer's output after the fact — recorded here as a real
question for them, not a recommendation to change it.

**What this repo's benchmark tooling did**: relabelled the panel 5 legend
entry from "WVA demand (requests, approx)" to "WVA demand (peak capacity
plan, not concurrent — approx)" so the gap against `in_system`/`being served`
reads as expected/by-design rather than as the panel's lines disagreeing
with each other.

## 7. Deferred: a full scale-event causal waterfall (WVA decision → KEDA/HPA → pod → metrics → WVA sees it)

Came up while scoping the `run_only.sh` metrics-collection gap
(`run-only-metrics-gap.md`) and discussing why pod-startup-time has no
substitute anywhere in this port (see that doc's Collector A/B scope
decision). Dean's framing: for a scale-up event we should be able to build a
full timestamp waterfall —

    WVA decision  →  KEDA/HPA actuation  →  kube schedules pod  →
    pod log start  →  pod log ready  →  metrics first detected  →  WVA sees the live pod

— and most of the stages are things this port already touches, or nearly:

- **WVA decision**: already captured — `capture_wva_controller_log.sh`
  (`analyzer-result`/`scaling-decision` lines) and `scrape_wva_metrics.sh`
  (`wva_desired_replicas`/`wva_current_replicas` gauges).
- **KEDA/HPA actuation**: **not collected anywhere yet**. KEDA implements
  `ScaledObject` via a real `HorizontalPodAutoscaler` underneath —
  `kubectl get hpa -o yaml` carries `.status.lastScaleTime` plus conditions
  with `lastTransitionTime`. A namespace-scoped, client-side poll (same
  shape as `sample_replicas.sh`) would get this for free; nothing currently
  reads it.
- **kube scheduling/ready**: **partially collected, narrower than it could
  be**. `collect_metrics.sh`'s `collect_pod_startup_times` (the in-pod
  collector, not carried into `run_only.sh` — see the scope decision above)
  only extracts the `Ready` condition's `lastTransitionTime` from
  `kubectl get pods -o json`. The same API response also carries
  `PodScheduled`, `Initialized`, and `ContainersReady`, each with its own
  `lastTransitionTime` — a finer breakdown (scheduling delay vs. image
  pull/init vs. readiness-probe delay) is sitting in data already fetched,
  just not extracted.
- **pod log start/ready timestamps**: **unverified, not investigated**.
  vLLM's own stdout likely logs distinguishable startup phases (model load,
  "Started server process", "Application startup complete", Uvicorn
  binding) with timestamps independent of Kubernetes' own probe timing —
  could corroborate or add sub-phase detail vs. the `Ready` condition, which
  is the more robust source (stable K8s-level signal, not tied to a specific
  vLLM version's log wording). Not checked against a real pod's logs yet —
  the decode Deployment on the namespace used to scope this was parked
  (0/0) at the time.
- **metrics first detected**: **free once Collector A exists** — literally
  the earliest timestamp in whichever `metrics/raw/<pod>_*.log` file a given
  pod has. No new collection needed, just a derived stat nothing currently
  computes.
- **"WVA sees the live pod"**: **not investigated**. Would need to check
  whether the controller's own log emits a line when it picks up a new pod
  as ready/eligible — not checked against `internal/actuator`'s actual log
  call sites yet.

**Why this is a gap, not a task**: real value (an actual root-cause waterfall
for "why did it take N seconds to add capacity," broken into scaler-decision
vs. actuation vs. kube vs. app-readiness vs. detection latency), but larger
than any single collection task so far — spans a new client-side HPA poller,
extending an existing pod-condition extraction, and an unverified log-parsing
question. Build later, piecemeal, starting with whichever stage a real
investigation actually needs first — not upfront as a bundle nothing has
asked for yet.

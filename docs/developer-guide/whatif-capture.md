# What-if capture and Generate

`whatif capture` turns a past window in Prometheus into the inputs the
analyzers would have read, and `whatif generate` plays them through the
analyzers as a controller's cycles would have. The output is an ordinary
learner recording. The design is R2 in
[the learner proposal, 2.9](../proposals/analyzer-learner.md#29-recorder-fields-for-ttft-and-capture).
How to run it on a real window is in
[Testing: Capture a past window](testing.md#capture-a-past-window).

```
whatif capture  --prometheus URL --namespace NS (--kube-state-metrics | --role-of ROLE=SELECTOR ...) \
                --from T0 --to T1 [--step 15s] [--policy FILE] [--timeout 2m30s] [--force] -o run.capture.jsonl.gz
whatif generate -in run.capture.jsonl.gz -o run.jsonl
```

`make whatif` builds `bin/whatif`, with the commit in the build. The command
is not in the manager image.

Exit codes: 0 done; 2 a usage or input error (a bad flag, overlapping
`--role-of` selectors, a window too large without `--force`, a capture
`generate` cannot read); 1 anything else (a failed Prometheus query, a
`generate` that fails inside the analyzers).

## Talking to Prometheus

- Every HTTP request has a timeout, `--timeout` (2m30s by default, a little
  over Prometheus' own 2-minute query timeout).
- `--bearer-token-file` is sent to an https Prometheus only, and only to its
  own host: a redirect to another host does not carry it. With an http
  `--prometheus` it is refused, since it would never be sent.
- `--insecure-skip-verify` warns on stderr.
- `--from` and `--to` are truncated to the millisecond, and `--step` must be
  a whole number of milliseconds: that is what Prometheus keeps.
- Every sample must be on the step grid, `--from` plus a whole number of
  steps. A query frontend that aligns start to step (Thanos, Mimir) answers
  off it, and the capture fails, naming the cause.
- Every range query's samples stay in memory for the whole capture. A
  window of more than `whatifsource.MaxStepQueries` (1,000,000) steps times
  queries, about a day at a 15 s step, is refused unless `--force`.

**Every query is required.** One that fails stops the capture at once and
names the query; the collector's retries do not sleep through their
backoff on an answer that cannot change. A failed query would leave its
field absent at every step, which a capture cannot tell from a field the
window did not have, so the generated run would read inputs no controller
read. The usual failure is a limit such as `query.max-samples` on a
histogram over many steps: shorten the window or lengthen the step.

## The pieces

| Piece | Where | What it does |
| --- | --- | --- |
| `windowAPI` | `internal/whatifsource/window.go` | A `promv1.API` that answers each instant query at the current step. It asks each PromQL expression once, as one range query over the window (split into parts of at most 10,000 steps, `Options.RangeQueryPoints` in tests). Range queries are the only requests it sends. The first that fails cancels the capture's context with its error. |
| The collector | `internal/collector` | The controller's own `ReplicaMetricsCollector`, over a `PrometheusSource` on `windowAPI`, with every query registered as the engine registers it. Each step opens a recording cycle (`BeginRecordingCycle(true)`), so the TTFT inputs are read too. `SetClock` stamps the rows at the step, not at the wall clock. |
| `fleet` | `internal/whatifsource/fleet.go` | The cluster at one step, from kube-state-metrics. It is the collector's `PodLocator` (which variant a Pod is) and its Pod reader (readiness, Ready time, start time, revision, deletion). |
| `RoleOf` | `internal/whatifsource/roles.go` | `--role-of ROLE=SELECTOR`: a label selector, matched against the `kube_pod_labels` labels when they carry every key it names. When they do not, a one-equality selector `app=x` matches Pods named `x-...`, and the capture is flagged as inferred. Two selectors whose name prefixes nest (`app=qwen` and `app=qwen-prefill`), or a Pod two selectors match, are refused (`ErrRoleOverlap`, exit 2). |
| `Capture` | `internal/whatifsource/capture.go` | Two passes over the window. The first finds every variant and its model from the KV-cache series. The second collects every model at every step. It writes a gzipped learnrec stream: the start line, a `capture.Window` line and one `capture.Cycle` line per model per step. |
| `Generate` | `internal/engines/analyzers/learnreplay/generate.go` | The third replay mode. It plays the cycles in the engine's order and writes a recording stamped with its own build's commit. |
| `promtest` | `internal/whatifsource/promtest` | A fake Prometheus HTTP API for tests. It serves Prometheus's own JSON and records every request. |

## The capture file

Each line is a learnrec event (`learnrec.Read` reads the file):

- `start`: `learnrec.Start`, built from the analyzers a controller with the
  policy would build, with the step as its optimize interval. The slo
  analyzer is always built and judging. The throughput analyzer is built
  when the policy lists it.
- `capture.Window`: `learnreplay.CaptureWindow`. It holds the namespace, the
  window, the step, where the roles came from, the flags (caveats), and each
  variant's model, role, accelerator, GPU count and parsed engine
  parameters (`capacity.EngineParams`).
- `capture.Cycle`: `learnreplay.CaptureCycle`. It holds one model's
  `learnrec.Input` at one step (rows in variant and Pod order, the variants'
  states, the merged policy, the scheduler queue, the arrival rate, the
  router latency and the flow-control pools), and the model's parked
  variants.

## Generate's order

One step, at the step's instant, as `steadystate` runs a cycle:

1. `slo.BeginCycle`;
2. the settle tracker keeps only the models with a variant that has
   replicas, as the engine's prune does (`retainSettle`): a model parked
   whole is forgotten. A step with no such model ends here, as the engine's
   cycle with no active variant does;
3. `NoteVariantsPresent` on saturation and then slo, model by model in key
   order: first the variants with replicas, then the parked ones;
4. saturation's `EvictStaleHistory`, the store's `EvictStale`, slo's
   `EvictStale`.

Then each model, in key order:

5. the settle tracker. It observes the model's rows. A model with variants
   and no rows is held and gets nothing more, as `onNothingObserved` does;
6. for each variant, in name order, `StableAcceleratorFor` and then
   `FileScaleTarget`. A capture never forgets a variant on request;
7. `Analyze`: saturation, then slo and throughput when the policy lists
   them. The slo analyzer's router serves the captured router latency.

The generated recording's start line carries the capture's flags as
`learnrec.Start.Caveats`, so a golden made from it says what the capture
could not see. A Fidelity replay keeps them, and the export allows their
words (`internal/whatifsource` is one of `MessageWordSources`).

## Where each input comes from

The rows are the collector's, so every field comes from the query the
controller reads it from, by the same rules (presence bits, NaN, readiness,
the uptime bound). `TestCapture_EachQueryFillsItsField` checks each field
present, absent and NaN.

| Input | Query (`registration.Query...`) or series |
| --- | --- |
| `KvCacheUsage`, `QueueLength` (and their presence bits); the busiest rank's | `KvCacheUsage`, `QueueLength`, `MaxRankKvCacheUsage`, `MaxRankQueueLength` |
| `CapacityQueueLength`, `MaxRankCapacityQueueLength`, `CapacityQueueKnown` | `CapacityQueueLength`, `MaxRankCapacityQueueLength` |
| `NumGpuBlocks`, `BlockSize`, `TotalKvCapacityTokens`, `EngineRanks`, `TokensInUse` | `CacheConfigInfo` |
| Length means; `OutputTokensBuckets`, `AvgOutputTokensSquared` | `AvgInputTokens(Recent)`, `AvgOutputTokens(Recent)`, `OutputTokensBuckets` |
| `PrefixCacheHitRate` | `PrefixCacheHitRate` |
| `AvgITL` with `AvgITLKnown` and `AvgITLEmpty` (NaN), `AvgTTFT`, `AvgServiceTime` | `AvgITL`, `AvgTTFT`, `AvgServiceTime` |
| `PrefillComputedTokenRate`, `GenerationTokenRate`, `RequestRate`, `KvUsageInstant`, `KvUsageMean` | the queries of the same names |
| `RunningRequests`, `PreemptionRate`, `SpecAcceptedPerStep` | the queries of the same names |
| R1's TTFT inputs: `PromptTokensBuckets`, `TTFTBuckets`, `AvgQueueTime`, `QueueTimeBuckets`, `QueueMin`, `AvgPrefillTime`, `AvgKVTransferTime`, `TPOTBuckets` | `PromptTokensBuckets`, `TTFTBuckets`, `AvgQueueTime`, `QueueTimeBuckets`, `QueueMin`, `AvgPrefillTime`, `AvgKVTransferTime`, `TPOTBuckets` |
| `Input.SchedulerQueue`, `Input.ArrivalRate` | `SchedulerQueueSize`, `SchedulerQueueBytes`, `ModelArrivalRate` |
| `Input.RouterLatency` | `SLORouterTTFTBuckets`, `SLORouterTPOTSum`, `SLORouterTPOTCount`, `SLORouterTPOTBuckets`, `FlowControlWaitBuckets` |
| `Input.FlowControlPools` | `FlowControlPoolModels`, `FlowControlPoolSaturation`, `FlowControlOutcomes` |
| `Ready`, `ReadySince`, `StartSeconds`, `Revision`; a deleted Pod's series dropped | `kube_pod_status_phase`, `kube_pod_status_ready`, `kube_pod_status_ready_time`, `kube_pod_created`, `kube_pod_start_time`, `kube_pod_deletion_timestamp`, and the ReplicaSet in `kube_pod_owner` |
| Variant, role | `kube_pod_owner` and `kube_replicaset_owner` (the Deployment), `label_llm_d_ai_role` in `kube_pod_labels`; or `--role-of` |
| `CurrentReplicas`, `PendingReplicas`, `DesiredReplicas`; parked variants | the variant's engine Pods in kube-state-metrics (those with a KV series at some step of the window, so a router Pod a name prefix also picks is not counted); `kube_deployment_spec_replicas` |
| `GPUsPerReplica`, `AcceleratorName` | `kube_pod_container_resource_requests{resource="nvidia_com_gpu"}`; `kube_pod_info` joined to `kube_node_labels`; else `--gpus-per-replica`, `--accelerator` |
| Engine parameters | `--engine`, `--engine-args`, `--batch-cap`, parsed by `capacity.ParseEngineArgs` |

## What a capture cannot know

Each of these is either flagged in the capture or fixed by what it writes:

- **Engine parameters.** kube-state-metrics does not export them. They come
  from flags, so the capture always carries `FlagParamsFromFlags`. The
  engine image is a placeholder.
- **Rollouts.** No variant is ever filed as rolling out, and every capture
  says so (`FlagNoRollouts`).
- **Variant states.** A capture fills the replica counts, GPUs per replica,
  role and accelerator. Discovery's other fields (held and filled replicas,
  placements, Kueue) stay zero.
- **Readiness without kube-state-metrics.** Every Pod reads as Ready, with
  no Ready time (`FlagNoReadiness`). A step at which kube-state-metrics
  lists no Pod is read the same way, its Pods attributed by `--role-of`
  alone, and the capture says at how many steps (`FlagKSMGaps`).
- **LeaderWorkerSets.** kube-state-metrics has no owner for them. Use
  `--role-of`. A replica spans several Pods, but GPUs per replica are read
  from one Pod's request; a variant whose Pods a StatefulSet owns, or that
  carry the LeaderWorkerSet labels, is flagged (`FlagMultiPodGroups`).

## Tests

- `internal/whatifsource`: each query into its field (present, absent,
  NaN), the model-level inputs, kube-state-metrics (roles, readiness, GPUs,
  parking), chunked range queries, and deterministic bytes; a failed query
  failing the capture at once, overlapping `--role-of`, replicas counted
  from engine Pods only, the rollout and multi-Pod flags, the step grid,
  and the window-size cap.
- `cmd/whatif`: the flags, usage errors (exit 2), a Prometheus error
  (exit 1, no file left), the step and the window, and `generate`; the
  bearer token kept off a redirect's host and refused for http, the
  `--insecure-skip-verify` and `--batch-cap` warnings, the request timeout,
  `generate`'s exit codes, and the flags surviving capture, generate, a
  Fidelity replay and the export.
- `learnreplay` (`TestGenerate_*`): the call order on a hand-built capture,
  refusals, a Fidelity replay of the generated recording, the settle
  tracker forgetting a model parked whole, and the caveats.
- `steadystate` (`TestWhatifCapture_*`): the round trip. An in-process
  controller collects from a fake Prometheus and records. A capture of the
  same series plays through Generate. Every event is compared byte for
  byte: each Analyze's input, result and store records, and the order of
  calls in every cycle. The engine is given variant states with only the
  fields a capture fills. That is the bound the comparison holds to. The
  round trip runs again with the window split into range queries of five
  steps.

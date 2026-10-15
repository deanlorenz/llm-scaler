# Troubleshooting

From a symptom to a fix. Every command uses `$NS` for the namespace the
**controller** runs in (see [Verifying the install](operations.md#verifying-the-install)),
and `<ns>` for the model's namespace, which is the same for a namespace-scoped
install. Steps marked **(cluster admin)** need rights a namespace owner does not
have.

Most runbooks start from the controller's two per-cycle log lines
([cycle log](cycle-log.md)). Set this once:

```bash
LOGS="kubectl logs -n $NS -l app.kubernetes.io/name=workload-variant-autoscaler --tail=-1"
```

## Symptom index

| symptom | first signal | likely causes | runbook |
| --- | --- | --- | --- |
| will not scale up | `scaling-decision` shows `tgt` equal to `curr` under load | utilization below `scaleUpThreshold` by the controller's measure, at its own max, a limiter, unscraped pods, stale metrics | [will not scale up](#a-model-will-not-scale-up) |
| will not wake from zero | `WVAModelParkedWhileRequestsRefused`; `wva_model_scaling_blocked{reason="no-wake-signal"}`; `wva_model_replicas == 0` with requests queued | EPP flow control off or EPP not restarted, the direct EPP scrape failing, InferencePool API group mismatch | [will not wake](#a-model-will-not-wake-from-zero) |
| will not park at zero | `wva_model_scaling_blocked{reason=~"variant-floor\|policy-forbids-zero\|activation-retention\|engine-unsupported"}`; `WVAModelWillNotScaleToZero` | a variant floor, a policy that forbids zero, a just-woken model, a model running both vLLM and SGLang, an idle counter that does not exist yet | [will not park](#a-model-will-not-park-at-zero) |
| flapping, or a scale-up that never lands | `WVAReplicaScalingThrashing` (it counts every cycle that is not `no-change`); `tgt` alternating in `scaling-decision`, or `tgt` steady above `curr` | utilization crossing a narrow band, short stabilization windows, utilization-share swings; or actuation stuck, Pods Pending, slow replica starts | [flapping](#a-model-is-flapping); [actuation stuck](#desired-is-not-current-actuation-stuck) |
| pinned at max | `wva_variant_at_max_replicas == 1`; `"atMax":true` in `scaling-decision` | `maxReplicaCount` too low for the load | [pinned at max](#a-model-is-pinned-at-max) |
| desired is not current | `wva_desired_replicas != wva_current_replicas` for many cycles | HPA stabilization window, KEDA not reaching the scaler, Pods Pending (GPUs, ResourceQuota), slow starts | [actuation stuck](#desired-is-not-current-actuation-stuck) |
| GPUs held / quota spent | `ResourceConstrained` Event; `wva_decisions_limited_total` rising; `wva_model_scaling_blocked{reason="quota-short"}` | the quota or GPU inventory is used up, unresolved accelerators, a lowered quota (it does not evict) | [GPUs held](#gpus-held-or-quota-spent) |
| metrics stale or missing | `MetricsUnavailable` Event; `wva_metrics_freshness_status{status!="fresh"} > 0`; `WVAMetricsCollectionFailing`; `wva_unmeasured_queue > 0` | ServiceMonitor or PodMonitor not selecting the pods, Prometheus unreachable, FMA launcher pods not scraped | [metrics](#metrics-stale-or-missing) |
| controller not Ready or crash-looping | pod `0/1` or `CrashLoopBackOff`; log `refusing to start unbounded`; readiness check `gpu-policy` failing | cluster policy unreadable, an invalid `default` entry in the ConfigMap the limiters come from, a `gpu-inventory` limiter declared alone with no node read, a Prometheus setting it refuses, an image older than the manifests | [controller not Ready](#the-controller-is-not-ready-or-crash-looping) |
| cluster policy not applied | `WVAPolicyNamespaceUnverified`; `wva_policy_namespace_unverified == 1` | the controller may not read Namespaces, so it runs on its own ConfigMap | [policy namespace unverified](#cluster-policy-is-not-applied-policy-namespace-unverified) |
| ScaledObject `READY False` or `TARGETS <unknown>` | `kubectl get scaledobject,hpa -n <ns>` | `scalerAddress` names a namespace with no scaler, `modelID` missing, the controller not Ready, KEDA backing off after a reinstall | [ScaledObject](#scaledobject-ready-false-or-targets-unknown) |
| capacity priced high after a restart | TTFT worse after an upgrade or leader change; `P3-k2`/`P4-k1` in `k2-decision` where `P1-obs` was before | `LEARNED_STATE_SNAPSHOT` off, the Role missing, an engine configuration that changed or could not be read | [after a restart](#capacity-priced-high-after-a-restart) |
| misses its SLO, or is never judged | `WVAModelSLOMissed`; `WVAModelSLOUnjudged`; an empty SLO dashboard row | tier thresholds too loose for the SLO (observe only), a fleet that never holds still, too little traffic, the router not scraped or not streaming | [SLO](#a-model-misses-or-is-never-judged-against-its-slo) |
| utilization-share optimizer does nothing | `wva_utilization_share_mode`, `wva_utilization_share_actionable` | shadow mode, not planned, held, swinging | [optimizer](#the-optimizer-evaluates-but-never-moves-anything) |

Alerts that point here are listed in [Prometheus: the shipped alerts](prometheus.md#the-shipped-alerts).
Events are listed in [Monitoring: Events](monitoring.md#events).

## A model will not scale up

**Symptom**: load rises, latency rises, the replica count does not.

Read the model's last decisions:

```bash
$LOGS | grep scaling-decision | grep '"modelID": "<model>"' | tail -5
$LOGS | grep analyzer-result  | grep '"modelID": "<model>"' | tail -5
```

| what the lines show | means | go to |
| --- | --- | --- |
| `tgt` above `curr`, for several cycles | the decision is right; it is not being carried out | [actuation stuck](#desired-is-not-current-actuation-stuck) |
| `"atMax":true` | the variant is at its own `maxReplicaCount` | [pinned at max](#a-model-is-pinned-at-max) |
| `util` below `scaleUpThreshold` | by the controller's measure the model is not saturated; check the measure next | the metrics below |
| no line for the model | the controller is not deciding for it: no ScaledObject names this scaler, or its metrics are missing | [ScaledObject](#scaledobject-ready-false-or-targets-unknown), [metrics](#metrics-stale-or-missing) |

Then check the silent causes, in this order:

```promql
wva_model_scaling_blocked{model_name="<model>"}           # any reason holding it back
wva_node_access_denied == 1                               # gpu-inventory alone, nodes unreadable: nothing scales up
wva_constraint_provider_skipped == 1                      # a declared limiter is left out; the others still bound
increase(wva_decisions_limited_total[15m]) > 0            # a limiter capped it
wva_unmeasured_queue{model_name="<model>"} > 0            # requests queue for pods it cannot see
wva_metrics_freshness_status{status!="fresh"} > 0         # decided on stale or missing data
```

`wva_node_access_denied` is covered in [controller not Ready](#the-controller-is-not-ready-or-crash-looping);
a limiter in [GPUs held](#gpus-held-or-quota-spent); the last two in
[metrics](#metrics-stale-or-missing).

## A model will not wake from zero

**Symptom**: the workload stays at 0 replicas while requests are pending.

Work through these in order. Do not run `hack/verify_scale_from_zero.sh` during
an incident: it parks the model first.

1. **The InferencePool.**
   ```bash
   kubectl get inferencepool -n <ns>
   ```
   The scaling manager watches a single InferencePool API group
   (`inference.networking.k8s.io` or `inference.networking.x-k8s.io`). If the
   cluster's pools use the other group, the datastore stays empty and
   scale-from-zero never gets a recommendation. Create the InferencePool, and
   let it be reconciled, before the workload's ScaledObject.

2. **Labels.** The workload's pod labels must match the InferencePool selector:
   ```bash
   kubectl get deployment <deployment> -n <ns> -o jsonpath='{.spec.template.metadata.labels}'
   kubectl get inferencepool <pool> -n <ns> -o jsonpath='{.spec.selector}'
   ```

3. **The EPP.** It must exist and export its flow-control queue:
   ```bash
   kubectl get svc -n <ns> | grep epp
   ```
   A model with no queue metric at all shows
   `wva_model_scaling_blocked{reason="no-wake-signal"}`. Enable the EPP's
   `flowControl` feature gate, then restart the EPP: it reads its config once
   at start.

4. **The queue itself.** Read it straight from the EPP, with the token the
   controller uses. The EPP's metrics port varies by release; find it first
   and use it in place of `9090` below:
   ```bash
   kubectl get svc <epp-service> -n <ns> -o jsonpath='{.spec.ports}'; echo
   TOKEN=$(kubectl -n "$NS" get secret wva-epp-metrics-token -o jsonpath='{.data.token}' | base64 --decode)
   kubectl port-forward -n <ns> svc/<epp-service> 9090:9090 &
   curl -s -H "Authorization: Bearer $TOKEN" localhost:9090/metrics \
     | grep -E 'llm_d_epp_flow_control_queue_size|inference_extension_flow_control_queue_size'
   kill %1
   ```
   llm-d's EPP exports `llm_d_epp_flow_control_queue_size`; upstream
   gateway-api-inference-extension exports
   `inference_extension_flow_control_queue_size`. The scaling manager reads
   whichever exists. A queue that stays at 0 means requests are not reaching the
   EPP: send them through the inference gateway, not straight to model pods.

### The EPP scrape is failing but the scaling manager still wakes models (slowly)

**Symptom**: the log carries

```text
Scale-from-zero: EPP metrics scrape failing; reading the flow-control queue from
Prometheus instead. Wakes still work but are slower and bounded by the scrape
interval — fix the direct path.
```

and `wva_scale_from_zero_queue_fallback_active{pool="..."}` is `1`.

**What it means**: the scaling manager reads the flow-control queue by scraping
the EPP pod **directly** (pod IP, EPP metrics port, bearer token projected at
`/var/run/secrets/epp-metrics/token`). Only this path bypasses Prometheus, so it
can fail alone. Then the scaling manager reads the same metric from Prometheus:
models still wake, but at the scrape interval instead of a 100 ms loop. A sample
older than 90 s is ignored, so a drained queue cannot wake anything.

Check, in order:

1. **The token.** `Failed to read EPP metrics token` in the log means the
   projected volume is missing, so the unauthenticated scrape is rejected. The
   image has no shell, so check the Secret has a token and the Deployment
   mounts it:
   ```bash
   kubectl get secret wva-epp-metrics-token -n "$NS" -o jsonpath='{.data.token}' | wc -c
   kubectl get deploy wva-controller-manager -n "$NS" \
     -o jsonpath='{.spec.template.spec.volumes[?(@.name=="epp-metrics-token")]}'; echo
   ```
   A count of `0` means the token controller never filled the Secret; an
   empty second line means the volume is missing from the Deployment.
2. **The EPP's tokenreview RBAC** (cluster admin). It is bound per EPP release
   name, so a renamed or reinstalled EPP release leaves it pointing at nothing.
   ```bash
   kubectl get clusterrolebinding | grep epp-tokenreview
   ```
3. **Network reachability to the EPP pod IP.** A NetworkPolicy that admits the
   monitoring namespace but not the controller's breaks this path only.

Once the direct path works, the gauge returns to `0` and the log says the scrape
recovered.

### Slow wakes

**Symptom**: the model wakes, but late. `wva_scale_from_zero_wake_seconds`
shows the time from activation to serving.

- **Many models waking at once.** The scale-from-zero engine processes at most
  `SCALE_FROM_ZERO_ENGINE_MAX_CONCURRENCY` decisions at a time (default `10`).
  Raise it for a cluster with many parked models:
  ```bash
  kubectl set env -n "$NS" deploy/wva-controller-manager SCALE_FROM_ZERO_ENGINE_MAX_CONCURRENCY=50
  ```
  This restarts the controller. A later `make deploy-wva` re-applies the
  Deployment, so check it is still set.
- **The replica itself starts slowly.** Compare with `wva_replica_start_seconds`;
  see [Preparing a workload](workload-preparation.md).
- **Requests bypass the gateway.** Requests sent straight to model pods never
  queue in the EPP and never wake anything.

### E2E and infra-only deploys

- [`deploy/install-epp.sh`](../../deploy/install-epp.sh) turns on EPP flow
  control when `ENABLE_SCALE_TO_ZERO=true` (it adds the `flowControl` feature
  gate to the GAIE standalone chart).
- The **InferenceObjective** `e2e-default` comes from the scale-from-zero e2e
  tests (`test/e2e/fixtures`), not from the install scripts.

## A model will not park at zero

**First signal**: `wva_model_scaling_blocked` for the model:

```promql
wva_model_scaling_blocked{model_name="<model>"}
```

| reason | fix |
| --- | --- |
| `variant-floor` | a variant still has `minReplicaCount > 0`; set every variant to 0 |
| `policy-forbids-zero` | the model's resolved policy has `scaleToZero` off; turn it on for every tier the model's variants name (the `default` entry or its `defaultPolicy` tier when they name none), or on the model's per-model entry, or accept that it stays up |
| `activation-retention` | just woken and held for its retention period; clears by itself |
| `engine-unsupported` | the model runs both vLLM and SGLang; run one engine per model |

No reason, and still not parked? A model that has never served a request has
no request counter yet, so the controller keeps it where it is. Send one
request. Full runbook: [scale-to-zero
guide](../guides/scale-to-zero/README.md#when-a-model-will-not-park).

## A model is flapping

**Symptom**: replicas go up and down every few minutes. `WVAReplicaScalingThrashing`
fires at more than two decisions a minute for one variant.

That alert is not proof of flapping. Its counter, `wva_replica_scaling_total`,
rises on every cycle that is not `no-change`. A scale-up that is asked for and
never lands fires it too (actuation stuck, Pods Pending, slow starts). Step 1
tells them apart: if `tgt` is steady above `curr`, go to [actuation
stuck](#desired-is-not-current-actuation-stuck).

1. **Read what the controller decided**, cycle by cycle:
   ```bash
   $LOGS | grep scaling-decision | grep '"modelID": "<model>"' | tail -40
   ```
   `tgt` alternating between two values is the controller flapping. `tgt`
   steady while the replica count moves is the HPA or something else moving
   the workload: see [actuation stuck](#desired-is-not-current-actuation-stuck).
2. **Read why**:
   ```bash
   $LOGS | grep analyzer-result | grep '"modelID": "<model>"' | tail -40
   ```
   Compare `util` with `scaleUpThreshold` and `scaleDownBoundary` on the same
   line. `util` crossing back and forth over one of them is load sitting on the
   edge of the band. Widen the band in the scaling policy (lower
   `scaleDownBoundary`): see [Changing policy safely](operations.md#changing-policy-safely)
   and [scaling policy](scaling-policy.md).
3. **Check the HPA's stabilization windows.** KEDA's HPA acts on the
   controller's number through the ScaledObject's
   `advanced.horizontalPodAutoscalerConfig.behavior`:
   ```bash
   kubectl get scaledobject <name> -n <ns> \
     -o jsonpath='{.spec.advanced.horizontalPodAutoscalerConfig.behavior}'
   ```
   The generated ScaledObjects use scale-up `stabilizationWindowSeconds: 0` and
   scale-down `300`, set at generation by `WVA_SO_SCALE_UP_STABILIZATION` and
   `WVA_SO_SCALE_DOWN_STABILIZATION` ([configuration](configuration.md)). A
   scale-down window shorter than the model's load cycle releases a replica it
   will need again. Raise it on the object:
   ```bash
   kubectl patch scaledobject <name> -n <ns> --type=merge -p \
     '{"spec":{"advanced":{"horizontalPodAutoscalerConfig":{"behavior":{"scaleDown":{"stabilizationWindowSeconds":600}}}}}}'
   ```
   or set `WVA_SO_SCALE_DOWN_STABILIZATION` before `make scaledobjects-apply`.
   If the ScaledObject is managed by GitOps, change it there.
4. **Under the utilization-share optimizer**, a model whose load swings faster
   than a transfer lands shows `swinging` or `fast-swing-capped`; see
   [the optimizer runbook](#the-optimizer-evaluates-but-never-moves-anything).

## A model is pinned at max

**Symptom**: `wva_variant_at_max_replicas == 1`, or `"atMax":true` in
`scaling-decision`, and latency is high.

At max is normal for a well-sized variant under load. It is a problem only when
`util` in `analyzer-result` is still above `scaleUpThreshold`: demand goes
unserved, and more GPUs will not help. Raise the ceiling.

```bash
kubectl get scaledobject <name> -n <ns> -o jsonpath='{.spec.maxReplicaCount}'
kubectl patch scaledobject <name> -n <ns> --type=merge -p '{"spec":{"maxReplicaCount":<n>}}'
```

Check for free GPUs first; without them a higher ceiling gives Pending pods
([GPUs held](#gpus-held-or-quota-spent)). A benchmark pinned at its ceiling
measures the ceiling, not the autoscaler.

## Desired is not current (actuation stuck)

**Symptom**: `wva_desired_replicas` differs from `wva_current_replicas` for many
cycles; `scaling-decision` shows `tgt` not equal to `curr` cycle after cycle.

The controller decided; KEDA, the HPA or the workload did not follow. Check the
chain in order:

1. **The scale-down window.** A scale-down waits out the HPA's
   `stabilizationWindowSeconds` (300 s by default). Several minutes of
   `tgt < curr` is the design.
2. **KEDA got the number.**
   ```bash
   kubectl get scaledobject,hpa -n <ns>
   kubectl describe hpa keda-hpa-<name> -n <ns>
   ```
   `READY True` and a numeric `TARGETS` mean KEDA reads the controller. Otherwise
   go to [ScaledObject](#scaledobject-ready-false-or-targets-unknown). In the
   HPA's conditions, `ScalingLimited` means it hit `minReplicaCount` or
   `maxReplicaCount`.
3. **The Pods.**
   ```bash
   kubectl get pods -n <ns> --field-selector=status.phase=Pending
   kubectl get events -n <ns> --field-selector reason=FailedScheduling
   kubectl get events -n <ns> --field-selector reason=FailedCreate
   ```
   `FailedScheduling` with insufficient `nvidia.com/gpu` is a full cluster.
   `FailedCreate` with `exceeded quota` is a Kubernetes ResourceQuota
   (`kubectl describe resourcequota -n <ns>`), which the scaling manager does
   not see.
4. **Slow starts.** Pods created but not Ready for minutes: compare
   `wva_replica_start_seconds` with what you expect, and see
   [Preparing a workload](workload-preparation.md).

## GPUs held or quota spent

**Symptom**: the controller wants more replicas and does not get them, or a
model cannot grow while others hold GPUs.

| signal | means |
| --- | --- |
| `ResourceConstrained` Warning Event on the ScaledObject | a limiter capped this cycle's decision |
| `wva_decisions_limited_total` rising | same, as a counter per `limiter_name` |
| `wva_available_gpus` | GPUs the `gpu-inventory` limiter sees per accelerator type |
| `wva_unattributed_gpus > 0` | GPUs held by workloads whose accelerator did not resolve, charged to no pool, so every pool looks freer than it is |
| `wva_model_scaling_blocked{reason="quota-short"}` | under the utilization-share optimizer, the whole group is short |
| `GPU constraint provider skipped; the other limiters still bound scaling` in the log, `wva_constraint_provider_skipped == 1` | a declared limiter is left out of decisions; the others still bound, but nothing checks what it would deny. `reason`: `unobserved` (its GPU usage is not observed yet; clears after a refresh), `failed` (it could not compute, e.g. a Kueue read or a node list failed; the log line says why), `node_read_denied` (`gpu-inventory` beside a quota, no node read: grant it with `make setup-prereqs`). `GPU constraint provider bounds scaling again` ends it |
| `Physical limiter: replicas are not fitted to nodes this cycle` | the physical limiter bounds scaling but no node snapshot younger than 2 minutes exists, so replicas are fitted to the pool only, as before node fit; `reason` says why. Usually the GPU usage observation is failing (the same cause skips the provider soon after). `Physical limiter: replicas are fitted to nodes again` ends it. See [A replica must fit one node](gpu-limiter.md#a-replica-must-fit-one-node) |
| A model with free GPUs in `wva_available_gpus` does not grow; `ResourceConstrained` on its ScaledObject ends `(limited by node-fit)`; `wva_decisions_limited_total{limiter_name="node-fit"}` rising | the free GPUs are split across nodes in holes smaller than one replica (or one pod of it), on nodes tainted against it, or held for a replica still starting (up to 5 minutes, the in-flight hold): with the physical limiter a replica must fit one node. A P/D model needs a node for a prefill **and** a decode replica. Nothing to fix in the scaling manager; free a node, or use smaller replicas. See [A replica must fit one node](gpu-limiter.md#a-replica-must-fit-one-node) |
| `Upgrade note (transitional): this policy declares a physical GPU limiter and quotas, and both now bound scaling` | expected once after upgrading a policy that declares both kinds: the listed variants get a different target than the quota alone gave them, because the GPUs that exist now bound them too. Nothing to fix unless the physical bound is not wanted; then drop the `gpu-inventory` entry. Logged only when the set of variants changes, and removed in the next release |

A **fully** spent quota can show none of these: the decision is `no-change`
(`tgt` equals `curr` with `util` over the threshold) and no Event is recorded.
Compare the quota with what the models hold. First find where the limiters come
from: when a policy namespace governs this controller (`wva-policy` by default),
the limiters there apply and any in the controller's own ConfigMap are ignored:

```bash
$LOGS | grep -E 'Cluster policy comes from|Loaded cluster policy from the policy namespace|Ignoring limiters in the controller.s own ConfigMap' | tail -5
kubectl get configmap wva-scaling-policy-config -n "$NS" -o yaml | grep -A10 limiters
kubectl get configmap wva-scaling-policy-config -n wva-policy -o yaml | grep -A10 limiters   # (cluster admin) the policy namespace, if the log names another, use that
kubectl get pods -n <ns> -o custom-columns='NAME:.metadata.name,GPUS:.spec.containers[*].resources.limits.nvidia\.com/gpu'
```

What to do:

- Raise the quota, or free GPUs by lowering another model's `minReplicaCount`
  or `maxReplicaCount`. Lowering a quota does not evict: models move back under
  it only as they scale down.
- Resolve accelerators: set a GPU product `nodeSelector` or `nodeAffinity` on
  each workload (`AcceleratorNotResolved` Event names them).
- A `gpu-inventory` limiter counts allocatable GPUs on nodes, not GPUs free
  for scheduling.
- `WVAGPUResourceExhausted` (zero allocatable GPUs of a type): **(cluster admin)**
  check what the nodes advertise and that the device plugin runs on them. A node
  that is NotReady, or has no running device plugin, advertises zero.
  ```bash
  kubectl get nodes -L nvidia.com/gpu.product \
    -o custom-columns='NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,GPUS:.status.allocatable.nvidia\.com/gpu'
  kubectl get pods -A -o wide | grep -i device-plugin
  ```
  On AMD nodes read `amd\.com/gpu` instead.

See [the GPU limiter](gpu-limiter.md) and [the quota limiter](quota-limiter.md).

## Metrics stale or missing

**Symptom**: decisions look wrong or absent; a `MetricsUnavailable` Warning Event
on the ScaledObject; `WVAMetricsCollectionFailing` firing.

1. **Is Prometheus reachable?** `wva_metrics_collection_errors_total` rising, or
   Prometheus errors in the controller log, mean the query fails. Check the
   Prometheus URL and TLS settings ([Prometheus](prometheus.md#configuration)).
2. **Are the model pods scraped?** `wva_metrics_pods_discovered` should be above
   0 per model, and `wva_metrics_freshness_status{status="fresh"}` equal to the
   pod count.
   ```bash
   make verify-deployment
   kubectl get servicemonitor,podmonitor -n <ns>
   ```
   Then confirm the targets are up on Prometheus' `/targets` page.
3. **Requests queue for pods it cannot see?** `wva_unmeasured_queue > 0`, or an
   `UnattributedReadyPods` Warning Event (`<variant> has N ready pod(s) but none
   attributed`):
   ```bash
   kubectl get events -n <ns> --field-selector reason=UnattributedReadyPods
   ```
   The serving pods are not scraped, or their ownerReferences do not reach the
   scale target. On FMA, see [FMA launcher pods](operations.md#fma-launcher-pods).

While a variant's metrics are unavailable it keeps its current replica count.
Details: [Metrics health](metrics.md).

## Capacity priced high after a restart

A controller that restarts forgets what it measured, and prices each replica
from the deployment-derived capacity until the model saturates again; that is
higher than the measured figure, so the fleet is smaller and TTFT worse for a
while after every upgrade or leader change. `LEARNED_STATE_SNAPSHOT=true`
carries the measured state across ([configuration](configuration.md#learned-state-across-a-restart)).
With it on, `wva_learned_state_restores_total{outcome}` counts the variants
restored and rejected, `wva_learned_state_snapshot_errors_total{operation}` the
failed reads and writes, and the log says why (logger `learned-state`):

- `Learned state: no snapshot to restore` -- the first start with the store,
  or after a flush. Expected once.
- `Learned state: snapshot restored` -- read back; `pendingVariants` wait for
  their first cycle, and `aged`, `unusable` and `unattributable` count entries
  not restored. Then, per variant on its first cycle, one of:
  - `learned state restored`;
  - `learned state restored without the ITL baseline: the engine image
    changed, the arguments did not` -- a new release rolled out while the
    controller was down. Capacity, throughput and start time are kept;
  - `learned state not restored: the variant's engine arguments changed; its
    k2 is kept as a lower-only prior for the buckets it was measured in` --
    its flags changed while the controller was down. The old capacity is used
    only where it is lower, and expires (30 minutes from a bucket's first use,
    then a 6-hour relaxation);
  - `learned state not restored: the variant's engine arguments changed, and
    no k2 window was recent enough, on the hardware it runs now, to keep as a
    prior` -- the same, with nothing to keep: the windows were old, or were
    measured on another accelerator or GPU count;
  - `learned state not restored: the variant's engine configuration could not
    be read completely, so it could not be checked` -- a flag such as
    `--max-num-seqs $VLLM_MAX_NUM_SEQS` whose value comes from an `envFrom`.
    Nothing is restored for that variant, by design. `wva_engine_config`'s
    `unresolved` label names the flag;
  - `learned state not restored: the variant's engine configuration differs
    from the one it was learned under` -- a snapshot written by an older
    release. Expect it for every variant after an upgrade of the scaling
    manager that changes the `fingerprint_version` label of
    `wva_engine_config` -- this release moves it from 2 to 3, once.

  Each of these lines carries an `outcome` field: `whole`, `args`, `prior`,
  `incomplete` or `differs`.
- `Learned state: could not read the snapshot; nothing restored, and nothing
  will be written until a read succeeds` -- typically Forbidden: the Role
  `wva-learned-state-role` or its binding is missing. Ask an admin to re-run
  `NAMESPACE=<controller-namespace> make setup-prereqs`. Retried every minute;
  the stored state is not overwritten meanwhile.
- `Learned state: snapshot not restored` -- the document names no writer, or
  a controller in another namespace wrote it (a ConfigMap copied by hand).
- `Learned state: the snapshot was over its size bound` -- the least recently
  used entries were left out; they are relearned after a restart.

To flush it -- after a model's engine or hardware changed in a way the
configuration check cannot see, say -- delete the ConfigMap:

```bash
kubectl delete configmap wva-learned-state -n <controller-namespace>
```

The running controller notices within a minute or two and forgets everything
it learned too, rather than writing it straight back (`Learned state: the
snapshot was deleted; everything learned is forgotten`). Every model then
prices from the derived capacity until it saturates again, as after a restart
with no store.

## The controller is not Ready or crash-looping

```bash
kubectl get pods -n "$NS" -l app.kubernetes.io/name=workload-variant-autoscaler
kubectl describe pod -n "$NS" -l app.kubernetes.io/name=workload-variant-autoscaler
kubectl logs -n "$NS" deploy/wva-controller-manager --previous    # after a crash
```

A NotReady controller has no endpoints on the `wva-external-scaler` Service, so
KEDA cannot reach it and **no** model scales. To see which readiness check fails:

```bash
kubectl port-forward -n "$NS" deploy/wva-controller-manager 8081:8081 &
curl -s 'localhost:8081/readyz?verbose'
kill %1
```

| failing check, or log line | means | fix |
| --- | --- | --- |
| `gpu-policy`: `cluster policy declares a gpu-inventory limiter but this controller cannot confirm it may list nodes` | a `gpu-inventory` limiter, declared alone, is in cluster policy and the node list failed. Kept NotReady on purpose: otherwise every variant would silently stop scaling up. For `forbidden`, `wva_node_access_denied` and `WVANodeAccessDenied` report the same; `Unauthorized` and `no answer yet` do not set them. The error after the colon says which: `forbidden` is missing node read; `Unauthorized` means the API server rejected the controller's credentials; `no answer yet` means the API server has not answered since the pod started (2s timeout per try) | Forbidden: (cluster admin) grant node read: `make enable-physical-limiter` grants every controller it finds and verifies when it runs, so re-run it after a new install. Or remove the limiter. Unauthorized: check the controller's ServiceAccount token, then restart the pod. No answer yet: check the API server; no action unless it persists ([details](gpu-limiter.md#when-policy-demands-a-limiter-the-controller-cannot-serve)) |
| `Cluster policy declares gpu-inventory beside quota limiters, but this controller may not list nodes` (an error line; the pod stays Ready) | the physical limiter is skipped and only the quota bounds; `wva_constraint_provider_skipped{reason="node_read_denied"}` is 1. Only a definite answer (`forbidden` or `Unauthorized`) sets it: a node list that gets no answer keeps what was last reported | (cluster admin) grant node read: `make setup-prereqs` for the install, or `make enable-physical-limiter`. Or drop `gpu-inventory` to keep the quota alone |
| `readyz`: `initial ConfigMap bootstrap not complete`, with `cannot read cluster policy from <ns>/<name>, refusing to start unbounded` in the log | the controller cannot read the cluster policy ConfigMap in the policy namespace (`wva-policy` by default). It refuses to run without its limits | (cluster admin) grant it get/list/watch on ConfigMaps there; `make enable-physical-limiter` does this for every controller it finds and verifies when it runs (re-run it after a new install) |
| `CrashLoopBackOff`, log `refusing to start unbounded: the "default" entry of ConfigMap <ns>/<name> is invalid` | the `default` entry of the ConfigMap the limiters come from has an invalid value. It carries the GPU limits, so the controller will not start without it. The entry was often edited while the controller ran: then the old entry stayed in force, and a `ConfigEntryRejected` event warned that the next restart would fail | fix the entry the log names, then let the pod restart. The line before it gives the reason: `Scaling-policy entry rejected: entry "default" rejected (<reason>)` |
| `external-scaler` | the gRPC scaler KEDA calls is not serving | read the log around startup for the bind error |
| pod restarts (`CrashLoopBackOff`) at startup, log `Shutdown requested during startup` after Prometheus connection errors, exit code 143 (read it with `kubectl get pod <pod> -n "$NS" -o jsonpath='{.status.containerStatuses[0].lastState.terminated.exitCode}'`) | the controller cannot reach Prometheus at startup and keeps retrying. Its health endpoint is not served yet, so the startup probe kills it after about 60s and the pod restarts | make Prometheus reachable: check the URL, TLS settings and network policy ([Prometheus](prometheus.md#configuration)). The restarts stop once it connects |
| `CrashLoopBackOff`, log names a Prometheus setting | a plain `http://` URL without `PROMETHEUS_ALLOW_HTTP=true`, or TLS settings combined with plain HTTP | fix the Prometheus settings ([Prometheus](prometheus.md#configuration)) |
| `CrashLoopBackOff`, `unknown flag` | the image is older than the manifests | deploy an image built from the same release as the manifests ([Upgrade](upgrade-and-uninstall.md)) |
| `CrashLoopBackOff`, `x509: certificate signed by unknown authority` | verification is on by default now, and a private CA signed the Prometheus certificate | mount the CA, then re-run the install with `PROMETHEUS_CA_CERT_PATH=<file>` and every other variable you installed with. The re-run restarts the controller ([TLS configuration](prometheus.md#tls-configuration)) |

## Cluster policy is not applied (policy namespace unverified)

**Symptom**: `WVAPolicyNamespaceUnverified` firing, or
`wva_policy_namespace_unverified == 1`.

The controller may not read Namespaces, so it cannot see whether a cluster
admin published policy for it. It runs on the ConfigMap in its own namespace,
and any limiters or quotas an admin published are **not** in force for it.

```bash
$LOGS | grep -E 'Cannot tell whether a cluster admin published policy|Could not read the managed Namespace' | tail -3
```

Fix **(cluster admin)**: grant the Namespace read, then restart the controller.
The gauge is set once at startup.

```bash
NAMESPACE=<controller namespace> make setup-prereqs
kubectl rollout restart deployment/wva-controller-manager -n "$NS"
```

After the restart, the log says where policy comes from:

| log line | policy in force |
| --- | --- |
| `Cluster policy comes from the namespace this one is labelled with` | the admin's, from the namespace in the `wva.llmd.ai/policy-namespace` label |
| `Cluster policy comes from the well-known namespace` | the admin's, from `wva-policy` |
| `Cluster policy comes from this controller's own namespace` | this controller's own ConfigMap; no admin policy exists. Logged at `-v=1`, which the default `-v=2` shows |

## ScaledObject READY False or TARGETS `<unknown>`

```bash
kubectl get scaledobject,hpa -n <ns>
kubectl get scaledobject <name> -n <ns> -o jsonpath='{.spec.triggers[0].metadata}'
kubectl get endpoints -n "$NS" wva-external-scaler
```

| sign | cause | fix |
| --- | --- | --- |
| `scalerAddress` names a namespace where no scaler runs | the ScaledObject points at another or a removed install | `make scaledobjects-repoint` |
| the trigger has no `modelID` | the controller cannot tell which model to answer for | set `modelID` to what the container serves; `make verify-scaledobjects` |
| `wva-external-scaler` has no endpoints | the controller is not Ready | [controller not Ready](#the-controller-is-not-ready-or-crash-looping) |
| `make verify-deployment` warns that the external scaler NetworkPolicy admits `:9090` ONLY from a namespace KEDA does not run in | the NetworkPolicy admits the wrong namespace, so KEDA's calls are dropped while the controller reads healthy | (cluster admin) `KEDA_NAMESPACE=<namespace KEDA runs in> make setup-prereqs` with the install's other variables ([details](upgrade-and-uninstall.md#the-external-scaler-networkpolicy)) |
| `TARGETS` reads `cpu: <unknown>/80%` | KEDA never got a metric spec and fell back to CPU | one of the above |
| the replica count never moves, and `wva_scaler_undecided_targets` stays above 0 for the namespace | the scaler answers the current count because the model has no usable decision. For a cycle or two after a controller restart or leader change that is normal. `wva_scaler_undecided_answers_total` says why: `reason="not-managed"`, or `no-decision` that never stops, means the model is never planned, usually a trigger value that does not parse; `stale` means its decision stopped being republished, so read that model's `scaling-decision` lines | [a bad value freezes the model](scaledobject-trigger.md#model-keys); `external scaler has no decision for this target` names the ScaledObject, and the controller log at `--v=4` names the key |
| the `Fallback` condition `True`, or `KEDAScalerFailed` that persists | KEDA cannot reach the scaler: the controller is down or not Ready, or the Service has no endpoints | [controller not Ready](#the-controller-is-not-ready-or-crash-looping) |
| `no children to pick from` in KEDA's log, after an uninstall and reinstall | KEDA's gRPC client is still backing off on a name that did not resolve | (cluster admin) restart the KEDA operator ([details](operations.md#no-children-to-pick-from-after-a-reinstall)) |

More rows: [First-line troubleshooting](operations.md#first-line-troubleshooting).
Removing or reinstalling the controller: [Upgrade and uninstall](upgrade-and-uninstall.md).

## A model misses, or is never judged against, its SLO

**Symptom**: `WVAModelSLOMissed` (warning) or `WVAModelSLOUnjudged` (info)
fires, or the SLO dashboard row stays empty for a model whose policy sets
targets. Both alerts carry `component: slo`, so they can be routed apart from
pages.

**On this release the slo analyzer only observes**, so neither alert means the
autoscaler failed: the fleet is sized by the saturation analyzer, and a miss
says that tier's thresholds do not meet the SLO. It is a tuning finding to act
on in working hours, not an outage. Set up in
[`slo` (observe only)](scaling-policy.md#slo-observe-only).

The `wva_slo_*` series carry the model's namespace as `exported_namespace`
(see [the labels](prometheus.md#slo-analyzer-metrics)); filter on that, not on
`namespace`.

### `WVAModelSLOMissed`

At least half of the judged minutes of the last 30, and at least 5, were over
target.

**1. Check the target can be met at all.** If it cannot, more replicas do not
help: loosen the target, or move the model to faster hardware.

```promql
# tpot: 1 for "infeasible" -- even an almost empty replica is over the target
wva_slo_source{model_name="<model>", source="infeasible"} == 1
# ttft: prefill time per pod (vLLM), queue left out; at or above the TTFT
# target, no replica count meets it
rate(vllm:request_prefill_time_seconds_sum{namespace="<ns>"}[5m]) / rate(vllm:request_prefill_time_seconds_count{namespace="<ns>"}[5m])
```

`infeasible` read from a `pinned-default` line is not reliable: that line's
intercept is a default for no particular card.

**2. If it can, mitigate:** give the model more capacity than saturation asks
for:

- raise its floor: `minReplicaCount` on each variant's ScaledObject
  (`kubectl edit scaledobject <so> -n <ns>`; where a tool generates the
  ScaledObject, change it there, or the next apply undoes it), or
- lower the tier's `scaleUpThreshold` (and write the tier's other fields out,
  see [Turn it on](scaling-policy.md#slo-observe-only), step 3).

**3. Then find the cause**, per objective:

```promql
# tpot: the mean ITL the variant would show sized for the target, against the target
wva_slo_predicted_itl_seconds{model_name="<model>"}
wva_slo_target_seconds{model_name="<model>", objective="tpot"}
# and the fill the target allows, before headroom: below kvCacheThreshold
# (0.80 by default), the target needs more replicas than the tier gives
wva_slo_fill{model_name="<model>"}
# ttft: the engine's queue wait against its prefill time, per pod (vLLM)
rate(vllm:request_queue_time_seconds_sum{namespace="<ns>"}[5m]) / rate(vllm:request_queue_time_seconds_count{namespace="<ns>"}[5m])
rate(vllm:request_prefill_time_seconds_sum{namespace="<ns>"}[5m]) / rate(vllm:request_prefill_time_seconds_count{namespace="<ns>"}[5m])
```

A queue that grows while prefill time stays flat is a capacity miss; a prefill
time that grows is a slow prefill (long prompts, a cold prefix cache). The
router's TTFT pools every priority band, so where its flow control holds a
low-priority band back on purpose, that wait counts as a miss too. Only
completed requests count, so under shedding the alert can resolve while
requests are refused: read `llm_d_epp_request_error_total` too.

The alert resolves once the judged minutes that meet the target outnumber the
misses in the last 30, so it stays up for a while after a fix. Changing the
tier's TTFT percentile leaves the old percentile's alert up for about 5 minutes.

### `WVAModelSLOUnjudged`, or nothing judged at all

A target set for 55 of the last 60 minutes, and no window judged in the hour. The alert
needs `wva_slo_target_seconds`, which exists only when the policy is right, so
**for the alert, skip to the router side below**. For an empty dashboard row
with no alert, start with the policy side -- the model's last
`Effective scaling policy` line in the controller log (logged when the policy
changes, so on a long-running controller it may have rotated out; restarting
the controller logs it again):

```bash
$LOGS | grep 'Effective scaling policy' | grep '"modelID": "<model>"' | tail -1
```

| the line shows | means |
| --- | --- |
| `"scalingPolicy"` is not the tier you set | the model's variants do not all name it (the alphabetically first name wins), or the tier is defined only in the global map and the namespace has its own map, which replaces it whole |
| `"slo": "off"` | the tier it reached has no `slo` block, and does not list the analyzer |
| `"slo"` ends `(analyzer not listed)` | targets set, `slo` not under `analyzers:` (also logged as `SLO policy is half-configured`) |
| `"slo"` ends `(observe)` | the policy is right: read the router side below |

Then the router side:

```promql
wva_slo_window_requests{model_name="<model>"}   # requests the router recorded, per objective
wva_slo_excluded{model_name="<model>"} == 1     # the reason a window is not judged
# the share of the last day it was not excluded (for either objective)
1 - avg_over_time(max by (exported_namespace, model_name) (wva_slo_excluded{model_name="<model>"})[1d:5m])
```

| what you see | means | fix |
| --- | --- | --- |
| `excluded` 1 for `starting` or `zero-replicas`, most of the time | the fleet never holds still for the window plus its grace: constant scale-ups, or a replica that keeps starting | check the starts are real (`kubectl get pods -n <ns>`). If the replica count goes up and down (`wva_desired_replicas`), the fleet flaps, which is a tuning question for the tier's band, not for the SLO. If it only climbs, it is growing, and the model is judged once it settles. Silence the alert meanwhile |
| `excluded` 1 for `unobserved` | the analyzer did not analyze the model on the cycle before: just after a controller restart, or the engine keeps skipping the model | if it persists, the model's replicas lack metrics on some cycles: [metrics](#metrics-stale-or-missing) |
| requests present but low, nothing excluded | too little traffic: a p90 needs 100 requests in the 5-minute window, a p95 200, a p99 1000, a TPOT mean 50 | nothing to do: the alert does not fire for this, and the model is judged once traffic returns |
| `tpot` requests `0` while the router serves streaming requests | clients do not send `stream_options.include_usage`; or the router does not publish `llm_d_epp_request_streaming_tpot_seconds`; or the router's TPOT query fails (`wva_metrics_collection_errors_total{query_type="router_latency"}` rising) | have clients send it, or run vLLM with `--enable-force-include-usage`; check the router publishes the series (`count(llm_d_epp_request_streaming_tpot_seconds_count{namespace="<ns>"})`); for a failing query, [metrics](#metrics-stale-or-missing) |
| no `window_requests` series at all | the router's histograms are not read | below |

When there is no `window_requests` series, check:

```promql
# Does Prometheus have the router's histogram for the model, and where?
sum by (namespace, model_name, target_model_name, streaming) (rate(llm_d_epp_request_ttft_seconds_count[5m]))
```

Here `namespace` is right: it is the router's own scrape label.

- **No series anywhere**: the router (EPP) is not scraped, or is too old to
  publish `llm_d_epp_*`. Check its ServiceMonitor / PodMonitor and version.
- **A series in another namespace**: the EPP runs outside the model's
  namespace. That is not supported: the analyzer reads the router in the
  model's namespace only, and nothing configures another. Run the EPP in the
  model's namespace.
- **Only `streaming="false"`**: the clients do not stream; only streaming
  requests are counted.
- **A series under another name**: the analyzer takes the model from
  `target_model_name`, or `model_name` where that is empty; clients that address
  an alias or an objective name show up under it, not under the model ID the
  controller logs. So do requests to a LoRA adapter: they are counted under the
  adapter's name, never the base model's.

## Utilization-Share Optimizer

Configuration: [`optimizer` in the scaling policy](scaling-policy.md#optimizer-cluster-default-only-live).
Metrics and blocked reasons: [utilization-share metrics](prometheus.md#utilization-share-optimizer-metrics).

### The optimizer evaluates but never moves anything

Work down the list in order.

1. **Is it selected at all?** Exactly one `wva_utilization_share_mode` series
   is `1`: `off`, `invalid`, `shadow` or `active`.
   - For `invalid` or `off`, the log says why: `Utilization share: invalid
     optimizer block; keeping today's optimizer` (a misspelled key, a value out
     of range, an empty `clusterNamespaces` entry, or `needs a limiters: list`,
     no budget to share).
   - A block in a namespace-local scaling-policy map is ignored: `ignoring
     optimizer blocks in namespace-local scaling-policy maps`.
   - A namespace-scoped install that reads cluster policy from a separate policy
     namespace reads an edit there only at start. Restart it after the change.
2. **Is it in shadow mode?** `wva_utilization_share_mode{mode="shadow"} == 1`.
   The log says `Utilization share: would rebalance (shadow)`, not `Utilization
   share: rebalancing`, and `wva_utilization_share_promised_gpus` is absent (it
   is published only while acting). Set `shadow: false` (or remove the key) to
   act. Both lines are written when the set of actionable roles changes, not
   every cycle, so a quiet log is not a stopped optimizer.
3. **Is the model planned?** The two lines above, and at `--v=4` the per-cycle
   `Utilization share: evaluation`, carry a `frozen` field. It lists the
   group's models left to today's optimizer this cycle, with the reason: no live
   analyzer result, a role with demand but no measured capacity, a
   namespace-local scaling-policy map (not planned in the cluster group), or a
   namespace not in `clusterNamespaces`. These models are in no group at all
   and publish no role series: variants on more than one accelerator type, a
   namespace with `enabled: false`, only a `gpu-inventory` limiter without
   `physicalGroups: true`, or a namespace a namespace quota lists in `exclude`
   with no finite `scope: cluster` quota for its accelerator type (logged once
   as `Utilization share: not sharing models no quota bounds`).
4. **Is any role actionable?** `wva_utilization_share_actionable` is `1` only
   for a role outside its tolerance band *and* off its whole-replica target. All
   `0` means the fleet is where the optimizer wants it, or every imbalance is
   under one replica (counted by
   `wva_utilization_share_withheld_total{reason="not-actionable"}`).
5. **Does it stay actionable?** A role must be actionable on **two consecutive
   cycles** before a transfer is planned, so a role that flickers never moves.
   A hard imbalance (step 6) does not wait. At most two replicas move per role
   per cycle, and two transfers run per group at once.
6. **Is it held?** `withheld_total{reason="reversal-hold"}` rising, or the
   blocked reason `reversal-hold`, means a role that just gave is kept from
   receiving (or the reverse) until the hold in
   `wva_utilization_share_effective_seconds{param="reversal-hold"}` passes. This
   is the anti-oscillation rule working, not a fault.
   - The hold lasts about twice a release time from the start of the transfer
     it gave in: about 12 minutes with a 300 s scale-down window.
   - It is lifted for a **hard imbalance**: a role at 0.9 or more of its
     scale-up threshold receives at once from a role at 0.6 or less of its own
     after giving (`utilizationShare.stabilization.skipWaitsWhen`). The log
     shows `skippedWaits=true`, and the role does not show `reversal-hold`
     while such a donor exists. A role held short for the whole hold means no
     other role is that lightly loaded.
   - A cancelled transfer holds only its own donor -> receiver pair; each role
     can still move with any other role.
   - The counter also counts moves withheld by a donor's back-off after an
     aborted release, or by a donor that has given all it can.
   - A donor's calm is judged on the higher of its current and peak need (two
     readings in a row) over its scale-down window, so a model in a trough of
     its normal demand is not "lightly loaded". For a receiver whose bursts
     outlast a move, the donor is judged on its current need alone: the move
     lands inside the burst.

   Three more reasons on the same counter are not holds:
   - `donor-peak`: while the quota holds every model's peak, a model already at
     its need takes no headroom that would leave its donor below the donor's
     own recent peak, by a single move or a donor set.
   - `awaiting-return`: a short model is credited with returns still releasing
     whose pods hold its pods, in whole replicas; only the rest is funded by
     draining other models. The model shows blocked reason `awaiting-release`.
   - `scale-down-band`: a return the model did not make because it would go
     lower than today's optimizer scales down to (demand a quarter of the way
     from `scaleDownBoundary` to `scaleUpThreshold`). A return is today's
     scale-down, and goes no lower.
7. **Is it swinging, paced, or at the transfer limit?** Blocked reasons:
   - `swinging` (`wva_utilization_share_swinging == 1`): the role reversed
     direction twice within the swing window and is planned on its mean need,
     so it may look short and still not receive.
   - `transfer-limit`: the group already runs two transfers with a donor;
     clears as they land.
   - `burst-ending`: the role is short with a hard imbalance, but its model's
     burst should end (by the length of its last completed burst) before a
     transfer lands, so it receives one replica a cycle instead of its whole
     shortfall.
   - `fast-swing-capped`: the role swings faster than a move while another model
     bursts for longer than one; its headroom is capped on its mean need, and it
     serves each crest from what it holds.

   `burst-ending` and `fast-swing-capped` are passing states; give the model a
   floor if it must not lag.
8. **Did the controller just restart?** After a restart, after the optimizer
   starts acting, or when a group first appears, every planned model of the
   group holds what it runs (or its restored target) for one fill timeout, about
   three minutes by default, and nothing in it scales. A fill in flight before
   the restart has no mark, and its receiver's pods must not lose their GPUs
   meanwhile. Expect this on every upgrade or leader change.
   - While it lasts, every planned model shows blocked reason `quiet-period`,
     and the log line `Utilization share: ledger started` carries
     `planningFrom`.
   - A freeze that does not end shows `marks-unreadable`: the controller cannot
     read the transfer marks on the donors' pods. The Error line `Utilization
     share: could not read transfer marks; retrying next cycle` says why,
     typically a missing RBAC grant to list pods in one model's namespace.

   **The need history.** The models' demand history survives a restart in the
   ConfigMap `wva-utilization-share-history` in the controller's namespace.
   - `Utilization share: need history restored` says it was read back.
     `rolesKnownNow` counts roles whose peaks are known at once; `peaksKnownAt`
     is when the first restored role's peak is known (in the past when some are
     known now).
   - With no usable history (none stored, or one ending more than ten minutes
     before the restart), the headroom cap is on until a
     `stabilization.flappingWindow` of history exists and the peaks are known
     ([a model shrinks while no other model grows](#a-model-shrinks-while-no-other-model-grows)).
     `wva_utilization_share_headroom_capped` is not published during the quiet
     period, when the planner does not plan.
   - A history shorter than today's `flappingWindow` (the window was raised
     since) is restored, but its peaks are known only once today's window is
     covered. `Utilization share: stored need history not used` says why an
     entry was ignored (`reason`; `older than its window` when every sample has
     aged out).
   - `Utilization share: could not read the stored need history; the models'
     peaks are unknown for a swing window and their headroom is capped
     meanwhile. Retrying every minute; nothing is written until a read
     succeeds` (with the `group`): the read failed with something other than
     NotFound, typically Forbidden because the Role
     `wva-utilization-share-history-role` or its binding is missing. Nothing is
     restored or written until a read succeeds, so the stored history is not
     overwritten. `Utilization share: could not store the need history; a
     restart now forgets the models' peaks` is the same for the write. Both are
     logged per group, once per change of the error; `Utilization share: the
     need history store works again` marks recovery.
   - On an install upgraded from before the store existed, the controller-only
     `make deploy-wva` reports the Role and binding as optional and installs
     without them. Ask an admin to re-run `make setup-prereqs` in the install's
     shape: `NAMESPACE=<ns> make setup-prereqs`; out of reach
     `NAMESPACE=<managed ns> make setup-prereqs WVA_NS=wva-<managed ns> WVA_WATCH_NS=<managed ns>`;
     cluster-scoped `make setup-prereqs SCOPE=cluster WVA_NS=<controller ns>`.
   - The ConfigMap stays below the API server's 1 MiB limit: at most 128 roles
     per group, and an equal share of 900 KiB per group written. A group over
     either bound leaves out the roles with the smallest stored peaks, which
     count at their `maxReplicas` after a restart. `Utilization share: need
     history truncated; the roles left out count at their ceiling after a
     restart` names them (`dropped`, the first ten), once per change.

   To flush the history (say a model's load changed for good and its old peaks
   keep the cap on), delete the ConfigMap:
   ```bash
   kubectl delete configmap wva-utilization-share-history -n <controller-namespace>
   ```
   The controller notices on its next read or write (within a minute or so) and
   forgets its in-memory history too, instead of writing it back: `Utilization
   share: the need history ConfigMap was deleted; every model's need history is
   forgotten`. For one window the cap then treats every model as after a
   restart with nothing stored. Editing one group's key by hand does not flush
   it; the next write replaces it from memory. Entries the controller cannot
   parse are removed by its next write, and entries of another format version
   once a week old. `make undeploy-wva` deletes the ConfigMap.
9. **Could it not mark the donor?** `Utilization share: could not mark a donor pod;
   transfer not started` means a fault stopped the donor's pod being steered: a
   sibling's deletion cost leaves no room for the mark (see [which pod
   leaves](scaling-policy.md#for-model-owners-what-the-optimizer-may-take-and-how-to-protect-a-model)),
   or the patch failed. The donor backs off, shows blocked reason
   `donor-not-steerable`, gets a `UtilizationShareDonorNotSteerable` Event naming
   the cause and the end of the back-off, and is retried after it.

   Two donor states are holds, not failures. In both, the receiver tries another
   donor, and the donor counts no abort, does not back off, and gets no Event or
   blocked reason:
   - **Its workload is changing:** mid-rollout (by its status, pods of two
     ReplicaSets, or LWS groups of two revisions at or above its partition), a
     pod not yet created, scheduled or Ready, or an LWS group missing or being
     replaced. At `--v=4`: `Utilization share: donor's workload is changing;
     transfer not started`. A paused Deployment or a held LWS canary rollout
     stays "changing", and does not give, until resumed or finished.
   - **It has nothing left:** every pod (or LWS group) is already given to
     transfers in flight. It is held until one of those releases lands, or for
     one release timeout, and is not counted as `reversal-hold`. At `--v=4`:
     `Utilization share: donor has nothing left to give; transfer not started`.

Other cases:

- **`whole-replica-short`:** the model serves below its need because the quota
  covers the needs in fractions but not in whole replicas (with 8-GPU replicas,
  a quota a few GPUs short of every need rounded up to whole nodes). Moves go to
  the model worse off by weight, and this one gave. Raise the quota by a
  replica, lower a floor, or accept the trade.
- **A P/D model with a blocked reason:** `wva_model_scaling_blocked` has no
  `role` label. Read `wva_utilization_share_headroom` and
  `wva_utilization_share_actionable` by `role`.
- **A lowered quota does not shrink the fleet:** the optimizer does not evict.
  A group above its quota plans over what it holds and moves back under only as
  models scale down.
- **Transfers start but the model does not grow:** read its
  `wva_model_scaling_blocked` reasons.

### A model shrinks while no other model grows

**Symptom**: under the utilization-share optimizer a model loses one or more
replicas, at least a few cycles plus one scale-down window after its load fell, and no other
model in the quota group grows to take the GPUs.

**What it means**: the model gave headroom back to the quota. This happens only
while the quota is **contended**.

- **Contended** means the quota cannot hold every model at its peak need over
  the last `stabilization.flappingWindow` at once (a peak counts once two
  readings in a row reach it). While the peaks fit, the whole spare stays spread
  by weight and nothing is given back.
- **Unknown peaks count high.** While no model's peak is known (a new group, or
  every model after a restart with no usable stored history, see step 8 above),
  each counts at its `maxReplicas` (the whole quota without one) until a
  `flappingWindow` of history shows the peaks fit. A model that joins a group
  whose peaks are known counts at what it holds, and does not turn the cap on
  by itself.
- **The cap.** While contended, each model (each role of a P/D model) keeps at
  most `maxHeadroomReplicas` (default `1`) times its weight, rounded up, above
  its need. The rest of the quota stays unassigned, so a model whose demand
  rises can start pods at once instead of waiting for another model's pods to
  finish their requests.
- **The return.** A model that held more than its need plus headroom for
  `stabilization.consecutiveCyclesBeforeMove` cycles in a row (two by default),
  typically after a burst ended, gives all the extra back in one cycle. The
  release then waits its scale-down window, as today's scale-down would. It
  returns only while:
  - no model in the group that can grow is short of its need;
  - its load would stay at or below `stabilization.returnSpareWhenLoadAtMost`
    (default `0.6`) on what it keeps, not counting headroom.

  While more than 0.6 x `queueLengthThreshold` requests (3 by default) wait on
  one of its pods for `consecutiveCyclesBeforeMove` cycles in a row, the return
  also waits, for at most its scale-down window. A decode pod of a P/D model
  counts only while its KV cache is full; prefill pods are not counted.
- **The floor.** It never goes below its need (the higher of current and
  average need while it swings) plus headroom, below what the other models'
  recent peaks leave of the quota in whole replicas, its `minReplicaCount`, or
  its last replica. A model that bursts alone keeps its spare; GPUs are returned
  only when another model's recent peak could use them.
- **Calling it off.** If, during the scale-down window, the model is short of
  its target again or busier than that load on what it keeps, the return is
  called off and it keeps the replica. If another model turns short while the
  return is still releasing, the returned GPUs go to the unassigned GPUs as
  usual, and the idle fill raises that model into them once the replica is
  released.

**How to tell**: the donor's scale target carries a `UtilizationShareGiving`
Event that names "the quota group's unassigned GPUs" and says "because it holds more headroom than maxHeadroomReplicas gives it while no model is short, and stays calm without it; the replica drains after its scale-down window, and is kept if its demand comes back before then; a model whose demand rises takes the GPUs at once". A return called off carries a
`UtilizationShareCancelled` Event: "this model's demand came back, so it keeps
the replica it was giving back". The controller log has
`Utilization share: transfer started` with `returned` set to true and no
`receiver`, then `Utilization share: spare returned to the quota` when the
replica is released. `wva_utilization_share_starts_total{kind="return"}`
counts returns per quota group. A model that holds more than its headroom and
is not giving it back shows why in `wva_utilization_share_withheld_total`:
`return-not-calm` (its load is above `returnSpareWhenLoadAtMost`),
`return-pod-busy` (its load would let the return go, but more than
`returnSpareWhenLoadAtMost` x `queueLengthThreshold` requests are waiting on
one of its pods; counted until the scale-down window bound passes) or
`return-role-short` (another model is short of its need).

Whether the cap is on: `wva_utilization_share_headroom_capped == 1` for the
group (published only while the optimizer acts, not in shadow mode, and not
during the restart quiet period, when the planner does not plan). The log says
when it flips, with `peakClaimGPUs` (the models' peaks summed in whole
replicas), `budget` and a `cause`. It turns on with one of:

- `cause=peaks-unknown`: `Utilization share: the models' peaks are not known
  yet -- ...`, with `unknownPeaks` naming the roles counted at their
  `maxReplicas`. This is the new-group rule above, or the restart rule when
  no `need history restored` line preceded it: it turns off
  once a `flappingWindow` of history exists
  (`wva_utilization_share_effective_seconds{param="swing-window"}`), if the
  recorded peaks fit.
- `cause=peaks-exceed-budget`: `Utilization share: the models' recent peaks do
  not fit the quota together; each keeps at most maxHeadroomReplicas of
  headroom, and the rest is returned`. The recorded peaks really do not fit.

It turns off with `Utilization share: the models' recent peaks fit the quota
together again; the spare is spread by weight`. Each line carries `scope` as
the metrics label it (`cluster` for the cluster group), so it joins the
group's series.

```bash
kubectl get events -n <model-namespace> --field-selector reason=UtilizationShareGiving | grep unassigned
kubectl logs -n <controller-namespace> -l app.kubernetes.io/name=workload-variant-autoscaler --tail=-1 | grep 'spare returned to the quota'
kubectl logs -n <controller-namespace> -l app.kubernetes.io/name=workload-variant-autoscaler --tail=-1 | grep -E 'recent peaks|peaks are not known'
```

**What to do**: nothing, if the model still serves within its targets. The GPUs
are free for whichever model needs them next, and this model takes them back the
same way when its demand rises. Otherwise:

- **More spare on each model:** raise `utilizationShare.maxHeadroomReplicas`. A
  value as large as the models' `maxReplicas` spreads the whole spare by weight
  even while contended and leaves little to return, but a model whose demand
  rises must then wait for another model's pods to drain.
- **Keep replicas longer after demand falls:** lower
  `stabilization.returnSpareWhenLoadAtMost`.
- **Protect one model:** raise its `minReplicaCount`. A higher `weightClass`
  gives it more headroom replicas (weight times `maxHeadroomReplicas`, rounded
  up) but does not shield them from another model's need.

See the [scaling policy reference](scaling-policy.md#optimizer-cluster-default-only-live).

### A model shows `no-compatible-donor`

**Symptom**: `wva_model_scaling_blocked{reason="no-compatible-donor"} == 1` on a
model that is short, while other models on the same accelerator type hold more
than they need.

**What it means**: each of the model's pods needs a donor pod at least as large.
A 4-GPU pod cannot be funded by two 2-GPU pods, since nothing shows they share a
node. No donor in the group has pods large enough.

**What to do**: no optimizer setting changes this. Raise the quota for that
accelerator type, or lower a model whose pods are at least as large. With pods
of more than one size (P/D roles with different GPU counts), each role is judged
on its own pods; find the short one with `wva_utilization_share_headroom < 0`.

### Transfer marks left on pods after a downgrade

**Symptom**: after moving to a version without the utilization-share optimizer,
some pods still carry `llm-d.ai/utilization-share-transfer` and a low
`controller.kubernetes.io/pod-deletion-cost` (`-1000`, or lower when a sibling's
cost was lower), so the ReplicaSet removes them first on every scale-down. The
same happens after renaming a controller's `CONTROLLER_INSTANCE`: the controller
removes only marks carrying its current instance, so those written under the old
name stay.

**Prevention**: before downgrading (or renaming `CONTROLLER_INSTANCE`), set
`shadow: true` (or remove the `optimizer:` block) and wait one optimization
cycle. The controller removes its marks and restores each pod's previous
deletion cost. A namespace-scoped install that reads cluster policy from a
separate policy namespace needs a restart for the edit: edit, restart, wait a
cycle. Then check none are left. Both commands below read every namespace
(cluster admin); a tenant uses `-n <ns>` instead of `-A`:

```bash
kubectl get pods -A -o json | jq -r '.items[]
  | select(.metadata.annotations["llm-d.ai/utilization-share-transfer"] != null)
  | .metadata.namespace + "/" + .metadata.name'
```

**Cleanup after the fact**: the mark records the pod's deletion cost from before
the transfer as `prevCost`; restore it, or remove the cost when the mark has
none, and remove the mark (cluster admin as written; `-n <ns>` for a tenant):

```bash
kubectl get pods -A -o json \
  | jq -r '.items[]
      | select(.metadata.annotations["llm-d.ai/utilization-share-transfer"] != null)
      | [.metadata.namespace, .metadata.name,
         ((.metadata.annotations["llm-d.ai/utilization-share-transfer"] | fromjson? | .prevCost) // "none")]
      | @tsv' \
  | while read -r ns pod prev; do
      if [ "$prev" = "none" ]; then
        kubectl annotate pod -n "$ns" "$pod" \
          llm-d.ai/utilization-share-transfer- \
          controller.kubernetes.io/pod-deletion-cost-
      else
        kubectl annotate pod -n "$ns" "$pod" --overwrite \
          llm-d.ai/utilization-share-transfer- \
          "controller.kubernetes.io/pod-deletion-cost=$prev"
      fi
    done
```

A mark that is not valid JSON is treated as having no previous cost. If several
controllers with different `CONTROLLER_INSTANCE` values share the cluster, each
mark names its writer in its `instance` field; run the cleanup only for the
instances that were downgraded (or renamed), by adding that field to the
`select`. A controller with `CONTROLLER_INSTANCE` unset writes no `instance`
field at all, so match it with `== null`, not `== ""`:

```jq
# marks of the instance named old-name
select((.metadata.annotations["llm-d.ai/utilization-share-transfer"] | fromjson? | .instance) == "old-name")
# marks of a controller with CONTROLLER_INSTANCE unset
select((.metadata.annotations["llm-d.ai/utilization-share-transfer"] | fromjson? | .instance) == null)
```

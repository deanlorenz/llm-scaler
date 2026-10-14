# Scale a model on saturation

## Start here

- **Status:** Stable. The default path; every other path assumes it is in place.
- **You need:**
  - llm-d model servers already serving in the namespace (none yet? [install a small llm-d model](../../guides/install-small-model/)).
  - a Prometheus scraping the model servers and their EPP.
  - the EPP's `featureGates: [flowControl]` in its `EndpointPickerConfig`: `make check-prereqs` and `make deploy-wva` refuse without it.
  - a cluster admin, for KEDA on the cluster and `make setup-prereqs` once per namespace.
- **Do this** ([Install the scaling manager in a namespace](../../guides/install-in-namespace/)):
  0. (namespace owner) [Prerequisites](../../guides/install-in-namespace/README.md#prerequisites): `export NAMESPACE=<your-namespace>`, then `make check-prereqs` (read-only).
  1. (cluster admin) [Prepare the namespace](../../guides/install-in-namespace/README.md#1-cluster-admin-prepare-the-namespace) (`make setup-prereqs`).
  2. (namespace owner; the cluster admin when the controller runs in `wva-<ns>`) [Install the controller](../../guides/install-in-namespace/README.md#2-install-the-controller) (`make deploy-wva`).
  3. (namespace owner) [Make the workloads safe to scale](../../guides/install-in-namespace/README.md#3-make-the-workloads-safe-to-scale): `make workload-patch` writes a patch for you to apply through your modelservice chart; it changes nothing on its own ([writing the patch](../../reference/workload-preparation.md#writing-the-patch-make-workload-patch)).
  4. (namespace owner) [Register the workloads](../../guides/install-in-namespace/README.md#4-register-the-workloads) (`make scaledobjects-plan`, edit the plan, `make scaledobjects-apply`).
  5. (namespace owner) Confirm only KEDA can reach the scaler's port `:9090`, which is plaintext and unauthenticated by default: `make verify-deployment` reads the `wva-external-scaler` NetworkPolicy that step 1 writes when it knows KEDA's namespace. If it prints `No NetworkPolicy: scaler port open`, the cluster admin re-runs step 1 in the install's shape with `KEDA_NAMESPACE=<namespace KEDA runs in>` (the warning prints the exact command), or you serve the scaler over mutual TLS ([network exposure](../../reference/security-model.md#network-exposure)). A policy you cannot read is reported as not checked, not as open.
  One controller for every namespace instead (cluster admin): [install cluster-wide](../../guides/install-cluster-wide/).
- **Check it works:** `kubectl get scaledobject,hpa -n $NAMESPACE` shows `READY True` and a number under `TARGETS`; a few minutes later the controller log has `scaling-decision` lines ([Verification](../../guides/install-in-namespace/README.md#verification)). The controller runs in `$NAMESPACE`, or in `wva-$NAMESPACE` when the admin keeps it out of your reach.
- **Undo:** (namespace owner; the cluster admin when the controller runs in `wva-<ns>`) `make undeploy-wva` ([Cleanup](../../guides/install-in-namespace/README.md#cleanup)); the model servers keep running. ScaledObjects you adopted are left in place and listed for you to repoint or remove. Cluster-scoped objects the admin created stay until the admin removes them ([what an uninstall run by you leaves behind](../../guides/install-in-namespace/README.md#what-an-uninstall-run-by-you-leaves-behind)).

## What it is

The default path, and the one every other path builds on. The scaling manager reads what your
inference engines are actually doing — KV-cache occupancy, queues, token rates —
turns it into a supply-and-demand statement per model, and answers KEDA with a
replica count. KEDA actuates it.

**Use it when** a model's load varies and a hand-set replica count is either
wasting accelerators at the trough or missing SLO at the peak, and when the
thing that saturates first is the engine.

**Do not use it when** the bottleneck is somewhere the scaling manager cannot see — a gateway,
a tokenizer service, the network — or when nothing scrapes vLLM/SGLang and EPP
metrics into Prometheus. The scaling manager has no opinion it did not read from a metric.

## What it needs

- KEDA on the cluster, and Prometheus scraping the model servers and the EPP.
- A KEDA `ScaledObject` per workload. This is not optional: the controller has
  no watch and no listing, so until a ScaledObject exists it is running and idle.
- Nothing else. The scaling manager holds no accelerators of its own.

## Setting it up

[Install the scaling manager in a namespace](../../guides/install-in-namespace/) covers the
whole sequence. The two steps that matter here are the last two:

```bash
make scaledobjects-plan     # lists your model servers; applies nothing
make scaledobjects-apply    # registers them — this is what makes it scale
```

`scaledobjects-plan` writes an editable plan first, so you see what would be
autoscaled before anything is.

## Verifying it

Three things, in this order. `$NAMESPACE` is the namespace the models run in.
The controller runs there too in the default namespace install; in
`wva-$NAMESPACE` when the admin keeps it out of your reach; and in
`workload-variant-autoscaler-system` (by default) for a cluster-wide install.

```bash
export NAMESPACE=<your-namespace>
WVA_NS=$NAMESPACE   # the controller's namespace: wva-$NAMESPACE out of reach, workload-variant-autoscaler-system cluster-wide

# 1. The scaling manager is deciding at all: one line per model, per cycle
kubectl logs -n "$WVA_NS" deploy/wva-controller-manager | grep scaling-decision

# 2. It decided for a reason: supply, demand and the threshold it compared them to
kubectl logs -n "$WVA_NS" deploy/wva-controller-manager | grep analyzer-result

# 3. KEDA acted on it
kubectl get scaledobject,hpa -n "$NAMESPACE"
```

The same decision, as a metric in Prometheus (PromQL takes the namespace
literally, so write the value of `$NAMESPACE`):

```promql
wva_desired_replicas{exported_namespace="<your-namespace>"}
```

A target lower than the demand implies is usually not a bug: a limiter clamps
what the optimizer may ask for, and the reason is reported — see [bounding a
fleet by real GPUs](../bound-by-gpus/). Different thresholds per class of
workload are [tiers](../workload-classes/).

The log lines' fields are described in [the cycle log](../../reference/cycle-log.md);
`exported_namespace` is the workload's namespace and plain `namespace` is the
controller's, which is [why alerts group by the former](../../reference/monitoring.md).

## Is my model scaled right?

Four checks, cheapest first. `<modelID>` is the trigger's `modelID`, which must
equal the engine's `--served-model-name`.

1. **The engine series exist for this model.** Everything else depends on it:

   ```promql
   count by (model_name) (vllm:kv_cache_usage_perc{model_name="<modelID>"})
   # SGLang: count by (model_name) (sglang:token_usage{model_name="<modelID>"})
   ```

   An empty result means the scaling manager is blind to this model. It says so
   with a `MetricsUnavailable` Warning Event on the ScaledObject
   (`kubectl get events -n "$NAMESPACE" --field-selector reason=MetricsUnavailable`).
   Fix the scrape (ServiceMonitor/PodMonitor reaching the engine's `/metrics`)
   or the name (`--served-model-name` vs `modelID`) before tuning anything.
2. **Utilization sits inside the band.** In the `analyzer-result` line compare
   `util` with `scaleUpThreshold` and `scaleDownBoundary` (the same line carries
   both). Under steady load `util` should settle between them: above
   `scaleUpThreshold` the model should be growing, below `scaleDownBoundary`
   shrinking. A `util` stuck above the threshold while replicas do not grow
   points at a ceiling (`maxReplicaCount`, a limiter), not at the analyzer.
3. **The target matches what runs.**

   ```promql
   wva_desired_replicas{exported_namespace="<your-namespace>", variant_name="<scaledobject>"}
     -
   wva_current_replicas{exported_namespace="<your-namespace>", variant_name="<scaledobject>"}
   ```

   A gap that persists beyond the HPA's scale-down window (300 s as generated)
   or a pod's start time means KEDA/HPA is not acting: check
   `kubectl get scaledobject,hpa -n "$NAMESPACE"` for `READY`/`ACTIVE` and the HPA's
   conditions.
4. **Latency is what you wanted.** TTFT from the engine's own histogram:

   ```promql
   histogram_quantile(0.9, sum by (le) (rate(
     vllm:time_to_first_token_seconds_bucket{model_name="<modelID>"}[5m])))
   ```

   TTFT too high with `util` inside the band means the band is too high for
   this model: move it to a lower [tier](../workload-classes/#choosing-a-tier).
   TTFT well under target with many replicas means the band can go up.

## What it costs

No accelerators. The controller is a small deployment, and the recurring cost is
the Prometheus queries it makes each cycle — one set per namespace, not per pod.

## How it is tested

- End-to-end: `test/e2e/saturation_v2_test.go` (`Saturation V2 engine`),
  `saturation_analyzer_path_test.go`, `saturation_config_test.go`,
  `external_scaler_keda_test.go`, `smoke_keda_test.go`.
- Benchmark scenario: `hack/benchmark/scenarios/guides/workload-autoscaling.yaml`,
  driven by [Benchmark the scaling manager](../../guides/benchmarking/); the harness's own start-path settings are in [Replica start time in the harness](../../guides/benchmarking/README.md#replica-start-time-in-the-harness).
- The recorded numbers in
  [benchmark-results.md](../../developer-guide/benchmark-results.md) were
  measured on upstream WVA v0.6.0's earlier saturation engine. They are a historical
  baseline; they do not describe this engine.

## Tuning it

Thresholds, analyzer selection and policy tiers are in the
[scaling policy reference](../../reference/scaling-policy.md). What the engine
measures, and how a measurement becomes a replica count, is in
[the steady-state engine](../../concepts/steady-state-engine.md).

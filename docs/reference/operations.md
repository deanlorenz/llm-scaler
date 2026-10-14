# After the install

Verifying the scaling manager works, and the first things to check when it does not.

> Part of the [deployment guide](../../deploy/).

This page is the operational entry point. Longer subjects have their own pages:

- **[Watching what the scaling manager decides](monitoring.md)** -- the dashboard,
  the metrics that answer specific questions, and reading the logs.
- **[Preparing a workload to be scaled](workload-preparation.md)** -- the model
  cache, draining before scale-down, `make workload-patch`, and the rest of the
  start path (`make prepull`, `make weights`).
- **[Troubleshooting](troubleshooting.md)** -- from a symptom to its runbook.
- **[Upgrade and uninstall](upgrade-and-uninstall.md)** -- pinning a version,
  what a restart costs, and what an uninstall leaves behind.

## Verifying the install

Every command on this page uses `$NS` for the namespace the **controller** runs
in — not a fixed name, since a per-team or cluster-scoped install can be
anywhere.

Start by running:

```bash
make verify-deployment
```

It reports the namespace it found the controller in ("WVA controller is
running and ready in `<ns>`"), or — if there is more than one scaling-manager
install on this cluster, or none — lists every candidate and stops rather than
guess; choose yours from that list.

Set `NS` to the controller's namespace. In a fresh shell, find it the way the
controller check does, by its Deployment label:

```bash
kubectl get deploy -A -l app.kubernetes.io/name=workload-variant-autoscaler \
  -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name
NS=$(kubectl get deploy -A -l app.kubernetes.io/name=workload-variant-autoscaler \
  -o jsonpath='{.items[0].metadata.namespace}')
```

With more than one install the second command takes the first one listed: pick
yours from the first command's list and set `NS` by hand. A tenant without
cluster-wide read sets it directly: `NS=<your-namespace>` for a
namespace-scoped install (the controller runs in the namespace it manages), or
the `WVA_NS` it was installed with. `MODEL_NS` below is the namespace your
workloads run in, the same as `NS` for a namespace-scoped install:

```bash
MODEL_NS="$NS"   # or the namespace holding your ScaledObjects
```

Confirm registration matches what is actually serving:

```bash
make verify-scaledobjects
```

For more detail, straight from the controller and KEDA:

```bash
kubectl get pods -n "$NS"                     # the controller is Running
kubectl get scaledobject -n "$MODEL_NS"       # your managed workloads
kubectl get hpa -n "$MODEL_NS"                # KEDA created one per ScaledObject
```

The whole chain works when the ScaledObject reads `READY True` and its HPA's
`TARGETS` shows a number (`1/1 (avg)`): the scaling manager was called, decided,
and KEDA received the answer. `<unknown>` in `TARGETS` means KEDA never got one —
check the trigger's `scalerAddress` and that `modelID` is set. Do not use the
HPA's `CurrentMetrics` as the check: it is populated, with an empty entry, even
when nothing is being scaled.

If metrics are the problem, follow them forward:

```bash
# 1. the model server exposes them
kubectl port-forward -n <llm-namespace> <vllm-pod> 8000:8000
curl -s http://localhost:8000/metrics | grep vllm:

# 2. Prometheus scrapes them  (query vllm:num_requests_running)
kubectl port-forward -n <monitoring-namespace> svc/kube-prometheus-stack-prometheus 9090:9090

# 3. the scaling manager reads them and decides
kubectl logs -n "$NS" -l app.kubernetes.io/name=workload-variant-autoscaler | grep scaling-decision
```

Step 3 also shows in two always-on gauges: `wva_metrics_pods_discovered` (pods
found per model, should be > 0) and `wva_metrics_freshness_status{status="fresh"}`
(should equal the pod count). See [Metrics health](metrics.md).

## Testing autoscaling

One command, against what you already installed:

```bash
make benchmark-smoke NAMESPACE=<namespace>
```

Symmetric 1024-in/1024-out load at 15 req/s for five minutes, then a snapshot of
the dashboard over exactly that window. It checks the whole chain first — KEDA, a scaling-manager
controller that manages *this* namespace, model servers, an EPP, a ScaledObject
— and reports every gap at once rather than one per five-minute run. It stands
nothing up, so it is safe against a live namespace, and it needs no benchmark
CLI. The snapshot is written only when there is a dashboard to snapshot.

For runs whose numbers you intend to compare, use
[Benchmark the scaling manager](../guides/benchmarking/). Full procedures, including the
simulator and the e2e suites, are in [Testing](../developer-guide/testing.md).

## First-line troubleshooting

| symptom | most likely cause | check |
| --- | --- | --- |
| The scaling manager pod is not `Running` | image pull, resources, or Prometheus unreachable | `make verify-deployment`, then `kubectl describe pod -n "$NS" -l app.kubernetes.io/name=workload-variant-autoscaler` |
| a `MetricsUnavailable` Warning Event on the ScaledObject | the model's metrics stopped arriving: usually the ServiceMonitor or PodMonitor does not select the model pods, so the series never reach Prometheus | `kubectl get events -n <ns> --field-selector involvedObject.kind=ScaledObject,reason=MetricsUnavailable`, then `make verify-deployment`, `kubectl get servicemonitor,podmonitor -n <ns>` and Prometheus `/targets`. See [Metrics health](metrics.md#the-metricsunavailable-event) |
| HPA exists but its `TARGETS` reads `<unknown>` | KEDA never got an answer — usually the trigger's `scalerAddress` or a missing `modelID` | `kubectl get scaledobject,hpa -n <ns>`, then `kubectl describe hpa -n <ns> keda-hpa-<so-name>` |
| nothing scales, no errors | a limiter is declared and the workload's accelerator does not resolve, so it gets no GPU budget | `kubectl logs -n "$NS" -l app.kubernetes.io/name=workload-variant-autoscaler \| grep -i accelerator` |
| a model never wakes from zero | the EPP flow-control queue is not reaching the scaling manager | see [Troubleshooting](troubleshooting.md) |
| `READY False` on the ScaledObject, and the HPA's `TARGETS` reads `cpu: <unknown>/80%` | KEDA could not fetch the metric spec from the scaling manager, so it fell back to a CPU metric. The trigger names a scaler it cannot reach — most often a `scalerAddress` naming a **different install's namespace** than the controller actually runs in. `make scaledobjects-repoint` fixes exactly this: it rewrites `scalerAddress` on objects that ask for the scaling manager but name a namespace where no scaler runs, and leaves one pointing at a second live install alone | `kubectl get scaledobject -n <ns> -o custom-columns=NAME:.metadata.name,ADDR:.spec.triggers[0].metadata.scalerAddress`, then `kubectl get svc -n "$NS" wva-external-scaler` (the address must be `wva-external-scaler.$NS...`). Admin, across the cluster: the same with `-A` |
| demand looks far too low for the load you are driving, and an `UnattributedReadyPods` Warning Event (`has N ready pod(s) but none attributed`) keeps recurring | FMA is in the namespace and nothing is scraping its launcher pods, so the traffic they serve is invisible | `kubectl get events -n <ns> --field-selector reason=UnattributedReadyPods`, then `make verify-fma`, see also [FMA launcher pods](#fma-launcher-pods) |
| The scaling manager applies no decisions for a workload, silently, though the HPA reads a healthy ratio the whole time | the ScaledObject's `modelID` no longer matches what the container actually serves — a hand-changed model that nothing re-syncs | `make verify-scaledobjects` |
| `check-prereqs`/`deploy-wva` refuses: "No llm-d found in `<ns>`" | the namespace holds no llm-d model servers yet | finish the llm-d install in `<ns>`, or `SKIP_CHECKS=true` |
| refuses: "Monitoring not enabled for your llm-d servers; please enable Model-server metrics" | nothing scrapes the model servers — the scaling manager would hold every workload at `minReplicas` and say so only in its log | `kubectl apply -n <ns> -k config/modelserver-metrics` (or llm-d's own `guides/recipes/modelserver/components/monitoring`) |
| refuses: "Router monitoring not enabled; please enable EPP metrics" | nothing scrapes the EPP — the throughput analyzer has no arrival-rate signal (both the flow-control enqueue counter and `scheduler_attempts_total` read 0) | from an llm-d checkout: `kubectl apply -n <ns> -k guides/recipes/observability` |
| refuses: "Router queue not enabled; please turn on flow control for EPP" | the `flowControl` feature gate is off — scale-from-zero and `wva_unmeasured_queue` have no queue-depth signal to read | add `featureGates: [flowControl]` to the EPP's `EndpointPickerConfig`; llm-d ships a ready values file at `guides/workload-autoscaling/keda-epp-queue/<guide>/router.values.yaml` |

### `no children to pick from` after a reinstall

KEDA's gRPC client backs off on a name that did not resolve, and keeps backing
off for far longer than an uninstall/reinstall takes. So a ScaledObject that
outlived an uninstall can stay `READY False` against a scaler that is now
running perfectly — the name was NXDOMAIN while the scaling manager was gone,
and KEDA has not re-resolved it yet.

Restart the KEDA operator (cluster admin: KEDA is shared by every tenant):

```bash
kubectl rollout restart deploy/keda-operator -n "${KEDA_NAMESPACE:-keda-system}"
```

`keda-system` is the installer's default (`KEDA_NAMESPACE`), and what the
commands on this page assume. A KEDA the cluster already had may live
elsewhere; find it with `kubectl get pods -A -l app.kubernetes.io/name=keda-operator`
(admin).

Verified on kind: the ScaledObjects went `READY True` within a poll interval of
the restart, with nothing else changed. Deleting the ScaledObjects before
uninstalling avoids it — which `make undeploy-wva` now does for the ones it
created. See [Upgrade and uninstall](upgrade-and-uninstall.md).

### FMA launcher pods

A namespace running Fast Model Actuation needs two things the scaling manager
does not do by default, and both fail silently when missing:

- **the launcher pods must be scraped.** They declare no container ports, so a
  PodMonitor selecting by port name generates no target for them at all — not a
  failing target, no target. Fix with
  `kubectl apply -k config/fma-launcher-metrics -n <ns>`.
- **the plan must target the requester**, not a decode Deployment, when the
  requester is the only serving workload there.

Symptoms: demand far lower than the load you are driving, a recurring
`UnattributedReadyPods` Warning Event (`has N ready pod(s) but none
attributed`), or a variant flat at `minReplicaCount` while the queue grows. The
text is the Event's, not a log line:

```bash
kubectl get events -n <ns> --field-selector reason=UnattributedReadyPods
```

The whole story — how attribution works, how to size `maxReplicas` from the
launcher pool, why GPU accounting is a lower bound, and what to check — is in
[the FMA post-mortem](../proposals/fma-post-mortem.md).

First stop for any of these:

```bash
kubectl logs -n "$NS" -l app.kubernetes.io/name=workload-variant-autoscaler --tail=200
```

Deeper diagnosis — EPP metrics, scale-from-zero, slow scale-up — is in
[Troubleshooting](troubleshooting.md).

## Changing policy safely

Thresholds, tiers, scale-to-zero and limiters live in the scaling-policy
ConfigMap, `wva-scaling-policy-config` in the controller's namespace. The
controller re-reads it live; no restart. Cluster limits an admin publishes live
in the same-named ConfigMap in the policy namespace (`wva-policy` by default),
and editing that one is a cluster-admin action. The exception: a
namespace-scoped controller does not watch a separate policy namespace, so it
reads that policy once at start. After editing it, restart each such
controller (`kubectl rollout restart deploy/wva-controller-manager -n <its ns>`);
a cluster-scoped controller picks the edit up live.

1. **Back up** (strip the fields that would block a later replace):

   ```bash
   kubectl get configmap wva-scaling-policy-config -n "$NS" -o yaml \
     | yq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.managedFields)' \
     > policy-backup.yaml
   ```

2. **Edit**:

   ```bash
   kubectl edit configmap wva-scaling-policy-config -n "$NS"
   ```

3. **Confirm it took**, within a cycle:

   ```bash
   kubectl logs -n "$NS" -l app.kubernetes.io/name=workload-variant-autoscaler --since=5m \
     | grep -E 'Updated global scaling policy from ConfigMap|Loaded cluster policy from the policy namespace|GPU limiter \(re\)built from config|Effective scaling policy'
   ```

   | line | means |
   | --- | --- |
   | `Updated global scaling policy from ConfigMap` | the controller's own ConfigMap was read; `entries` is how many tiers parsed |
   | `Loaded cluster policy from the policy namespace` | the admin's policy ConfigMap was read; carries `limiters` and `limiterMode` |
   | `GPU limiter (re)built from config` | a `limiters:` change took effect |
   | `Effective scaling policy` | which tier a model resolved to |

   No line at all means the edit did not apply. Look for an error in the same
   log window; a malformed block is the usual cause.

4. **Roll back** if it misbehaves:

   ```bash
   kubectl replace -f policy-backup.yaml
   ```

These settings are read **only at startup**; changing them needs
`kubectl rollout restart -n "$NS" deploy/wva-controller-manager`:

| setting | where |
| --- | --- |
| Prometheus URL, TLS and token | the `config.yaml` key of the `wva-manager-config` ConfigMap, or the Deployment's env |
| `GLOBAL_OPT_INTERVAL` (the cycle length), feature flags, `LEARNED_STATE_*`, the Prometheus cache | same |
| enabling or disabling the throughput analyzer | scaling policy; the controller records a `ThroughputAnalyzerRestartRequired` Warning Event on the ConfigMap |

The full split is in [Configuration](configuration.md#what-a-change-needs-a-restart-or-not).
A restart has its own cost; see [Upgrade and uninstall](upgrade-and-uninstall.md#after-every-upgrade-or-restart).

## Command cheatsheet

```bash
# === llm-scaling-manager Controller ===
kubectl get pods -n "$NS"
kubectl logs -n "$NS" -l app.kubernetes.io/name=workload-variant-autoscaler -f
kubectl describe deployment wva-controller-manager -n "$NS"

# === Managed workloads (a ScaledObject IS the registration) ===
kubectl get scaledobject,hpa -n <namespace>
kubectl describe scaledobject <name> -n <namespace>
kubectl describe hpa keda-hpa-<name> -n <namespace>
kubectl get events -n <namespace> --field-selector involvedObject.kind=ScaledObject
kubectl get scaledobject,hpa -A                       # (admin) every namespace

# === Metrics and Monitoring ===
kubectl get servicemonitor,podmonitor -n <namespace>
kubectl get servicemonitor,podmonitor -A              # (admin)
kubectl port-forward -n <monitoring-namespace> svc/kube-prometheus-stack-prometheus 9090:9090

# === KEDA (admin; keda-system unless KEDA_NAMESPACE said otherwise) ===
kubectl get pods -n "${KEDA_NAMESPACE:-keda-system}"
kubectl logs -n "${KEDA_NAMESPACE:-keda-system}" -l app=keda-operator

# === vLLM / Application ===
kubectl get pods -n <app-namespace>
kubectl logs -n <app-namespace> <vllm-pod>
kubectl port-forward -n <app-namespace> <vllm-pod> 8000:8000

# === Configuration ===
kubectl get configmap -n "$NS"
kubectl get configmap wva-manager-config -n "$NS" -o yaml          # Prometheus URL, intervals
kubectl get configmap wva-scaling-policy-config -n "$NS" -o yaml   # thresholds, tiers, limiters
```

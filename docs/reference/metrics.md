# Metrics Health Monitoring

## Overview

llm-scaling-manager validates that the vLLM metrics it needs are actually
arriving before it decides anything, and degrades predictably when they are
not. This page covers what that judgement is, where you can see it, and what
the controller does when a variant's metrics are missing or stale. It is the
answer to "scaling looks stuck and I do not know whether the problem is the
decision or the data".

> **Where to look.** The judgement is recorded as two conditions on the
> variant object the controller builds in memory each cycle. That object is
> **not** a Kubernetes resource: this project installs no CRD, so there is
> nothing to `kubectl get`. The conditions reach you through the controller's
> logs and through the health gauges in
> [Monitoring](monitoring.md#the-metrics-that-answer-specific-questions) — start there.

## Status Conditions

Two conditions are set per variant, per cycle
(`internal/variant/types.go`).

### 1. MetricsAvailable

Indicates whether vLLM metrics are available from Prometheus for the variant.

**Status Values:**
- `True`: Metrics are available and up-to-date
- `False`: Metrics are missing, stale, or Prometheus query failed

**Reasons:**
- `MetricsFound`: Metrics successfully retrieved and up-to-date
- `MetricsMissing`: No vLLM metrics found (likely ServiceMonitor misconfiguration)
- `MetricsStale`: Metrics exist but are outdated (>5 minutes old)
- `PrometheusError`: Error querying Prometheus API

### 2. OptimizationReady

Indicates whether the optimization engine can run successfully.

**Status Values:**
- `True`: Optimization completed successfully
- `False`: Optimization cannot run or failed

**Reasons:**
- `OptimizationSucceeded`: Optimization completed and replicas calculated
- `OptimizationFailed`: Optimization engine failed
- `MetricsUnavailable`: Cannot optimize without valid metrics

## Seeing the condition an operator can act on

Three gauges carry the same information to a dashboard or an alert, and unlike
the conditions they are queryable:

```promql
# Are the pods behind each model being found at all?
wva_metrics_pods_discovered

# How many of them were decided on with fresh data this cycle?
wva_metrics_freshness_status{status="fresh"}

# Anything sitting in stale or missing is a MetricsAvailable=False variant.
wva_metrics_freshness_status{status!="fresh"} > 0
```

`wva_metrics_collection_errors_total` rising alongside them points at
`PrometheusError` rather than at a ServiceMonitor gap. The full catalogue,
including what each one should read on a healthy fleet, is in
[Monitoring](monitoring.md).

The condition's own message text, which names the likely cause, appears in the
controller log for the cycle that set it:

```bash
kubectl logs -n $NS -l app.kubernetes.io/name=workload-variant-autoscaler \
  | grep -iE 'MetricsMissing|MetricsStale|PrometheusError'
```

A typical message:

```text
No vLLM metrics found for model 'meta-llama/Llama-3-8b' in namespace 'default'. Ensure:
1. ServiceMonitor is created in the monitoring namespace
2. ServiceMonitor selector matches vLLM service labels
3. vLLM pods are running and exposing /metrics endpoint
4. Prometheus is scraping the monitoring namespace
```

## Graceful Degradation

When metrics are unavailable, the scaling manager implements graceful degradation:

1. **Skips optimization** for affected variants (no scaling decisions)
2. **Maintains current replica count** (doesn't scale to zero or make random changes)
3. **Records the condition** with an actionable message
4. **Continues monitoring** and retries on next reconciliation interval
5. **Other variants continue to optimize** if their metrics are available

The second point is the one to rely on: a variant whose metrics disappear holds
the replica count it had. It does not fall back to a default, and it does not
park.

## Architecture

### Metrics Validation Flow

```text
1. Controller reconciles each discovered variant
2. For each variant:
   a. Validate metrics availability
   b. Set MetricsAvailable condition
   c. If metrics unavailable:
      - Set OptimizationReady=False
      - Skip optimization (graceful degradation)
      - Continue to next variant
   d. If metrics available:
      - Collect metrics
      - Continue with optimization
3. Run optimization for all variants with valid metrics
4. Set OptimizationReady condition based on optimization result
```

### Key Components

- **`internal/collector`**: queries Prometheus and reports per-pod freshness
- **`variant.SetCondition()`** (`internal/variant/types.go`): sets a condition on
  the in-memory variant object
- **`internal/metrics`**: publishes the gauges above, which are the operator-facing
  form of the same judgement

## Best Practices

1. **Alert on `wva_metrics_freshness_status{status!="fresh"} > 0`** rather than
   reading conditions
2. **Set up alerts** for prolonged MetricsAvailable=False conditions
3. **Review condition messages** in the controller log for troubleshooting guidance
4. **Validate ServiceMonitor** configuration during initial deployment
5. **Test metrics flow** before relying on the scaling manager for production autoscaling

## Related Documentation

- [Monitoring](monitoring.md) — the full metric catalogue and what each should read
- [Prometheus Integration (Custom Metrics)](prometheus.md)
- [ServiceMonitor Configuration](../../config/base/monitoring/servicemonitor.yaml)

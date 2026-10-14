# ScaledObject trigger metadata

A model is registered with the scaling manager by a KEDA `ScaledObject` whose
trigger is `type: external-push` and points at the scaling manager's external
scaler. KEDA hands the trigger's `metadata` map to the scaling manager on every
call, so the keys on this page are the whole per-workload configuration
surface: there are no annotations to set and nothing else is read from the
ScaledObject or the workload for these values.

The keys are parsed in `internal/registry/scalermeta.go`. KEDA metadata is a map
of strings, so quote every value (`"10.0"`, `"6000"`).

## A complete example

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: qwen-decode-wva
  namespace: my-models
spec:
  scaleTargetRef:
    apiVersion: apps/v1                      # leaderworkerset.x-k8s.io/v1 for an LWS
    kind: Deployment                         # or LeaderWorkerSet
    name: qwen-decode
  pollingInterval: 5
  cooldownPeriod: 30
  minReplicaCount: 1
  maxReplicaCount: 10
  advanced:
    restoreToOriginalReplicaCount: true
    horizontalPodAutoscalerConfig:
      behavior:                              # see "Scaling behavior" below
        scaleUp:
          stabilizationWindowSeconds: 0
          policies:
            - type: Percent
              value: 100
              periodSeconds: 5
        scaleDown:
          stabilizationWindowSeconds: 300
          policies:
            - type: Percent
              value: 100
              periodSeconds: 120
  triggers:
    - type: external-push
      name: wva-external-scaler
      metadata:
        scalerAddress: wva-external-scaler.<controller-namespace>.svc.cluster.local:9090
        modelID: "Qwen/Qwen3-32B"            # = the engine's --served-model-name
        variantCost: "10.0"
        # scalingPolicy: "interactive"       # only once that tier exists in the ConfigMap
        defaultOutputTokens: "6000"          # long generations; seed only
```

## Model keys

These appear on a ScaledObject that scales a model server.

| Key | Required | Default | Validation | Read by | What it does |
| --- | --- | --- | --- | --- | --- |
| `modelID` | yes, unless `warmPoolName` is set | — | non-empty | every engine; it is the grouping key | Names the model this workload serves. Must equal the engine's `--served-model-name` (else `--model`), because engine metrics are matched on `model_name`. ScaledObjects with the same `modelID` in one namespace are variants of one model and are sized against each other. |
| `variantCost` | no | `"10.0"` | a number, `>= 0` | cost-aware optimizer; scale-from-zero variant choice | Price of **one replica** of this variant, relative to the model's other variants: GPUs per replica × relative GPU price. On scale-up the optimizer grows the variant with the lowest cost per unit of capacity; on scale-down it sheds the most expensive variant first. With one variant per model it changes nothing. See [accelerator variants](../well-lit-paths/accelerator-variants/). |
| `scalingPolicy` | no | the `default` entry's `defaultPolicy`, else the `default` entry | any string | steady-state engine policy resolution | Names a policy **tier** in the scaling-policy ConfigMap. A name with no matching entry falls back to the default entry and logs `Scaling policy not found; this variant is scaling under the default entry.` Variants of one model naming different tiers: the lexicographically smallest wins. See [tiers](../well-lit-paths/workload-classes/#choosing-a-tier). |
| `defaultOutputTokens` | no | built-in 512 | whole number `>= 0`; `"0"` means unset | steady-state engine (demand floor) | Seeds how many tokens one request generates, so a router queue is priced before any replica has completed a generation. Replaced by the fleet's own measurement as soon as one exists, and also outranked by the output length the fleet last recorded, which is carried across a gap in readings: the seed is used only when the fleet has neither. Across a model's variants the **largest** wins (set it on the decode trigger under P/D). See [expected output length](scaling-policy.md#expected-output-length). |
| `warmPool` | no | the namespace's only pool | any string | warm-pool controller | Which warm pool this model may borrow from. Needed only when the namespace has more than one pool; with several pools and none named, the model gets no warm copy and the controller logs why. |
| `warmPoolCopies` | no | absent = automatic | whole number `>= 0` | warm-pool controller | How many warm copies of this model the pool holds. Absent: the pool ranks it and holds at most one. `"0"`: never warm it. `"1"`: always keep one. `"N"`: keep N, so N scale-ups can bridge at once. |
| `replicaStartSeconds` | no | — | — | **nothing yet** | **Reserved.** Declared as the seed for how long a replica takes to become Ready, but not read by any code path today. The analyzer uses its own default until it measures a replica start. Setting it has no effect. |

`scalerAddress` is KEDA's own key (where to dial); the scaling manager never
reads it. Its value is `wva-external-scaler.<controller-namespace>.svc.cluster.local:9090`.

**A bad value freezes the model at its current count.** If `modelID` is empty or
`variantCost`, `warmPoolCopies` or `defaultOutputTokens` does not parse, the
workload is skipped entirely (`internal/utils/variant.go`): the scaling manager
never plans it, so it never makes a decision for it, and `GetMetrics` keeps
refusing with `Unavailable` ([before the first
decision](#before-the-first-decision-and-the-fallback)) for as long as the typo
stands. The HPA holds whatever count the workload had, under any load. KEDA
records the trigger error on the ScaledObject, with the message
`WVA has not published a decision for this target yet`, and the controller logs
`external scaler GetMetrics refused: no decision yet` at the default verbosity.
The cause is a controller log line at verbosity 4, which the default
verbosity (2) hides: add `--v=4` to the controller Deployment's `args` (see
[configuration](configuration.md)) and look for
`Skipping a registered workload with unusable trigger metadata`, with the
ScaledObject in `scaledObject` and the key and value in `error`. If a
registered model never gets a decision, check these values first.

## Warm-pool keys

These appear only on the ScaledObject that scales a warm **pool** Deployment.
`warmPoolName` marks the trigger as a pool's; with it set, no `modelID` is
needed and the model keys above are ignored. `deploy/warmpool.sh create` writes
these for you; an unset key keeps the controller's flag default. Any value that
does not parse makes the whole pool trigger refused rather than defaulted.

| Key | Validation | Written by `warmpool.sh create` | What it does |
| --- | --- | --- | --- |
| `warmPoolName` | non-empty | always | Declares this ScaledObject as warm pool `<name>`. |
| `warmPoolSleepMinSize` | whole number `>= 0` | `--reserve` (default 1) | Free pods kept in reserve. `maxReplicaCount` must exceed it, or the pool can never warm anything. |
| `warmPoolMaxHold` | positive duration (`5m`) | `--max-hold` | Longest a bridge pod is lent before it is reclaimed. |
| `warmPoolMinHold` | duration `>= 0` (`90s`) | `--min-hold` | Shortest a bridge pod is lent, so it is not returned the instant the new replica is Ready with a cold cache. Capped by `warmPoolMaxHold`. |
| `warmPoolRetained` | `true`/`false` | `--type retained` / `bridge` | Makes the pool serving capacity rather than a bridge: no hold timeout. See [retained pool](../well-lit-paths/retained-pool/). |
| `warmPoolSwitchSpareThreshold` | percent, 0–100 | no | Retained pool only: spare capacity, as a percent of a model's supply, below which the pool switches to that model. |
| `warmPoolMinSwitchInterval` | duration `>= 0` | no | Retained pool only: minimum time between two switches. Default `10m`. |
| `warmPoolPreloadTop` | whole number `>= 0` | no | Warm this many of the busiest models without waiting for each to miss first. |
| `warmPoolGPUMemoryUtilization` | fraction, `0 < x <= 1` | no | Overrides `--gpu-memory-utilization` for warm copies: less KV cache, more models per pod. |

Sizing these is covered in [the warm-pool guide](../guides/warm-pool/sizing.md).

## How the keys get onto the ScaledObject

`make scaledobjects-plan` writes an editable plan, one entry per model server,
and `make scaledobjects-apply` turns each `apply: yes` entry into a ScaledObject
(`deploy/lib/scaledobject.sh`). The plan carries `modelID`, `minReplicas`,
`maxReplicas`, `variantCost` and `scalingPolicy` (commented out unless set);
the generated trigger has `scalerAddress`, `modelID`, `variantCost` and, when
set, `scalingPolicy`. The other keys are not in the plan. Set them in one of
two ways:

- **A template for every model.** `WVA_DEFAULT_SO_TEMPLATE=<file>` replaces the
  generated shape. It substitutes `{{NAMESPACE}} {{NAME}} {{KIND}}
  {{APIVERSION}} {{MODEL_ID}} {{SCALER_ADDRESS}} {{MIN}} {{MAX}}
  {{VARIANT_COST}} {{SCALING_POLICY}}`; anything else in the file is copied
  as written. The sample is
  `config/samples/keda/external-scaler/scaledobject-template.yaml`.

  ```bash
  make scaledobjects-apply WVA_DEFAULT_SO_PLAN=wva-plan.yaml WVA_DEFAULT_SO_TEMPLATE=$PWD/my-template.yaml
  ```

- **One model, after the fact.** Patch the trigger:

  ```bash
  kubectl patch scaledobject <name> -n <ns> --type=json -p \
    '[{"op":"add","path":"/spec/triggers/0/metadata/defaultOutputTokens","value":"6000"}]'
  ```

  `scaledobjects-apply` with `apply: yes` skips a workload that already has a
  ScaledObject, so a hand-added key survives it. `apply: adopt` does not: it
  replaces the trigger list with the plan's keys, and hand-added keys must be
  patched back.

## Scaling behavior

The generator writes this HPA behavior, and it is the recommended shape for any
ScaledObject that points at the scaling manager, hand-written or adopted:

| | stabilization | policy |
| --- | --- | --- |
| scale-up | 0 s | `Percent 100` every 5 s |
| scale-down | 300 s | `Percent 100` every 120 s |

Scale-up acts on the current answer because the wait for a new replica is
already paid in its start time. Scale-down waits for 300 s of sustained lower
answers; after a restart or leader change it also guards the first decisions a
new leader makes, which rest on a short history. Removing a replica too early
costs a cold start on the next burst; keeping one too long costs only money.

Override the defaults per install with
`WVA_SO_SCALE_{UP,DOWN}_{PERIOD,STABILIZATION}`, or replace the shape with
`WVA_DEFAULT_SO_TEMPLATE`. `apply: adopt` keeps the existing ScaledObject's
`advanced` block, so an adopted object keeps whatever behavior it had; check it.

### Before the first decision, and the fallback

After every controller restart and every leader change, the new leader has no
decision until its first cycle completes. **Until then it refuses KEDA's
`GetMetrics` with `Unavailable`** (`WVA has not published a decision for this
target yet`), the same answer a standby gives. KEDA records a trigger error and
the HPA holds the replica count it has; nothing scales down on a number nobody
decided. `IsActive` and the push stream are unaffected: before the first
decision they report the target active if it has replicas.

The generator also writes a KEDA `fallback` into every ScaledObject:

```yaml
fallback:
  failureThreshold: 3              # WVA_SO_FALLBACK_FAILURES
  replicas: <minReplicaCount>
  behavior: currentReplicasIfHigher
```

After `failureThreshold` failed polls in a row, KEDA stops passing the error on
and feeds the HPA `max(current replicas, replicas)` instead. Since `replicas` is
the object's own `minReplicaCount`, that is the current count: the fallback
never scales anything up or down. What it changes is that the HPA keeps
recording a recommendation while the scaler is away, so the 300 s scale-down
window still covers the moment the scaler returns, and the ScaledObject's
`Fallback` condition says it is in effect. A fleet does not shed replicas on the
first answer after an outage longer than the window.

`behavior` needs KEDA 2.17 or later (the installer pins 2.19); with an older
KEDA, set `WVA_SO_FALLBACK=false`. `apply: adopt` adds the same fallback to an
adopted ScaledObject that has none, and leaves one that has a fallback exactly
as it is. The template sample carries the same block.

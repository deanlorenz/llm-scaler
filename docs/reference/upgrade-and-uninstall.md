# Upgrade and uninstall

How to upgrade the controller safely, what a restart costs, and what an
uninstall removes and leaves behind. `$NS` is the namespace the controller runs
in.

> Part of the [deployment guide](../../deploy/). Day-to-day checks are in
> [After the install](operations.md); symptoms in [Troubleshooting](troubleshooting.md).

## Pin the version

The default image is `ghcr.io/ev-shindin/llm-scaling-manager:main`. `:main` is a
**floating** tag, rebuilt on every push to `main`, and the installer pulls it
with `imagePullPolicy: Always`. Any pod restart (a node drain, an eviction, a
leader change) can start different code from what you tested. Do not run
production on it.

Pin a release tag or a digest, and deploy with the manifests from the **same**
release. An image older than the manifests crash-loops on flags it does not
know.

```bash
git checkout v0.9.0
make deploy-wva NAMESPACE=<namespace> IMG_TAG=v0.9.0
# or a digest
make deploy-wva NAMESPACE=<namespace> IMG=ghcr.io/ev-shindin/llm-scaling-manager@sha256:<digest>
# out-of-reach shape (the cluster admin runs it): the controller runs in wva-<namespace>
NAMESPACE=<namespace> make deploy-wva WVA_NS=wva-<namespace> WVA_WATCH_NS=<namespace> IMG_TAG=v0.9.0
```

`IMG_TAG` sets the tag of the default `IMG`; `IMG` replaces the whole reference.
The pull policy follows the reference: `IfNotPresent` for a digest or a `vX.Y.Z`
tag, `Always` for anything else. `WVA_IMAGE_PULL_POLICY` overrides it. Confirm
what runs:

```bash
kubectl get deploy wva-controller-manager -n "$NS" \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"  "}{.spec.template.spec.containers[0].imagePullPolicy}{"\n"}'
```

Pass every install-time variable you used the first time (`SCOPE`,
`ENVIRONMENT`, `DEPLOY_ALERTING_RULES`, ...) on every upgrade. An upgrade
re-renders the install from them. For example, leaving `DEPLOY_ALERTING_RULES`
at its default `false` deletes the alert rules. The full list is in
[Configuration](configuration.md).

The limiter is the exception. A re-run without `WVA_LIMITER` keeps the limiter
the controller's `wva-scaling-policy-config` already declares; pass `WVA_LIMITER`
only to change it. Limiters an admin published in a policy namespace
(`make enable-physical-limiter`) are never touched by `make deploy-wva`.

## Upgrade

1. **Back up your policy.** The install owns `wva-scaling-policy-config`; keep a
   copy of what you tuned
   ([Changing policy safely](operations.md#changing-policy-safely)).
2. **Check prerequisites** against the new release. It is read-only:
   ```bash
   make check-prereqs NAMESPACE=<namespace>
   # out-of-reach shape: the controller runs in wva-<namespace>
   NAMESPACE=<namespace> make check-prereqs WVA_NS=wva-<namespace> WVA_WATCH_NS=<namespace>
   ```
   A new release can need a kind or a permission the old one did not. If it
   reports missing prerequisites, a cluster admin runs, once:
   ```bash
   NAMESPACE=<namespace> make setup-prereqs        # (cluster admin)
   # out-of-reach shape
   NAMESPACE=<namespace> make setup-prereqs WVA_NS=wva-<namespace> WVA_WATCH_NS=<namespace>
   ```
   A missing **optional** prerequisite does not stop the install: the controller
   runs without the feature that needs it, and says so. For this release that
   is not enough: see [Upgrading to this release](#upgrading-to-this-release).
3. **Deploy** the pinned image, as above, in the install's shape.
   - Out-of-reach install: the cluster admin runs the
     `WVA_NS=wva-<namespace> WVA_WATCH_NS=<namespace>` form. The plain
     `make deploy-wva NAMESPACE=<namespace>` would install a second controller
     in `<namespace>` instead of upgrading the one in `wva-<namespace>`.
   - The re-run keeps the declared limiter (see above) and re-applies the rest
     of `wva-scaling-policy-config` from the release. That is why step 1 backs
     it up.
4. **Verify**, naming the namespace as you did for the install. Without it, both
   commands look in the default `workload-variant-autoscaler-system`:
   ```bash
   NAMESPACE=<namespace> make verify-deployment
   NAMESPACE=<namespace> make verify-scaledobjects
   # out-of-reach shape: the controller runs in wva-<namespace>
   NAMESPACE=<namespace> WVA_NS=wva-<namespace> WVA_WATCH_NS=<namespace> make verify-deployment
   ```

### Upgrading to this release

This release adds two cluster-scoped grants (`wva-policy-namespace-reader-*`,
`wva-kueue-reader-*`) and a namespaced Role (`wva-controller-status-role`, for
the status ConfigMap). It also moves the external-scaler NetworkPolicy into the
admin phase. `make check-prereqs` reports all three grants as **optional**, so a
tenant's upgrade goes ahead without them and the controller runs without what
they enable. Do this, in order:

1. **A cluster admin re-runs `make setup-prereqs` first**, for every
   namespace-scoped install, with the variables that install used, before the
   namespace owner runs `make deploy-wva`:
   ```bash
   NAMESPACE=<namespace> make setup-prereqs        # (cluster admin)
   # out-of-reach shape
   NAMESPACE=<namespace> make setup-prereqs WVA_NS=wva-<namespace> WVA_WATCH_NS=<namespace>
   ```
   Also pass:
   - `KEDA_NAMESPACE=<namespace KEDA runs in>`, unless the installer can find
     KEDA itself. If it cannot, **no** NetworkPolicy is applied and `:9090` is
     open to every Pod ([the scaler NetworkPolicy](#the-external-scaler-networkpolicy)).
   - `PROMETHEUS_SA=<namespace>/<serviceaccount>` (or the `PROMETHEUS_URL` the
     install first used). Without one, the re-run keeps the existing
     `metrics-reader` binding unchanged and warns which account it names
     ([Prometheus](prometheus.md)).
2. **Policy may move.** After its next restart, a controller that manages its
   own namespace can read Namespaces, so it finds a published cluster policy:
   the namespace its `wva.llmd.ai/policy-namespace` label names, else
   `wva-policy` if it exists. Its limiters and optimizer selection then come
   from there, and the `limiters:` in its own `wva-scaling-policy-config`
   **stop applying**. Thresholds, tiers and per-model entries still come from
   its own ConfigMap.
3. **`make enable-physical-limiter` and `make disable-physical-limiter` restart
   every namespace-scoped controller whose policy lives outside the namespace it
   manages**, on every run. That is a self-managed controller reading
   `wva-policy` (or its labelled namespace), or an out-of-reach one reading its
   own ConfigMap in `wva-<namespace>`. Such a controller caches only the
   namespace it manages and reads other ConfigMaps once, at startup. The plan
   the command prints marks them `WILL BE RESTARTED`. Run it when a restart is
   acceptable ([what a restart costs](#after-every-upgrade-or-restart)).
4. **What now fails closed**:
   - A Kueue ClusterQueue with a `namespaceSelector` grants a namespace its
     quota only when the selector admits it (no selector admits nothing, `{}`
     admits every namespace). If the controller cannot read the namespace's
     labels, that queue's grant is withheld, so the namespace is bounded at
     nothing by it.
   - With a Kueue quota source configured, `make deploy-wva` (the controller-only
     phase) **refuses** when the controller cannot `get` its managed Namespace,
     and names the `make setup-prereqs` run that fixes it. When it cannot tell
     (you may not impersonate the controller, or cannot read whether a Kueue
     source is configured), it **warns** with the same fix and goes ahead.
   - A NetworkPolicy that admits a namespace KEDA does not run in drops every
     KEDA call: every ScaledObject naming this scaler goes not-Ready and nothing
     scales, while the controller reports healthy.
5. **Check**:
   - `wva_policy_namespace_unverified` is `0`. `1` means the controller could
     not read its Namespace and runs on its own ConfigMap
     (alert `WVAPolicyNamespaceUnverified`).
   - `NAMESPACE=<namespace> make verify-deployment` (add
     `WVA_NS=wva-<namespace> WVA_WATCH_NS=<namespace>` for the out-of-reach
     shape) prints no NetworkPolicy warning: not "wrong KEDA namespace", not
     "port open". "Not checked" means you may not read NetworkPolicies there;
     run it as someone who can.
   - The [`wva-controller-status` ConfigMap](operations.md#what-the-controller-is-running-with)
     names the policy namespace, limiter and acceptance the controller runs with:
     ```bash
     kubectl get cm wva-controller-status -n "$NS" \
       -o jsonpath='{.data.policyNamespace} {.data.limiter} {.data.policyAccepted}{"\n"}'
     ```

#### Policy that starts to apply

Some settings in named tiers and per-model entries were ignored or replaced
before. They now apply. Before you upgrade, read your
`wva-scaling-policy-config` **and** every namespace-local scaling-policy
ConfigMap, and work through this list.

1. **Fields a tier or per-model entry leaves out.** They now come from the
   layer below (the tier, then `default`), not from built-in values.

   | field the entry leaves out | tier before | per-model before | now |
   | --- | --- | --- | --- |
   | `kvCacheThreshold` | 0.80 | 0.80 | the layer below |
   | `queueLengthThreshold` | 5 | 5 | the layer below |
   | `priority` | 1.0 | 1.0 | the layer below |
   | `scaleUpThreshold`, `scaleDownBoundary` (entry sets `analyzerName: saturation` or `analyzers`) | 0.85, 0.70 | 0.85, 0.70 | the layer below |
   | `analyzers` (entry sets `analyzerName: saturation`) | `[saturation]` | `[saturation]` | the layer below |

   To keep the old behaviour, set the old value in the entry explicitly, for
   example `kvCacheThreshold: 0.80`.

2. **Settings that were ignored in a tier or per-model entry.**

   | field | tier before | per-model before | now |
   | --- | --- | --- | --- |
   | `scaleToZero` | ignored | applied | applied; a model parks only if every tier its variants name allows it, and waits for the longest `retentionPeriod` |
   | `scaleFromZero.requirePrefill` | ignored | ignored | applied; a model waits for prefill if any of its tiers requires it |
   | `shapeChangeHoldSeconds` | ignored | ignored | applied |
   | `disableShapeChangeHold` | ignored | ignored | applied |

   Nothing reports these the first time they take effect. To keep the old
   behaviour, remove the field from the tier or per-model entry.

   Two consequences to check:

   - A tier that disables `scaleToZero` now keeps its models up, even when
     `default` enables it. `WVAModelWillNotScaleToZero` may start firing for
     those models (reason `policy-forbids-zero`). Enable `scaleToZero` on the
     tier, or accept the alert.
   - A tier or per-model `requirePrefill: true` now applies. A P/D model whose
     prefill cannot be placed stays at zero rather than waking decode alone.

   A variant that names no tier follows the variants of its model that do. If
   only the decode of a P/D model names a tier, the prefill now follows that
   tier too.

3. **`scaleToZero.retentionPeriod` values that cannot be used.** An unusable
   value (not a duration, such as `5 minutes`) used to fall back to 10m. It now
   falls back to the value of the layer below (the tier, then `default`, then
   10m), so the model may wait longer or shorter than before. Values under 30s
   are now unusable too. Fix the value. A skipped value is reported as a
   `ConfigFieldIgnored` event on the ConfigMap, in `wva_config_ignored_fields`,
   in the log, and for the `default` entry in the `policyIgnored` key of the
   [`wva-controller-status` ConfigMap](operations.md#what-the-controller-is-running-with).
   A negative `shapeChangeHoldSeconds` is skipped and reported the same way.

4. **`terminationGracePeriodSeconds` patches below 40.** The controller now
   takes up to 40s to stop: it finishes the running cycle, then releases its
   lease. Raise the patch to 40, or remove it. A patch left at 10 is no worse
   than before; it only loses the faster failover
   ([details](operations.md#what-the-controller-is-running-with)).

After the upgrade, compare the controller's `Effective scaling policy` log
lines with the ones from before. Each names a model and its
`scaleUpThreshold`, `scaleDownBoundary`, `kvCacheThreshold` and `priority`:

```bash
kubectl logs -n "$NS" deploy/wva-controller-manager | grep 'Effective scaling policy'
```

Save the same output before you upgrade, so you have something to compare.

Other changes you may notice:

- A scale-from-zero wake records one `ScaledUp` Event. It used to record one
  on every 100ms tick until KEDA scaled the target, so a dashboard that counts
  `ScaledUp` Events will show far fewer.
- A SIGTERM during startup, for example from the startup probe after about 60s
  while Prometheus is unreachable, is now logged as
  `Shutdown requested during startup` and exits 143. Previously the process was
  killed by the signal with no log line; the exit code was 143 as well.

### External scaler TLS on upgrade

Mutual TLS on the scaler port is now the default. Most installs already have
ScaledObjects, and a plain upgrade leaves those on plaintext and lists them.
To move to TLS, run:

```bash
make deploy-wva WVA_SCALER_TLS=true WVA_SCALER_TLS_MIGRATE=true
```

It moves the controller and its ScaledObjects together. KEDA's calls for each
ScaledObject may fail for a few seconds while it moves.

- **Split install:** KEDA's side must be in place first. A cluster admin runs
  `make setup-prereqs`; then the tenant runs the command above. A tenant's
  `make deploy-wva` cannot write KEDA's side, and stays on plaintext until the
  admin has.
- **GitOps:** run the command, then commit the
  [new trigger](configuration.md#external-scaler-mutual-tls) (the TLS address
  and the `authenticationRef`) to git right away. Otherwise GitOps puts the old
  one back and those ScaledObjects stop reaching the scaler.

To stay on plaintext, set `WVA_SCALER_TLS=false`. Check the result with
`make verify-deployment`.

### The external-scaler NetworkPolicy

`wva-external-scaler` admits `:9090` only from KEDA's namespace. The admin phase
(`make setup-prereqs`) writes it, and only when it **knows** where KEDA runs. In
order, it takes:

1. `KEDA_NAMESPACE`, set explicitly;
2. the namespace serving the external-metrics APIService, when that is KEDA's;
3. `KEDA_NAMESPACE`'s default with `KEDA_HELM_INSTALL=true`, when that
   APIService does not exist yet (this install puts KEDA there);
4. the namespace an existing `wva-external-scaler` policy already admits.

So a re-run with `KEDA_NAMESPACE` corrects a policy that names the wrong
namespace; with nothing to go on, a wrong policy stays wrong. When none answers,
no policy is written, the installer warns that `:9090` is open, and
`make verify-deployment` prints `No NetworkPolicy: scaler port open`.

```bash
KEDA_NAMESPACE=<keda ns> NAMESPACE=<namespace> make setup-prereqs
# out-of-reach shape
KEDA_NAMESPACE=<keda ns> NAMESPACE=<namespace> make setup-prereqs WVA_NS=wva-<namespace> WVA_WATCH_NS=<namespace>
# cluster-scoped install
KEDA_NAMESPACE=<keda ns> make setup-prereqs SCOPE=cluster WVA_NS=<controller ns>
```

What the installer leaves alone, and reports as `left as it is, state UNKNOWN`
(with `kubectl get networkpolicy wva-external-scaler -n <ns> -o yaml` to check
it):

- a policy it cannot read;
- a policy that admits `:9090` from anything other than exactly one namespace;
- any policy, when the controller-only phase (`INSTALL_PHASE=wva`) runs with
  `WVA_SCALER_NETWORKPOLICY=false`. Only the admin phase removes one.

`make check-prereqs` and the controller-only phase list the policy as optional.

- Run `make verify-deployment` with the same `NAMESPACE` (and `WVA_NS`,
  `WVA_WATCH_NS` for the out-of-reach shape) and read its NetworkPolicy lines. A
  namespace you cannot see KEDA from is reported as not confirmed, not as
  correct, and a policy you cannot read as not checked, not as open.
- Earlier versions of these docs had you apply a policy by hand, named
  `wva-external-scaler-from-keda`. NetworkPolicies add up, so it still admits
  whatever namespace it names to `:9090`, and the installer neither reads nor
  updates it. Delete it once the shipped one is in place:
  ```bash
  kubectl delete networkpolicy wva-external-scaler-from-keda -n "$NS" --ignore-not-found
  ```

## After every upgrade or restart

A new controller pod starts with no decision in memory. Until it has one:

- **Until its first cycle completes** (one `GLOBAL_OPT_INTERVAL`, 15 s by
  default, plus leader election), the scaler answers KEDA's `GetMetrics` with
  each target's current replica count. The HPA holds, and KEDA sees no error.
  `wva_scaler_undecided_answers_total{reason="no-decision"}` grows meanwhile
  and stops once the first cycle publishes.
- **While no controller pod is serving**, KEDA cannot reach the scaler. The
  generated `fallback` covers that: after more than `failureThreshold` failed
  reads in a row (3, at about one read every 15 s) KEDA holds the current
  count. A short rollout may log a `KEDAScalerFailed` Event or two and never
  reach the fallback. See [before the first
  decision](scaledobject-trigger.md#before-the-first-decision-and-the-fallback).
- **Under the utilization-share optimizer**, every planned model holds what it
  runs for one fill timeout, about three minutes with default timings, and
  shows the blocked reason `quiet-period`. Expect it on every upgrade and leader
  change.
- **What it learned is held in memory by default.** Learned capacity and
  throughput are re-learned after every restart or leader change, so a request
  shape it had already priced is sized from occupancy again until it saturates
  once more. `LEARNED_STATE_SNAPSHOT=true` carries it across instead
  ([learned state across a restart](configuration.md#learned-state-across-a-restart)).

Do not judge an upgrade by the first few minutes of decisions.

## Uninstall

Pass the **same** `ENVIRONMENT`, `SCOPE` and namespace you installed with; the
uninstall renders the same overlay and deletes what it renders.

```bash
make undeploy-wva NAMESPACE=<namespace>
```

If the controller was installed in a namespace of its own (`WVA_NS`), as in the
out-of-reach shape where it runs in `wva-<namespace>` and manages `<namespace>`,
pass both, or the uninstall looks for the controller in the wrong place:

```bash
make undeploy-wva NAMESPACE=<namespace> WVA_NS=wva-<namespace>
```

### What it removes

| object | note |
| --- | --- |
| the controller Deployment, its ServiceAccounts, Services and ServiceMonitor | |
| `wva-manager-config` and `wva-scaling-policy-config` | back up your policy first |
| the install's ClusterRoles and ClusterRoleBindings | suffixed per install, so other installs keep theirs. Cluster-scoped: a namespace owner gets Forbidden on these, and the uninstall says which are left for a cluster admin |
| the controller's Role and RoleBinding in a namespace it managed from outside (`WVA_WATCH_NS`) | |
| `wva-utilization-share-history` and `wva-controller-status` | written by the controller, not rendered, so deleted by name |
| the ScaledObjects **this installer created** | KEDA restores each workload to its pre-autoscaling replica count as they go. Keep them with `UNDEPLOY_SCALEDOBJECTS=false` |

### What it leaves

| left behind | why | clean up |
| --- | --- | --- |
| the namespace `$NS` | it may hold your model servers | `kubectl delete namespace "$NS"` only if nothing else lives there |
| ScaledObjects you **adopted** (written by you or GitOps, pointed at this scaler) | not the installer's to delete. Their trigger now calls a scaler that is gone, and **a model parked at zero never wakes**: activation only comes from the scaler | see below |
| Prometheus, KEDA, the EPP | shared; this install may not have created them | `UNDEPLOY_SHARED=true` removes them too, for everything on the cluster |
| the PrometheusRule `controller-manager-alerts` | the uninstall does not render the alerts component | `kubectl delete prometheusrule controller-manager-alerts -n "$NS"` |
| the dashboard ConfigMap `wva-operation-dashboard` | shared by every install on the cluster | (cluster admin, if it is in a shared monitoring namespace) `kubectl delete configmap wva-operation-dashboard -n <dashboard-namespace>` |
| cluster policy in the policy namespace (`wva-policy`, or the one a `wva.llmd.ai/policy-namespace` label names): the ConfigMap, Role `wva-policy-reader` and this install's RoleBinding `wva-policy-reader-<suffix>` | the ConfigMap is written by `make enable-physical-limiter`; the Role and RoleBinding by it and by `make setup-prereqs` (for a controller that manages its own namespace, when the policy namespace exists) | (cluster admin) see below |
| ClusterRoles and bindings from an install older than per-install suffixes | they may be bound by another install | (cluster admin) delete by hand once no install uses them |
| utilization-share transfer marks on pods | written while the optimizer acted | [Transfer marks left on pods](troubleshooting.md#transfer-marks-left-on-pods-after-a-downgrade); set `shadow: true` and wait a cycle **before** uninstalling to avoid them |

### Adopted ScaledObjects

The uninstall lists them. To find them later:

```bash
kubectl get scaledobject -n <ns> \
  -o custom-columns=NAME:.metadata.name,ADDR:.spec.triggers[0].metadata.scalerAddress \
  | grep "wva-external-scaler.$NS"
```

Then, for each:

- **Another install serves the model**: `make scaledobjects-repoint` rewrites
  `scalerAddress` on objects that name a namespace where no scaler runs.
- **Going back to what scaled it before**: change its trigger, in GitOps if it
  is managed there.
- **No autoscaling any more**: delete it, then set the replica count you want.
  KEDA restores the original count only when the object has
  `advanced.restoreToOriginalReplicaCount: true`; otherwise the workload stays
  where it is, which for a parked model is zero.
  ```bash
  kubectl delete scaledobject <name> -n <ns>
  kubectl scale deployment <name> -n <ns> --replicas=1
  ```

### Policy namespace and limiter leftovers (cluster admin)

The per-install suffix is the first eight hex digits of the SHA-256 of the
controller's namespace (`wva-<namespace>` in the out-of-reach shape). The policy
namespace is the `policyNamespace` in the controller's `wva-controller-status`
ConfigMap, else `wva-policy`. The uninstall removes that ConfigMap, so read it
first; afterwards, the managed namespace's `wva.llmd.ai/policy-namespace` label
names it, if it is set:

```bash
POLICY_NS=$(kubectl get cm wva-controller-status -n "$NS" \
  -o jsonpath='{.data.policyNamespace}' 2>/dev/null)
POLICY_NS="${POLICY_NS:-wva-policy}"
SUFFIX=$(printf '%s' "$NS" | sha256sum | cut -c1-8)
kubectl delete rolebinding "wva-policy-reader-$SUFFIX" -n "$POLICY_NS"
kubectl get clusterrole,clusterrolebinding | grep -- "-$SUFFIX"   # anything the uninstall could not remove
```

Only when no scaling-manager install remains on the cluster:

```bash
make disable-physical-limiter   # clears the limiter from wva-policy and every policy ConfigMap a controller reads
kubectl delete namespace "$POLICY_NS"
```

`disable-physical-limiter` leaves the read-only RBAC in place on purpose; the
namespace delete removes it. Like `enable-physical-limiter`, it needs `kubectl`,
`jq` and `yq`. Where no limiter is declared it changes nothing and prints
`No limiter declared in <ns>/wva-scaling-policy-config; nothing to remove.` A
failed edit stops with an error naming the step, and never leaves an emptied
ConfigMap.

If the uninstall ran as a namespace owner and reported Forbidden objects, a
cluster admin re-runs the same `make undeploy-wva` with the same variables.

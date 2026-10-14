# Upgrade and uninstall

How to move the controller to a new version without surprises, what a restart
costs, and what an uninstall removes and leaves behind. `$NS` is the namespace
the controller runs in.

> Part of the [deployment guide](../../deploy/). Day-to-day checks are in
> [After the install](operations.md); symptoms in [Troubleshooting](troubleshooting.md).

## Pin the version

The default image is `ghcr.io/ev-shindin/llm-scaling-manager:main`. `:main` is a
**floating** tag, rebuilt on every push to `main`, and the installer pulls it
with `imagePullPolicy: Always`. So every pod restart (a node drain, an eviction,
a leader change) can start different code than the one you tested. Do not run
production on it.

Pin a release tag or a digest, and deploy with the manifests from the **same**
release: an image older than the manifests crash-loops on flags it does not
know.

```bash
git checkout v0.9.0
make deploy-wva NAMESPACE=<namespace> IMG_TAG=v0.9.0
# or a digest
make deploy-wva NAMESPACE=<namespace> IMG=ghcr.io/ev-shindin/llm-scaling-manager@sha256:<digest>
```

`IMG_TAG` sets the tag of the default `IMG`; `IMG` replaces the whole reference.
Any tag other than `main`, `latest`, `dev` or `nightly`, and any digest, is
pulled `IfNotPresent`. Confirm what runs:

```bash
kubectl get deploy wva-controller-manager -n "$NS" \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"  "}{.spec.template.spec.containers[0].imagePullPolicy}{"\n"}'
```

Pass every install-time variable you used the first time (`SCOPE`,
`ENVIRONMENT`, `WVA_LIMITER`, `DEPLOY_ALERTING_RULES`, ...) on every upgrade.
An upgrade re-renders the install from them: `DEPLOY_ALERTING_RULES` left at its
default `false`, for example, deletes the alert rules. The full list is in
[Configuration](configuration.md).

## Upgrade

1. **Back up your policy.** The install owns `wva-scaling-policy-config`; keep a
   copy of what you tuned
   ([Changing policy safely](operations.md#changing-policy-safely)).
2. **Check prerequisites** against the new release. It is read-only:
   ```bash
   make check-prereqs NAMESPACE=<namespace>
   ```
   A new release can need a kind or a permission the old one did not. If it
   reports missing prerequisites, a cluster admin runs, once:
   ```bash
   NAMESPACE=<namespace> make setup-prereqs        # (cluster admin)
   ```
   A missing **optional** prerequisite does not stop the install: the controller
   runs without the feature that needs it, and says so.
3. **Deploy** the pinned image, as above.
4. **Verify**:
   ```bash
   make verify-deployment
   make verify-scaledobjects
   ```

## After every upgrade or restart

A new controller pod starts with an empty in-memory decision. What happens
meanwhile:

- **Until its first cycle completes** (one `GLOBAL_OPT_INTERVAL`, 15 s by
  default, plus leader election), the scaler answers KEDA with `0`, meaning "no
  decision yet". The HPA reads that as `minReplicaCount`, and its scale-down
  stabilization window (300 s by default) keeps it from acting on that before
  the first real decision lands. A shorter scale-down window shortens that
  protection.
- **Under the utilization-share optimizer**, every planned model holds what it
  runs for one fill timeout, about three minutes with default timings, and
  shows the blocked reason `quiet-period`. Expect it on every upgrade and leader
  change.

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
| `wva-utilization-share-history` | |
| the ScaledObjects **this installer created** | KEDA restores each workload to its pre-autoscaling replica count as they go. Keep them with `UNDEPLOY_SCALEDOBJECTS=false` |

### What it leaves

| left behind | why | clean up |
| --- | --- | --- |
| the namespace `$NS` | it may hold your model servers | `kubectl delete namespace "$NS"` only if nothing else lives there |
| ScaledObjects you **adopted** (written by you or GitOps, pointed at this scaler) | not the installer's to delete. Their trigger now calls a scaler that is gone, and **a model parked at zero never wakes**: activation only comes from the scaler | see below |
| Prometheus, KEDA, the EPP | shared; this install may not have created them | `UNDEPLOY_SHARED=true` removes them too, for everything on the cluster |
| the PrometheusRule `controller-manager-alerts` | the uninstall does not render the alerts component | `kubectl delete prometheusrule controller-manager-alerts -n "$NS"` |
| the dashboard ConfigMap `wva-operation-dashboard` | shared by every install on the cluster | (cluster admin, if it is in a shared monitoring namespace) `kubectl delete configmap wva-operation-dashboard -n <dashboard-namespace>` |
| cluster policy in the policy namespace (`wva-policy`): the ConfigMap, Role `wva-policy-reader` and this install's RoleBinding | written by `make enable-physical-limiter`, for every controller on the cluster | (cluster admin) see below |
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
namespace is `wva-policy` unless it was set with `WVA_POLICY_NS`; use the same
value here:

```bash
POLICY_NS="${WVA_POLICY_NS:-wva-policy}"
SUFFIX=$(printf '%s' "$NS" | sha256sum | cut -c1-8)
kubectl delete rolebinding "wva-policy-reader-$SUFFIX" -n "$POLICY_NS"
kubectl get clusterrole,clusterrolebinding | grep -- "-$SUFFIX"   # anything the uninstall could not remove
```

Only when no scaling-manager install remains on the cluster:

```bash
make disable-physical-limiter WVA_POLICY_NS="$POLICY_NS"   # clears the limiter from every policy ConfigMap it finds
kubectl delete namespace "$POLICY_NS"
```

`disable-physical-limiter` leaves the read-only RBAC in place on purpose; the
namespace delete takes it with it.

If the uninstall ran as a namespace owner and reported Forbidden objects, a
cluster admin re-runs the same `make undeploy-wva` with the same variables.

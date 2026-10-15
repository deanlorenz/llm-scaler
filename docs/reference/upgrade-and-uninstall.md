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
know. Before the first release, pin a digest.

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

#### Other changes that need an action

- **OpenShift: a cluster admin runs `make setup-prereqs`.** The ServiceMonitor
  now scrapes as `wva-metrics-scraper`, and the old non-expiring token Secret
  `wva-controller-manager-token` is deleted. Only the admin phase does both.
  `make deploy-wva INSTALL_PHASE=wva` alone keeps scraping with the old token.
  ```bash
  NAMESPACE=<namespace> make setup-prereqs ENVIRONMENT=openshift   # (cluster admin)
  # out-of-reach shape: the controller runs in wva-<namespace>
  NAMESPACE=<namespace> make setup-prereqs ENVIRONMENT=openshift WVA_NS=wva-<namespace> WVA_WATCH_NS=<namespace>
  # cluster scope
  make setup-prereqs ENVIRONMENT=openshift SCOPE=cluster WVA_NS=<controller namespace>
  ```
- **The install stops when KEDA is absent**, before it creates anything. It
  prints the command that installs KEDA
  (`KEDA_HELM_INSTALL=true NAMESPACE=<namespace> make setup-prereqs`). The
  controller-only phase (`INSTALL_PHASE=wva`) never installs KEDA, so there it
  always stops.
- **kube-prometheus-stack is pinned** (`KUBE_PROMETHEUS_STACK_CHART_VERSION`,
  default `92.7.0`). A newer release already installed is kept, with a
  warning. Set the variable to install that exact version, including an older
  one.

#### Prometheus TLS verification is on by default

Earlier releases skipped TLS verification for every Prometheus. This release
verifies the certificate of any `https://` Prometheus, except the one the
installer deploys itself, which has no CA to verify with.

A re-run keeps what the controller runs with now:

- **A CA path, same Prometheus.** `PROMETHEUS_CA_CERT_PATH` is kept, and so is
  the verification setting it had. If verification was off, it stays off, and
  the installer warns that the CA path is unused. A volume and mount you added
  with `kubectl patch` survive re-runs. With GitOps, keep them in your overlay.
- **A CA path, different `PROMETHEUS_URL`.** The CA path is dropped, because
  the new Prometheus may use another CA. The installer warns, and prints the
  re-run that keeps it.
- **Verification off, no CA, same Prometheus.** It stays off, and the
  installer warns
  `TLS verification for <url> stays OFF, as it was before this upgrade`.
  Turning it on with no CA would stop the controller connecting.
- **Verification off, but a different `PROMETHEUS_URL`.** The new Prometheus
  is verified, and the installer says so. Pass
  `PROMETHEUS_TLS_INSECURE_SKIP_VERIFY=true` to keep it off.

An explicit `PROMETHEUS_TLS_INSECURE_SKIP_VERIFY` or `PROMETHEUS_CA_CERT_PATH`
always wins.

To turn verification on, do one of these:

- Mount the CA that signed the Prometheus certificate, and re-run with
  `PROMETHEUS_CA_CERT_PATH=<file>` ([TLS configuration](prometheus.md#tls-configuration)).
- Re-run with `PROMETHEUS_TLS_INSECURE_SKIP_VERIFY=false`, if a public CA signed
  the certificate.

When a Prometheus setting changes, the installer restarts the controller. The
controller reads these settings only when it starts.

If the installer cannot read the controller's current config, it stops before
changing anything, and asks you to re-run with
`PROMETHEUS_TLS_INSECURE_SKIP_VERIFY=true` or `=false`. It does not stop when
you pass `PROMETHEUS_CA_CERT_PATH`, or for the Prometheus it deploys itself.

The re-run commands the installer prints carry `PROMETHEUS_URL`, `IMG`,
`DEPLOY_PROMETHEUS=false` and `ENVIRONMENT` when you set them. Add every other
variable you installed with.

#### Patch on pods moves to Role `wva-pod-writer`

The controller's ClusterRole no longer lets it patch pods. Each namespace that
needs it gets Role `wva-pod-writer` instead. Only warm pools and the
utilization-share optimizer patch pods. A namespace-scoped controller keeps
`patch` in its own Role, so nothing changes for it.

For a cluster-scoped controller, the upgrade grants `wva-pod-writer` before it
removes the old grant. It does so in each namespace that holds:

- a warm pool that calls this controller,
- a pod with a utilization-share transfer mark, of a model that calls this
  controller, or
- any model that calls this controller, when the utilization-share optimizer
  is active (not shadow). The re-run also removes the `optimizer:` block
  ([Re-running the installer resets the default entry](scaling-policy.md#re-running-the-installer-resets-the-default-entry)),
  so run these again, in order:
  ```bash
  make utilization-share-shadow NAMESPACE=<models namespace> CONTROLLER_NAMESPACE=<controller namespace>
  make utilization-share-activate NAMESPACE=<models namespace> CONTROLLER_NAMESPACE=<controller namespace>
  ```

It prints each namespace it granted in. Then read its warnings:

- **`Could not grant the controller patch on pods in <namespace>`**: that warm
  pool stops lending, or the optimizer cannot clear its marks there. A cluster
  admin runs the command the warning prints.
- **`... are UNKNOWN`**: the installer could not list ScaledObjects or pods
  across the cluster. Re-run the upgrade as a cluster admin, or grant by hand in
  each namespace with a warm pool or an active optimizer, from the repository
  root:
  ```bash
  bash deploy/lib/pod_writer.sh <namespace> <controller namespace> | kubectl apply -f -
  ```
- **`Could not tell whether the utilization-share optimizer is active`**: the
  installer could not read the controller's status or policy. If the optimizer
  is active, run the command above in each namespace the warning lists.
- **`... does not say which controller they call`**: a ScaledObject declares a
  warm pool or a model, but its `scalerAddress` is not
  `wva-external-scaler.<namespace>...`. If it calls this controller, run the
  command above in its namespace.

A cluster without the ScaledObject kind (`SCALER_BACKEND=none`) has no warm
pools or models to grant for, and the upgrade says nothing.

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
| RoleBinding `wva-pod-writer-<suffix>` in every workload namespace, and Role `wva-pod-writer` where no other controller's binding uses it | made by `deploy/warmpool.sh create`, utilization-share activate, and the upgrade to this release |
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
| Role `wva-pod-writer` and RoleBinding `wva-pod-writer-<suffix>` the uninstall could not read or delete | it may not list RoleBindings across the cluster, or delete them there. It lists each one with its `kubectl delete` command, or says it could not look | (cluster admin) see [Leftover pod-write grants](#leftover-pod-write-grants) |
| Role `wva-pod-writer` in a namespace another controller also uses | that controller's binding still needs it | nothing; it goes with that controller |
| Role `wva-pod-writer` and RoleBinding `wva-pod-writer-<suffix>` everywhere, when the controller Deployment is still there | the delete failed, or its result could not be read. A running controller still needs the grant | remove the Deployment, then run the uninstall again |
| utilization-share transfer marks on pods | written while the optimizer acted | [Transfer marks left on pods](troubleshooting.md#transfer-marks-left-on-pods-after-a-downgrade); set `shadow: true` and wait a cycle **before** uninstalling to avoid them |

### Leftover pod-write grants

List the pod-write bindings, and the controller namespace each one is for:

```bash
kubectl get rolebinding -A -l app.kubernetes.io/name=workload-variant-autoscaler \
  -o 'custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,ROLE:.roleRef.name,CONTROLLER:.subjects[0].namespace' \
  | grep -E '^NAMESPACE|wva-pod-writer'
```

For each row whose `CONTROLLER` is the namespace you uninstalled:

```bash
kubectl delete rolebinding <NAME> -n <NAMESPACE>
```

Then delete the Role in that namespace, once no other `wva-pod-writer-*`
binding is left there:

```bash
kubectl delete role wva-pod-writer -n <NAMESPACE>
```

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

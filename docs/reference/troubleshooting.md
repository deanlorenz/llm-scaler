# Troubleshooting


## Deployment Not Scaling Up

**Symptom**: Deployment remains at 0 replicas despite pending requests.

**Possible Causes**:

1. **InferencePool datastore is empty**:
   ```bash
   # Check if InferencePool exists and is reconciled
   kubectl get inferencepool
   ```
   
   the scaling manager watches a single InferencePool API group (`inference.networking.k8s.io` or `inference.networking.x-k8s.io`). If the cluster's pools use the other group, the datastore stays empty and scale-from-zero never gets a recommendation.
   
   **Solution**: Ensure InferencePool is created and reconciled before the workload's ScaledObject. When using **`make deploy-e2e-infra`**, `deploy/install-epp.sh` installs the GAIE standalone chart which creates the InferencePool after the EPP starts.

2. **Labels mismatch**:
   ```bash
   # Check deployment labels
   kubectl get deployment llama-8b-deployment -o jsonpath='{.spec.template.metadata.labels}'
   
   # Check InferencePool selector
   kubectl get inferencepool llama-pool -o jsonpath='{.spec.selector}'
   ```
   
   **Solution**: Ensure deployment labels match InferencePool selector.

3. **EPP metrics source not available**:
   ```bash
   # Check if EPP service exists
   kubectl get svc | grep epp
   ```
   
   **Solution**: Verify EndpointPicker service is running and metrics are being collected.

4. **No pending requests in queue**:

   Extract the Bearer token from the EPP metrics reader secret:
   ```bash
   TOKEN=$(kubectl -n workload-variant-autoscaler-system get secret wva-epp-metrics-token -o jsonpath='{.data.token}' | base64 --decode)
   ```

   Port-forward the EPP metrics service to localhost:9090:

   ```bash
   kubectl port-forward svc/epp 9090:9090
   ```

   In a separate terminal, query the metrics endpoint:
   ```bash
   curl -H "Authorization: Bearer $TOKEN" localhost:9090/metrics | grep inference_extension_flow_control_queue_size
   ```

   **Solution**: Verify requests are being sent to the correct model endpoint.

   The metric family was renamed: llm-d's EPP exports
   `llm_d_epp_flow_control_queue_size` and upstream gateway-api-inference-extension
   still exports `inference_extension_flow_control_queue_size`. Grep for both — the scaling manager
   reads whichever exists.

### The EPP scrape is failing but the scaling manager still wakes models (slowly)

**Symptom**: the log carries

```text
Scale-from-zero: EPP metrics scrape failing; reading the flow-control queue from
Prometheus instead. Wakes still work but are slower and bounded by the scrape
interval — fix the direct path.
```

and `wva_scale_from_zero_queue_fallback_active{pool="..."}` is `1`.

**What it means**: The scaling manager reads the flow-control queue by scraping the EPP pod
**directly** — pod IP, EPP metrics port, bearer token projected at
`/var/run/secrets/epp-metrics/token`. Every other metric it consumes comes from
Prometheus, so this one path can fail on its own. When it does, the scaling manager falls back to
reading the same metric from Prometheus so models still wake. Nothing looks broken
from the outside; wakes are just slower — bounded by the Prometheus scrape interval
rather than the engine's 100 ms loop — and a sample older than 90 s is ignored, so
a queue that has already drained cannot wake anything.

This is a degraded state, not a supported one. Check, in order:

1. **The token.** `Failed to read EPP metrics token` in the log means the projected
   volume is missing; the scaling manager then scrapes unauthenticated and the EPP rejects it.
   ```bash
   kubectl -n workload-variant-autoscaler-system exec deploy/wva-controller-manager --      ls -l /var/run/secrets/epp-metrics/token
   ```
2. **The EPP's tokenreview RBAC**, which authorizes the scaling manager's token. It is bound per
   release name, so a renamed or reinstalled EPP release leaves the scaling manager's binding
   pointing at nothing.
   ```bash
   kubectl get clusterrolebinding | grep epp-tokenreview
   ```
3. **Network reachability to the EPP pod IP.** Prometheus scrapes the EPP too, so a
   NetworkPolicy admitting the monitoring namespace but not the scaling manager's breaks exactly
   this path and no other.

The gauge returns to `0` and the log reports the scrape recovered once the direct
path works again.

### E2E and infra-only deploys

For e2e-style deploys, **`deploy/install-epp.sh`** enables EPP flow control when `ENABLE_SCALE_TO_ZERO=true` (adds the `flowControl` feature gate to the GAIE standalone chart). The **InferenceObjective** `e2e-default` is created by the scale-from-zero e2e tests (`test/e2e/fixtures`), not by the install scripts. See [deploy/install-epp.sh](../../deploy/install-epp.sh).

## Slow Scale-Up Response

**Symptom**: Deployment takes too long to scale up from zero.

**Possible Causes**:

1. **High concurrent processing load**:
   
   This can happen when there are many variants that are scaled down to zero, causing the scale-from-zero engine to process multiple scaling decisions simultaneously.
   
   **Solution**: Increase `SCALE_FROM_ZERO_ENGINE_MAX_CONCURRENCY`:

   Add the environment variable to the scaling manager controller deployment:

   ```yaml
   apiVersion: apps/v1
   kind: Deployment
   metadata:
      name: controller-manager
      namespace: workload-variant-autoscaler-system
   spec:
      template:
         spec:
            containers:
            - name: manager
            env:
            - name: SCALE_FROM_ZERO_ENGINE_MAX_CONCURRENCY
              value: "50"  # Increase for larger clusters
   ```

2. **Inference gateway not receiving requests**:
   
   **Solution**: Verify that requests are being routed through the inference gateway and not directly to model server endpoints.

## Utilization-Share Optimizer

Configuration: [`optimizer` in the scaling policy](scaling-policy.md#optimizer-cluster-default-only-live).
Metrics and blocked reasons: [utilization-share metrics](prometheus.md#utilization-share-optimizer-metrics).

### The optimizer evaluates but never moves anything

Work down the list in order.

1. **Is it selected at all?** `wva_utilization_share_mode` says: `off`,
   `invalid`, `shadow` or `active` is `1`. For `invalid` or `off`, the log says
   why: `Utilization share: invalid optimizer block; keeping today's optimizer`
   (a misspelled key, a value out of range, an empty `clusterNamespaces` entry,
   or `needs a limiters: list`, no budget to share). A block in a
   namespace-local scaling-policy map is ignored:
   `ignoring optimizer blocks in namespace-local scaling-policy maps`. On a
   namespace-scoped install that reads its cluster policy from a separate policy
   namespace, an edit there is read only when the controller starts: restart it
   after changing the block.
2. **Is it in shadow mode?** `wva_utilization_share_mode{mode="shadow"} == 1`;
   the log says `Utilization share: would rebalance (shadow)` rather than
   `Utilization share: rebalancing`, and `wva_utilization_share_promised_gpus` is
   absent: it is published only while the optimizer acts. Set `shadow: false`
   (or remove the key) to act. Both lines are written when the set of actionable
   roles changes, not every cycle, so a quiet log is not a stopped optimizer.
3. **Is the model planned?** The two lines above, and at `--v=4` the per-cycle
   `Utilization share: evaluation`, carry a `frozen` field listing the
   group's models left to today's optimizer this cycle, with the reason: no live
   analyzer result, a role with demand but no measured capacity, a
   namespace-local scaling-policy map (not planned in the cluster group), or a
   namespace not in `clusterNamespaces`. A model whose variants run on more than
   one accelerator type, one in a namespace with
   `enabled: false`, or one under only a `gpu-inventory` limiter without
   `physicalGroups: true` is in no group at all and publishes no role series.
4. **Is any role actionable?** `wva_utilization_share_actionable` is `1` only
   for a role outside its tolerance band *and* off its whole-replica target. All
   `0` means the fleet is already where the optimizer wants it, or every
   imbalance is smaller than one replica; `wva_utilization_share_withheld_total{reason="not-actionable"}`
   counts the latter.
5. **Does it stay actionable?** A role must be actionable on **two consecutive
   cycles** before a transfer is planned for it, so a role that flickers in and
   out of band never moves. A hard imbalance (step 6) does not wait for the
   second cycle. At most two replicas move per role per cycle and two
   transfers run per group at once.
6. **Is it held?** `withheld_total{reason="reversal-hold"}` rising, or the
   model's blocked reason `reversal-hold`, means a role that just gave is being
   kept from receiving (or the reverse) until the hold in
   `wva_utilization_share_effective_seconds{param="reversal-hold"}` passes. That
   is the anti-oscillation rule working, not a fault. The hold lasts about twice
   a release time from the start of the transfer it gave in; with a 300 s
   scale-down window that is on the order of 12 minutes. It is lifted for a
   **hard imbalance**: a role whose load is at least 0.9 of its scale-up
   threshold receives at once from a role whose load, after giving, is at most
   0.6 of its own (`utilizationShare.stabilization.skipWaitsWhen`); the
   controller logs that transfer with `skippedWaits=true`, and the role does not show
   `reversal-hold` while such a donor exists. So a role held below its need for
   the whole hold means no other role is that lightly loaded. A cancelled transfer holds only its own
   donor -> receiver pair from starting again; each role can still move with
   any other role. The counter also counts moves withheld by a donor's
   back-off after an aborted release, or by a donor that has given all it can.
   A donor's calm is judged on the higher of its current need and its peak
   need -- two readings in a row -- over its scale-down window, so a model
   read in a trough of its normal demand is not a "lightly loaded" donor.
   For a receiving model whose bursts outlast a move, the donor is judged on
   its current need alone: the move lands inside the burst.
   Two more reasons on the same counter are not holds: `donor-peak` (while the
   quota holds every model's peak, a model already at its need takes no
   headroom that would leave its donor below the donor's own recent peak, by
   a single move or a donor set) and `awaiting-return` (a short model whose
   replica spans several pods, such as a LeaderWorkerSet, is credited with
   returns still releasing whose pods hold its pods, in whole replicas, and
   only the rest is funded by draining other models; the model itself shows
   blocked reason `awaiting-release`).
7. **Is it swinging, or at the transfer limit?** Blocked reason `swinging`
   (`wva_utilization_share_swinging == 1`) means the role reversed direction
   twice within the swing window and is planned on its mean need, not its
   current one, so it may look short and still not receive. `transfer-limit`
   means the group already runs two transfers with a donor; it clears as they
   land.
8. **Did the controller just restart?** After a restart -- and after the
   optimizer starts acting, or a group first appears -- every planned model of
   the group holds what it runs (or its restored target) for one fill timeout,
   about three minutes with default timings, and nothing in it scales:
   a fill in flight before the restart has no mark, and its receiver's pods
   must not lose their GPUs meanwhile. Expect this freeze on every upgrade or
   leader change. Every planned model of the group shows the blocked reason
   `quiet-period` while it lasts, and the log line
   `Utilization share: ledger started` carries `planningFrom`. A freeze that
   does not end shows `marks-unreadable` instead: the controller cannot read
   the transfer marks on the donors' pods, and the Error line
   `Utilization share: could not read transfer marks; retrying next cycle` says
   why, typically a missing RBAC grant to list pods in one model's namespace.
   The models' demand history survives the restart in the ConfigMap
   `wva-utilization-share-history` in the controller's namespace, and the log
   line `Utilization share: need history restored` says it was read back:
   `rolesKnownNow` counts the roles whose peaks are known at once, and
   `peaksKnownAt` is when the first restored role's peak is known (a time in
   the past when some are known now). When it was not -- no stored history, or
   one ending more than ten minutes before the restart -- the headroom cap is
   on until a `stabilization.flappingWindow` of history exists and the models'
   peaks are known again (`wva_utilization_share_headroom_capped` is not
   published during the quiet period, when the planner does not plan)
   ([a model shrinks while no other model grows](#a-model-shrinks-while-no-other-model-grows)).
   A history that covers less than today's `flappingWindow` -- the window was
   raised since it was written -- is restored, but its peaks are known only
   once today's window is covered. `Utilization share: stored need history not
   used` says why a stored entry was ignored (`reason`; `older than its window`
   when every sample in it has aged out). The Error line `Utilization share:
   could not read the stored need history; the models' peaks are unknown for a
   swing window and their headroom is capped meanwhile. Retrying every minute;
   nothing is written until a read succeeds` (with the `group`) means the read
   failed with something other than NotFound -- typically Forbidden: the Role
   `wva-utilization-share-history-role` or its binding is missing. On an
   install upgraded from before the store existed, the controller-only
   `make deploy-wva` reports both as optional prerequisites and installs
   without them; ask an admin to re-run `NAMESPACE=<controller-namespace> make
   setup-prereqs` to enable the store. Nothing is restored, and nothing is
   written until a read succeeds, so the stored history is not overwritten.
   `Utilization share: could not store the need history; a restart now forgets
   the models' peaks` is the same for the write. Both are logged per group,
   once per change of the error, and `Utilization share: the need history
   store works again` marks that group's recovery.
   The ConfigMap is bounded below the API server's 1 MiB object limit: at most
   128 models' roles per group, and each group written gets an equal share of
   900 KiB. A group over either bound leaves out the roles with the smallest
   stored peaks, which count at their `maxReplicas` after a restart:
   `Utilization share: need history truncated; the roles left out count at
   their ceiling after a restart` names them (`dropped`, the first ten) and is
   logged once per change.
   To flush the history -- after a model's load has changed for good, say, and
   its old peaks keep the cap on -- delete the ConfigMap:
   ```bash
   kubectl delete configmap wva-utilization-share-history -n <controller-namespace>
   ```
   The running controller notices on its next read or write of it (within a
   minute or so) and forgets its in-memory history too, rather than writing it
   straight back: `Utilization share: the need history ConfigMap was deleted;
   every model's need history is forgotten`. The cap then judges every model
   as after a restart with nothing stored, for one window. Editing one group's
   key by hand does not flush it: the controller's next write of that group
   replaces it from memory. Entries the controller cannot parse are removed by
   its next write, and entries of another format version once a week old.
   Uninstalling with `make undeploy-wva` deletes the ConfigMap.
9. **Could it not mark the donor?** `Utilization share: could not mark a donor pod;
   transfer not started` means the donor's pod could not be steered by a fault:
   a sibling's deletion cost leaves no room for the mark (see
   [which pod leaves](scaling-policy.md#for-model-owners-what-the-optimizer-may-take-and-how-to-protect-a-model)),
   or the patch failed. The donor backs off, shows the blocked reason
   `donor-not-steerable`, gets a `UtilizationShareDonorNotSteerable` Event naming
   the cause and the end of the back-off, and is retried after it.

   A donor whose workload is changing is held, not failed: mid-rollout (by its
   status, pods of two ReplicaSets, or LWS groups of two revisions at or above
   its partition), a pod not yet created, scheduled or Ready, or an LWS group
   missing or being replaced. Its receiver tries another donor; it counts no
   abort, does not back off and gets no Event or blocked reason; at `--v=4` the
   log says `Utilization share: donor's workload is changing; transfer not
   started`. A paused Deployment or a held LWS canary rollout stays "changing",
   and does not give, until it is resumed or finished.

   A donor whose every pod (or LWS group) is already given to transfers still in
   flight is not broken, only exhausted: it is held until one of those releases
   lands, or for one release timeout, so its receiver tries another donor, but
   it counts no abort, does not back off, shows no blocked reason, gets no Event
   and is not counted as `reversal-hold`; at `--v=4` the log says
   `Utilization share: donor has nothing left to give; transfer not started`.

If a model shows `whole-replica-short`, it serves below its need while the
quota covers the needs in fractions but not in whole replicas: with 8-GPU
replicas, a quota a few GPUs short of every model's need rounded up to whole
nodes. Moves go to the model that is worse off by weight, and this one gave.
Raise the quota by a replica, lower a floor, or accept the trade.

If a P/D model shows a blocked reason, `wva_model_scaling_blocked` does not say
which role it came from (it has no `role` label); read
`wva_utilization_share_headroom` and `wva_utilization_share_actionable` by `role`.

If a lowered quota does not shrink the fleet: the optimizer does not evict to
meet a quota. A group whose quota is now below what it holds plans over what it
holds, and moves back under the quota only as models scale down.

If transfers do start but the model still does not grow, read its
`wva_model_scaling_blocked` reasons.

### A model shrinks while no other model grows

**Symptom**: under the utilization-share optimizer a model loses one or more
replicas, at least a few cycles plus one scale-down window after its load fell, and no other
model in the quota group grows to take the GPUs.

**What it means**: the model gave headroom back to the quota. This happens
only while the quota is **contended**: it cannot hold every model at its peak
need over the last `stabilization.flappingWindow` at the same time (a peak
counts once two readings in a row reach it). While no model's peak is known --
a new group, or every model after a controller restart that found no usable
stored history (see step 8 above), until a `flappingWindow` of history exists
-- each counts at its `maxReplicas` (the whole quota without one), so the cap
is on until a full window of history shows the peaks fit. A model that joins a group whose peaks are
known counts at what it holds, and does not turn the cap on by itself. While the
peaks fit, the whole spare stays spread by weight and nothing is given back.
While contended, the optimizer gives each model (each role of a P/D model) at most `maxHeadroomReplicas`
replicas (default `1`) times its weight, rounded up, above what it needs, and
leaves the rest of the quota unassigned, so that a model whose demand rises can
start pods into it at once instead of waiting for another model's pods to
finish their requests. A model that has held more than its need plus its
headroom for `stabilization.consecutiveCyclesBeforeMove` cycles in a row (two
by default) -- typically one that grew for a burst that has ended -- starts
giving all of the extra back in one cycle; the release then waits its
scale-down window, as today's scale-down would. It does so only while no model in the group that can grow is short of
its need, and
only while its load would stay at or below
`stabilization.returnSpareWhenLoadAtMost` (default `0.6`) on what it keeps,
not counting its headroom. It never goes below its need (the higher of its
current and average need while it swings) plus its headroom, below what the
other models' recent peaks leave of the quota in whole replicas, its
`minReplicaCount` or its last replica: a model that bursts alone keeps its
spare, and GPUs are returned only when another model's recent peak could use
them. If, while the release is still in its
scale-down window, the model is short of its target again or busier than that
load on what it keeps, the return is called off and the model keeps the
replica. If another model turns short while the return is still releasing, the
returned GPUs go to that model when they come free: its scale target gets a
`UtilizationShareReceiving` Event naming the model that is giving them back,
and the log says `Utilization share: returned spare handed to a receiver`. A
return is handed over only when the returned pod holds the short model's pod;
a return rebuilt from its pod marks after a restart, whose shape is not known,
is never handed over and goes to the unassigned GPUs.

**How to tell**: the donor's scale target carries a `UtilizationShareGiving`
Event that names "the quota group's unassigned GPUs" and says "because it holds more headroom than maxHeadroomReplicas gives it while no model is short, and stays calm without it; the replica drains after its scale-down window, and is kept if its demand comes back before then; a model whose demand rises takes the GPUs at once". A return called off carries a
`UtilizationShareCancelled` Event: "this model's demand came back, so it keeps
the replica it was giving back". The controller log has
`Utilization share: transfer started` with `returned` set to true and no
`receiver`, then `Utilization share: spare returned to the quota` when the
replica is released (unless it was handed to a receiver first). `wva_utilization_share_starts_total{kind="return"}`
counts returns per quota group. A model that holds more than its headroom and
is not giving it back shows why in `wva_utilization_share_withheld_total`:
`return-not-calm` (its load is above `returnSpareWhenLoadAtMost`) or
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
kubectl logs -n <controller-namespace> -l app.kubernetes.io/name=workload-variant-autoscaler | grep 'spare returned to the quota'
kubectl logs -n <controller-namespace> -l app.kubernetes.io/name=workload-variant-autoscaler | grep -E 'recent peaks|peaks are not known'
```

**What to do**: nothing, if the model still serves within its targets: the
GPUs are free for whichever model needs them next, and this model takes them
back the same way when its own demand rises. To keep more spare on each model,
raise `utilizationShare.maxHeadroomReplicas`; a value as large as the models'
`maxReplicas` spreads the whole spare by weight even while contended, as before
the key existed, and leaves little to return, at the price that a model whose demand rises must
wait for another model's pods to drain before it grows. To make a model keep
replicas longer after its demand falls, lower
`stabilization.returnSpareWhenLoadAtMost`. To protect one model only, raise its
`minReplicaCount`. A higher `weightClass` gives it more headroom replicas
(weight times `maxHeadroomReplicas`, rounded up) but does not shield that
headroom from another model's need. See the
[scaling policy reference](scaling-policy.md#optimizer-cluster-default-only-live).

### A model shows `no-compatible-donor`

**Symptom**: `wva_model_scaling_blocked{reason="no-compatible-donor"} == 1` on a
model that is short, while other models on the same accelerator type hold more
than they need.

**What it means**: every one of the model's pods needs a donor pod at least as
large to take its place: a 4-GPU pod cannot be funded by two 2-GPU pods, since
nothing shows the two share a node. Here no donor in the group has pods large
enough, so no transfer can be planned for this model.

**What to do**: the optimizer has no setting that changes this. Give the model
GPUs another way: raise the quota for that accelerator type, or lower a model
whose pods are at least as large. If the model has pods of more than one size
(P/D roles with different GPU counts), each role is judged on its own pods; check
which role is short with `wva_utilization_share_headroom < 0`.

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
cycle. The controller removes the marks it wrote and restores each pod's previous
deletion cost. On a namespace-scoped install that reads its cluster policy from a
separate policy namespace, that edit takes effect only after a controller
restart: edit, restart, then wait a cycle. Check that none are left:

```bash
kubectl get pods -A -o json | jq -r '.items[]
  | select(.metadata.annotations["llm-d.ai/utilization-share-transfer"] != null)
  | .metadata.namespace + "/" + .metadata.name'
```

**Cleanup after the fact**: the mark records the pod's deletion cost from before
the transfer as `prevCost`; restore it, or remove the cost when the mark has
none, and remove the mark:

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

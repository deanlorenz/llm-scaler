# Issue: EPP metrics token empty on all `namePrefix`-qualified installs

## Status

**Part 1 FIXED** — `config/base/rbac/kustomization.yaml` `replacements[]` block applied and
verified with `kubectl kustomize` across all four production overlays. The Secret annotation
now renders as `wva-epp-metrics-reader` (correct) on every overlay.

**Part 2 NOT NEEDED** — kustomize's built-in `namePrefix` + `namespace` transforms already
rewrite `subjects[].name` and inject `subjects[].namespace` in ClusterRoleBindings. Verified:
all four overlays render `name: wva-epp-metrics-reader` / `namespace: wva-system` in the
`epp-metrics-reader-role-binding` subject.

**Cluster (dhl-la-1708 / pokprod001)** — manual fix applied last session
(`kubectl annotate` + `kubectl replace`). Token was confirmed populated (1728 chars) at the
time. Recheck before the next benchmark run.

**Make target** — The permanent fix is already in the kustomize base. Any fresh install via:

```
make deploy-wva-on-openshift NAMESPACE=<ns>
```

or

```
make deploy-wva-on-k8s NAMESPACE=<ns>
```

will now produce a correctly-populated `wva-epp-metrics-token` Secret automatically.
No overlay or Makefile changes are needed — the fix lives entirely in
`config/base/rbac/kustomization.yaml`.

## Severity
**Silent operational failure.** The WVA controller's direct EPP scrape path
(`queue_fallback.go`, scale-from-zero wake signal) is broken on every production
install that uses `namePrefix: wva-`. A model parked at 0 replicas may never be
woken because the queue depth that triggers waking cannot be read. No error is
visible in normal operation.

---

## Symptom

The WVA controller logs this at startup (DEBUG level, often missed):

```
Failed to read EPP metrics token - EPP authentication will be disabled
path=/var/run/secrets/epp-metrics/token
```

Every direct EPP pod scrape then returns `401 Unauthorized`. The scale-from-zero
queue fallback (`internal/engines/scalefromzero/queue_fallback.go`) falls back to
Prometheus for queue depth — but this only works if the Prometheus path is
healthy. If both fail, a parked model is never woken.

In benchmark runs: all EPP scrape files (`epp*_metrics.log`) contain only
`Unauthorized` for the entire run, producing zero EPP signal in extracted results.

---

## Root Cause

### The kustomize `namePrefix` annotation rewrite gap

`config/base/rbac/epp-metrics-token-secret.yaml` declares a
`kubernetes.io/service-account-token` Secret whose annotation names the SA that
should own it:

```yaml
# config/base/rbac/epp-metrics-token-secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: epp-metrics-token
  annotations:
    kubernetes.io/service-account.name: epp-metrics-reader   # ← hardcoded
type: kubernetes.io/service-account-token
```

All four production overlays set `namePrefix: wva-`:

```
config/overlays/namespace-scoped/openshift/kustomization.yaml:  namePrefix: wva-
config/overlays/namespace-scoped/kubernetes/kustomization.yaml: namePrefix: wva-
config/overlays/cluster-scoped/openshift/kustomization.yaml:    namePrefix: wva-
config/overlays/cluster-scoped/kubernetes/kustomization.yaml:   namePrefix: wva-
```

Kustomize applies `namePrefix` to resource `metadata.name` fields — so the
Secret is renamed to `wva-epp-metrics-token` and the SA is renamed to
`wva-epp-metrics-reader`. **But kustomize does not rewrite free-form annotation
values.** The annotation `kubernetes.io/service-account.name` remains
`epp-metrics-reader`.

The Kubernetes token controller binds a `kubernetes.io/service-account-token`
Secret by looking up the SA named in that annotation **in the same namespace**.
It finds no SA named `epp-metrics-reader` (the actual SA is
`wva-epp-metrics-reader`), so it never populates `.data.token`. The secret stays
empty permanently.

### Verification (before fix)

After any `make deploy-wva-on-openshift` (or `-on-k8s`) **without the fix**:

```bash
# Secret exists but has no token
kubectl get secret wva-epp-metrics-token -n <ns> \
  -o jsonpath='{.data.token}' | wc -c
# → 1  (just the trailing newline — empty)

# Annotation still names the unprefixed SA
kubectl get secret wva-epp-metrics-token -n <ns> \
  -o jsonpath='{.metadata.annotations.kubernetes\.io/service-account\.name}'
# → epp-metrics-reader   (wrong — should be wva-epp-metrics-reader)

# The actual SA has the prefix
kubectl get sa -n <ns> | grep epp
# → wva-epp-metrics-reader   0 tokens   ...
```

Confirmed on `dhl-la-1708` (pokprod001 cluster):
- `wva-epp-metrics-token` present, `.data.token` empty
- `wva-epp-metrics-reader` SA present, 0 tokens
- Annotation: `epp-metrics-reader` (does not exist)

### Why WVA appears to work despite this

WVA has two independent paths to EPP data:

1. **Direct pod scrape** — reads `/var/run/secrets/epp-metrics/token`, hits the
   EPP pod IP directly. This is the path that is broken. Used by the
   scale-from-zero queue fallback engine.

2. **Prometheus query** — PromQL over `inference_extension_*` series. This path
   works fine; it does not use the mounted token at all.

Most scaling decisions use the Prometheus path, so the controller appears healthy.
The broken path only matters for scale-from-zero: at 0 replicas Prometheus may
have stale or absent data, and the direct scrape is the live fallback. If both
fail, the wake signal never fires.

---

## Impact

- Every install using `namePrefix: wva-` (all four production overlays) deployed
  with a broken EPP direct-scrape token until this fix.
- Scale-from-zero wake signal via direct scrape was silently disabled.
- Benchmark EPP metrics were entirely absent (all scrapes `Unauthorized`).
- No error was surfaced at install time. `make deploy-wva-on-openshift` exits 0.

---

## Fix

### Part 1 — `config/base/rbac/kustomization.yaml` ✅ APPLIED

Added a `replacements` block that copies the SA name (post-namePrefix) into the
Secret's annotation. kustomize applies namePrefix to the SA's `metadata.name`
first, then the replacement copies that transformed value into the annotation.

```yaml
replacements:
- source:
    kind: ServiceAccount
    name: epp-metrics-reader
    fieldPath: metadata.name
  targets:
  - select:
      kind: Secret
      name: epp-metrics-token
    fieldPaths:
    - metadata.annotations.[kubernetes.io/service-account.name]
```

After this fix, `kubectl kustomize config/overlays/namespace-scoped/openshift/`
renders:

```yaml
kind: Secret
metadata:
  annotations:
    kubernetes.io/service-account.name: wva-epp-metrics-reader  # ← correct
  name: wva-epp-metrics-token
type: kubernetes.io/service-account-token
```

Verified for all four overlays.

### Part 2 — ClusterRoleBinding subject ✅ NOT NEEDED

kustomize's built-in namePrefix transform already rewrites `subjects[].name` in
ClusterRoleBindings, and the overlay's `namespace:` field injects
`subjects[].namespace`. All four overlays render correctly without an explicit
replacement. Verified with `kubectl kustomize`.

### Immediate cluster fix (without redeploying)

For any existing install where the token is empty:

```bash
NS=<wva-namespace>
SECRET=wva-epp-metrics-token
ACTUAL_SA=$(kubectl get sa -n "$NS" -o jsonpath='{.items[*].metadata.name}' \
  | tr ' ' '\n' | grep 'epp.*metrics\|metrics.*epp' | head -1)

kubectl annotate secret "$SECRET" -n "$NS" \
  kubernetes.io/service-account.name="$ACTUAL_SA" --overwrite

kubectl get secret "$SECRET" -n "$NS" -o yaml | kubectl replace -f -

# Verify token is now populated (should be >> 1 char — do NOT print the value)
sleep 3
kubectl get secret "$SECRET" -n "$NS" \
  -o jsonpath='{.data.token}' | wc -c
```

Applied manually on `dhl-la-1708` — token confirmed populated.

---

## Files changed

| File | Change |
|------|--------|
| `config/base/rbac/kustomization.yaml` | Added `replacements[]` for Secret annotation (Part 1) |
| `config/base/rbac/epp-metrics-token-secret.yaml` | Added comment explaining why the annotation value is correct in the base |

---

## Related code

- `internal/datastore/datastore.go:46` — reads the token from the mounted path
- `internal/datastore/datastore.go:131` — passes it as `BearerToken` to pod scraping source
- `internal/engines/scalefromzero/queue_fallback.go:19` — documents the failure modes
- `config/base/manager/deployment.yaml:97-106` — mounts the secret at `/var/run/secrets/epp-metrics`
- `config/base/rbac/epp-metrics-token-secret.yaml` — the fixed Secret definition
- `config/base/rbac/epp-metrics-reader-clusterrolebinding.yaml` — ClusterRoleBinding (no change needed)

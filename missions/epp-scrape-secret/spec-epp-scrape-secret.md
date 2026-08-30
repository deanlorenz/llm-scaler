# Spec — epp-scrape-secret: fix EPP metrics-endpoint auth breaking across installs

## Status

**Rewritten 2026-08-30 after live-cluster fact-finding overturned the original hypothesis.**
The mission's name and origin are unchanged; the root cause and fix are not what was
originally assumed. See "History" below for what changed and why.

## Intent

Fix a real, currently-live bug: on a shared cluster, installing or upgrading EPP
(`optimized-baseline-epp`, via `llm-d-router-standalone`) in **any** namespace silently
breaks the EPP metrics-endpoint authentication of **every other namespace's** EPP install
that predates it — with no error, no event, no restart. The WVA controller's direct EPP
pod-scrape then fails with `500` (not 401), which matters specifically for the
scale-from-zero wake signal's fallback path, and is why benchmark runs saw zero EPP signal.

## Origin note

This mission was opened after `benchmark-runtools` found and documented a symptom
(`epp*_metrics.log` files showing only `Unauthorized`/failed scrapes) but correctly treated
the fix as out of its own scope. The original write-up,
`missions/epp-scrape-secret/source-issue-epp-metrics-token-nameprefix.md` (a verbatim,
read-only copy of `benchmark-runtools`'s
`docs/plans/benchmark/issue-epp-metrics-token-nameprefix.md`), proposed a root cause and fix
around kustomize's `namePrefix` not rewriting the Secret annotation naming the EPP-reader
ServiceAccount, plus the ClusterRoleBinding's `subjects[].name`. This mission's own
fact-finding (live cluster + rendered-manifest checks, done before any code was touched)
found that hypothesis **not present on the actual cluster** — see "History" below. The real
root cause is unrelated to kustomize, `namePrefix`, or `config/base/rbac/` entirely.

## History

1. **Original hypothesis (source doc):** kustomize's `namePrefix: wva-` renames the
   `epp-metrics-reader` ServiceAccount to `wva-epp-metrics-reader` but allegedly leaves the
   Secret's `kubernetes.io/service-account.name` annotation and the ClusterRoleBinding's
   `subjects[].name` unprefixed, so the token controller can't bind the Secret and it stays
   empty. Proposed fix: add `replacements:` entries in `config/base/rbac/kustomization.yaml`
   plus per-overlay `namespace` patches for the ClusterRoleBinding subject.
2. **Static-render fact-finding (this mission, before any live-cluster check):** extracted
   `upstream/main` to a scratch dir and rendered all four overlays with both `kubectl
   kustomize` and standalone `kustomize build` (v5.6.0-class, matching what
   `deploy/lib/infra_wva.sh` actually invokes) — **with no fix applied**. Both the Secret
   annotation and the ClusterRoleBinding subject/namespace already rendered correctly.
   Modern kustomize's built-in `nameReference` transformer already handles this exact
   well-known annotation pattern. Reproduced through a simulated version of the real
   deploy pipeline's extra synthetic temp-overlay wrapping
   (`deploy/lib/common.sh:wva_prepare_overlay_base`) too.
3. **Live-cluster fact-finding (this mission, read-only `kubectl` against the real
   cluster):** checked `dhl-la-1708` (the exact namespace the source doc names) plus 15
   other namespaces with the same Secret — every one has a populated token and a correctly
   prefixed annotation. Checked ~50 `*-epp-metrics-reader-role-binding` ClusterRoleBindings
   cluster-wide — every subject is correct, including the exact one for `dhl-la-1708`. The
   source doc's `kubectl get sa ... 0 TOKENS` observation is a red herring: that column only
   counts legacy auto-mounted `.secrets[]` references (removed by default since Kubernetes
   1.24) and has nothing to do with a manually-created
   `kubernetes.io/service-account-token` Secret bound via annotation, which is what this is.
4. **Real root cause, found by reading actual logs instead of re-deriving from the doc's
   assumptions:** the WVA controller's log in `dhl-la-1708` shows `pod ... returned status
   500`, not 401 — a server-side failure on the EPP pod being scraped, not a WVA-side auth
   problem. The EPP pod's own log shows why: `Authentication failed ...
   tokenreviews.authentication.k8s.io is forbidden: User
   "system:serviceaccount:dhl-la-1708:optimized-baseline-epp" cannot create resource
   "tokenreviews" ... at the cluster scope`. `controller-runtime`'s built-in metrics-auth
   filter needs to call `TokenReview` to validate *any* bearer token presented to
   `/metrics` (WVA's or anyone else's) — and EPP's own ServiceAccount lacks the cluster-scoped
   RBAC to make that call.
5. **Why:** this repo already has the fix for exactly this —
   `deploy/lib/epp-tokenreview-rbac.yaml` (a `ClusterRole` granting
   `tokenreviews`/`subjectaccessreviews` create) applied by
   `deploy/lib/infra_epp.sh:deploy_epp()`, landed in commit `d9c68e69` (2026-05-22), well
   before `dhl-la-1708`'s EPP was created (2026-08-17). But the `ClusterRoleBinding` it
   creates — `optimized-baseline-epp-tokenreview` — is a **fixed, unsuffixed, cluster-scoped
   name**. Every EPP install anywhere on the cluster re-applies the same object, and
   `kubectl apply`/`kubectl create ... | kubectl apply -f -` on a ClusterRoleBinding
   **replaces its `subjects[]` list wholesale**, not additively. So the *last* namespace to
   install/upgrade `optimized-baseline-epp` anywhere on the shared cluster silently steals
   this permission from every earlier install. Confirmed directly: the live
   `optimized-baseline-epp-tokenreview` binding is currently owned by `dhl-e2e-231`, whose
   EPP install landed 2 days after `dhl-la-1708`'s.
6. This is a structural twin of a hazard this same codebase already recognized and fixed —
   **for WVA's own shared ClusterRoleBindings** — via `deploy/lib/common.sh`'s
   `wva_append_crb_name_patches`/`wva_ns_suffix`/`wva_delete_legacy_crbs`, whose comments
   describe this exact failure mode almost verbatim ("two installs shared one binding — the
   second apply replaced its subject list, so the first controller lost node access"). That
   fix was never extended to this EPP-side binding.
7. **Compounding finding:** `undeploy_epp()` in the same file unconditionally
   `kubectl delete`s both the fixed-name `ClusterRole` and `ClusterRoleBinding` — so
   uninstalling *any one* namespace's EPP install currently deletes this permission for
   *every* namespace on the cluster, not just steals it. Same root cause, worse blast radius
   on the uninstall path.

## Problem statement (current, correct)

- `deploy/lib/epp-tokenreview-rbac.yaml` defines `ClusterRole/optimized-baseline-epp-tokenreview`.
- `deploy/lib/infra_epp.sh`'s `deploy_epp()` applies that ClusterRole, then runs
  `kubectl create clusterrolebinding optimized-baseline-epp-tokenreview
  --clusterrole=optimized-baseline-epp-tokenreview
  --serviceaccount="$NAMESPACE:optimized-baseline-epp" --dry-run=client -o yaml | kubectl
  apply -f -` — a **fixed name**, cluster-scoped, one copy on the whole cluster.
- `undeploy_epp()` unconditionally deletes both fixed-name objects.
- Net effect on a shared cluster (many namespaces, many independent WVA/EPP installs):
  - **Install/upgrade hazard:** the newest EPP install anywhere on the cluster silently
    becomes the sole subject of the binding; every earlier install's EPP starts returning
    `500` on `/metrics` the moment any *other* namespace's EPP is installed or upgraded.
  - **Uninstall hazard:** uninstalling any one namespace's EPP deletes the ClusterRole and
    ClusterRoleBinding outright, breaking every other namespace's EPP metrics auth
    immediately, regardless of install order.
  - No error, event, or restart accompanies either hazard. The controller stays `Running`;
    only the `/metrics` scrape (and, for WVA specifically, the scale-from-zero fallback that
    depends on it) silently stops working.

## Success criterion

- `deploy_epp()` creates a **per-namespace** `ClusterRole` + `ClusterRoleBinding` (suffixed
  the same way `wva_append_crb_name_patches`/`wva_ns_suffix` already suffix WVA's own shared
  bindings), so two independent EPP installs on the same cluster never share one binding.
- `undeploy_epp()` deletes only *this installation's own* suffixed objects — never another
  namespace's.
- An upgrade path exists (or is explicitly deemed unnecessary and documented) for
  clusters that already carry the old fixed-name `optimized-baseline-epp-tokenreview`
  ClusterRole/ClusterRoleBinding, so an existing install isn't left with two competing
  RBAC objects granting the same thing under different names with no cleanup.
- After `make deploy-wva-on-k8s`/`-on-openshift` in two different namespaces on the same
  cluster (or the closest available equivalent — see T4), both EPP pods keep returning
  `200` on `/metrics` when scraped with a valid bearer token, and neither install's
  uninstall affects the other's.

## Plan / Todo

### T1 — Mission + worktree scaffolding
**Intent.** Stand up this mission's tracking and its own fix worktree, off `upstream/main`,
separate from `benchmark-runtools`.
**Expected outcome(s).** `missions/epp-scrape-secret/` exists (spec, `STATE.md`, `ledgers/`)
in `session-tracking`; `worktrees/epp-scrape-secret` exists as a worktree branched from
`upstream/main`.
**Todo.**
- [x] Create `missions/epp-scrape-secret/` (spec, `STATE.md`, `ledgers/`)
- [x] Fact-find before touching any code (static render + live cluster) — see "History"
- [x] Rewrite this spec around the confirmed root cause (this edit)
- [ ] Create `worktrees/epp-scrape-secret` off `upstream/main` (`cd`, not `EnterWorktree` —
      per user instruction, stay unpinned so `session-tracking` remains reachable)
**Refs.** *Writes:* `missions/epp-scrape-secret/**`, `worktrees/epp-scrape-secret/**`.
**Status.** IN PROGRESS.

### T2 — Design the per-namespace suffix fix for the EPP tokenreview binding
**Intent.** Match the existing, already-proven convention
(`wva_ns_suffix`/`wva_append_crb_name_patches`/`wva_delete_legacy_crbs` in
`deploy/lib/common.sh`) rather than inventing a new one.
**Expected outcome(s).** A concrete diff plan for:
- `deploy/lib/epp-tokenreview-rbac.yaml` and/or `deploy/lib/infra_epp.sh`'s `deploy_epp()`:
  suffix both the ClusterRole and ClusterRoleBinding names with `wva_ns_suffix "$NAMESPACE"`
  (or the EPP-relevant namespace variable in scope at that point — confirm which — WVA's own
  installs use `$WVA_NS`; EPP's install namespace here is `$NAMESPACE`, confirm they're the
  same concept before reusing the helper as-is).
- `undeploy_epp()`: delete only the suffixed name for this install's namespace, not the
  fixed name.
- Decide what (if anything) to do about the existing fixed-name objects already on shared
  clusters (leave them, since they're harmless once nothing recreates or deletes them
  cluster-wide, and a fresh per-install object takes over the actual authorization work —
  or actively clean them up once, whichever `deploy_epp()`'s idempotency model prefers —
  needs a decision here, not an assumption).
**Todo.**
- [ ] Confirm `$NAMESPACE` in `infra_epp.sh` at the call site is the same value as
      `$WVA_NS`/the per-install namespace `wva_ns_suffix` is keyed on elsewhere (read
      `deploy/install.sh` call chain to confirm, don't assume)
- [ ] Decide the ClusterRole naming: does the Role need suffixing too, or only the Binding?
      (WVA's own `WVA_OWNED_CLUSTER_ROLES` suffixes both Role and Binding — check whether
      EPP's tokenreview ClusterRole has the same cross-tenant identical-rules property that
      makes WVA's manager-role safe to suffix, i.e. whether leaving it unsuffixed and shared
      is actually fine since the rules never differ across installs)
- [ ] Write the exact diff for `epp-tokenreview-rbac.yaml`, `infra_epp.sh` (`deploy_epp`,
      `undeploy_epp`)
**Refs.** *Reads:* `deploy/lib/common.sh`, `deploy/lib/infra_epp.sh`, `deploy/lib/epp-tokenreview-rbac.yaml`,
`deploy/install.sh`. *Writes:* none yet (design only).
**Status.** NOT STARTED.

### T3 — Implement in the fix worktree
**Intent.** Land the T2 design.
**Expected outcome(s).** `deploy_epp()`/`undeploy_epp()` use per-namespace-suffixed
ClusterRole/ClusterRoleBinding names; existing WVA-side suffixing is untouched.
**Todo.**
- [ ] Implement the diff from T2
- [ ] Self-review the diff before asking for external review
**Refs.** *Writes:* `deploy/lib/infra_epp.sh`, `deploy/lib/epp-tokenreview-rbac.yaml` (if the
Role itself needs a rename/templating mechanism).
**Status.** NOT STARTED.

### T4 — Verify
**Intent.** Prove the fix by the concrete success criterion above.
**Expected outcome(s).** Two independent namespaces' EPP installs on the same cluster
coexist without one breaking the other's `/metrics` auth; an uninstall of one leaves the
other's binding intact.
**Todo.**
- [ ] Simulate or directly test two-namespace install/uninstall interaction
- [ ] Decide whether/how to also verify against the real shared cluster already in a broken
      state (multiple existing namespaces currently missing this permission) — a live
      remediation is a separate, more sensitive action from shipping the code fix; do not
      conflate the two without asking the user first
**Refs.** *Reads:* deployed cluster state. *Writes:* none (verification only), unless the
user separately approves a live remediation step.
**Status.** NOT STARTED.

## Related code

- `deploy/lib/epp-tokenreview-rbac.yaml` — the ClusterRole granting `tokenreviews`/
  `subjectaccessreviews` create, currently applied under a fixed name
- `deploy/lib/infra_epp.sh:deploy_epp()` (around line 150) — applies the ClusterRole, creates
  the fixed-name ClusterRoleBinding
- `deploy/lib/infra_epp.sh:undeploy_epp()` — unconditionally deletes both fixed-name objects
- `deploy/lib/common.sh:wva_ns_suffix()`, `wva_append_crb_name_patches()`,
  `wva_delete_legacy_crbs()` — the existing, proven per-namespace-suffix convention this fix
  should reuse rather than reinvent
- `internal/datastore/datastore.go:46,131` — WVA's own token read/use (was the original,
  now-ruled-out suspect; not part of the actual fix)
- `internal/engines/scalefromzero/queue_fallback.go:19` — documents the downstream impact
  when the EPP direct-scrape path is unavailable

All paths above are repo-root-relative, resolved against `upstream/main` /
`worktrees/epp-scrape-secret`.

## Out of scope

- `config/base/rbac/**` and `config/overlays/*/kustomization.yaml` — the original suspects,
  confirmed not broken (see "History"). No changes planned there.
- `worktrees/benchmark-runtools/**` — not this mission's to touch, per explicit user
  instruction.
- Any live remediation of the currently-broken namespaces on the shared cluster (e.g.
  reapplying the CRB for `dhl-la-1708` by hand) — a separate, more sensitive action than
  shipping the code fix; not undertaken without a separate explicit ask.

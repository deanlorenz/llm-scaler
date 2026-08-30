# Ledger — epp-scrape-secret-initial — 2026-08-30

- Mission opened by user: benchmark-runtools found+documented an EPP-metrics-token bug
  (namePrefix not rewriting the Secret's SA annotation / ClusterRoleBinding subject), fix
  is a separate mission. User explicit direction: fact-find and verify the proposed fix
  first, discuss, get approval, only then implement. Do not touch benchmark-runtools.
- Early misstep (self-corrected): opened worktrees/session-tracking/missions/agentbus's spec
  file because it was the IDE-open file at turn start — not my mission's business, user
  called this out directly. Also initially misdescribed the ledger mechanism as two separate
  things ("a ledger + a tracked ledger in session-tracking") — corrected after reading
  CONVENTIONS.md: it is one ledger, local-scratch during the session, copied verbatim into
  session-tracking at checkpoint/session-end.
- Read worktrees/session-tracking/CONVENTIONS.md in full before creating anything, per the
  user's correction ("you should have read CONVENTIONS").
- Created missions/epp-scrape-secret/ (spec, STATE.md, ledgers/) in session-tracking. Copied
  the original issue doc verbatim into
  missions/epp-scrape-secret/source-issue-epp-metrics-token-nameprefix.md so this mission
  does not depend on benchmark-runtools's tree.
- Confirmed repo remotes: origin (push-enabled, deanlorenz fork), upstream (push disabled,
  ev-shindin/llm-scaler — DISABLED-no-push), ofer (push disabled) — matches
  CONVENTIONS.md's documented remote convention. upstream/main resolves locally already
  (f0bc16462eb7f191e82a6edc7d975d531feda008) — no fetch was needed.
- Next: fact-finding pass (spec's T2) — verify the source doc's proposed kustomize fix
  against the actual current file content on upstream/main, and confirm kustomize version
  used by the make deploy-wva-on-* targets, before proposing anything to the user.
- Confirmed via `git show upstream/main:config/base/rbac/*.yaml` that the four RBAC files
  (kustomization.yaml, epp-metrics-serviceaccount.yaml, epp-metrics-token-secret.yaml,
  epp-metrics-reader-clusterrolebinding.yaml) match the source doc's claims exactly: no
  `replacements` block, hardcoded unprefixed annotation/subject name, no `namespace` field
  on the CRB subject.
- Confirmed all four overlays (`config/overlays/{namespace,cluster}-scoped/{openshift,kubernetes}/kustomization.yaml`)
  set `namePrefix: wva-` and `namespace: wva-system` (a placeholder rewritten by
  deploy/install.sh to $WVA_NS) — matches doc's claim on namePrefix; the doc did not mention
  the `namespace:` field, which turned out to matter (see finding below).
- Traced the ACTUAL deploy path: `make deploy-wva-on-{k8s,openshift}` -> deploy/install.sh ->
  deploy/lib/infra_wva.sh:deploy_wva_controller, which calls `kubectl kustomize` (not the
  Makefile's pinned standalone `sigs.k8s.io/kustomize/kustomize/v5` binary) against a
  SYNTHETIC temp overlay (deploy/lib/common.sh:wva_prepare_overlay_base) that symlinks
  `./base` to the real overlay dir and wraps it with its own `namespace: $WVA_NS` +
  image-pin + `patches:` layer. So the real pipeline is: config/base (innermost) -> real
  overlay (namePrefix+namespace) -> synthetic temp overlay (namespace again, image, CRB
  suffix patches) -> `kubectl kustomize`.
- IMPORTANT existing-code finding: deploy/lib/common.sh:wva_append_crb_name_patches already
  patches `wva-epp-metrics-reader-role-binding`'s `metadata.name` (appends a per-namespace
  suffix, via WVA_SHARED_CLUSTER_ROLE_BINDINGS) and its ClusterRole's `roleRef.name` (via
  WVA_OWNED_CLUSTER_ROLES) -- but does NOT touch `subjects[].name` or `subjects[].namespace`.
  This is a different, pre-existing rename mechanism from anything in the source issue doc,
  and it means the CRB's own metadata.name is NOT the fixed `wva-epp-metrics-reader-role-binding`
  by the time it's actually applied -- it's suffixed. Relevant to whether/how a `replacements`
  target-by-name in config/base/rbac/kustomization.yaml would still resolve (it should, since
  it runs at the base layer before this outer suffix rename) -- but this needs to stay in mind
  for T2/T3.
- Confirmed local tooling: `kubectl` v1.33.3 embeds kustomize v5.6.0 (exact version match to
  Makefile's `KUSTOMIZE_VERSION ?= v5.6.0`); standalone `kustomize` binary on PATH is v5.8.1.
- **MAJOR FINDING, contradicts the source issue doc's stated root cause:** extracted
  upstream/main into a scratch dir (/tmp/epp-fact-find/upstream-main, read-only, git-archive
  of upstream/main, no worktree) and rendered all four overlays with BOTH `kubectl kustomize`
  and standalone `kustomize build`, with NO fix applied (source files exactly as they are on
  upstream/main today). Result, consistent across all four overlays and also reproduced
  through a simulated version of the real synthetic temp-overlay wrapping
  (wva_prepare_overlay_base): the Secret's `kubernetes.io/service-account.name` annotation
  DOES already render correctly as `wva-epp-metrics-reader` (not the unprefixed
  `epp-metrics-reader` the doc claims), and the ClusterRoleBinding's `subjects[].name` /
  `subjects[].namespace` ALSO already render correctly as `wva-epp-metrics-reader` /
  `wva-system`. This is kustomize's built-in `nameReference` transformer already handling
  this exact well-known annotation/field pattern -- no `replacements` block needed at all, at
  least not for what the rendered YAML shows.
- This means: if the token really is empty on a live cluster (dhl-la-1708, per the doc), the
  cause is NOT what the source doc identifies at the manifest-rendering level. Candidate
  explanations not yet investigated: (a) an older kustomize version was in effect at the time
  of that specific install (kubectl client version drift across machines/CI), (b) the Secret
  was created once under a stale/broken annotation and never got re-applied after a
  kustomize/kubectl upgrade so the live object is stale relative to what current source now
  renders, (c) something about wva_append_crb_name_patches's CRB-name-suffixing interacting
  with roleRef immutability (see wva_repair_immutable_rolerefs) that's unrelated to the
  Secret path entirely, (d) a real bug elsewhere not yet found. Have NOT yet reproduced the
  bug live on a cluster -- this is a static-render analysis only.
- Decision point: bringing this back to the user now rather than continuing further, since it
  materially changes what (if anything) needs fixing, and the user asked to discuss findings
  before any implementation.
- User directed: look at other namespaces (read-only) to figure out why the cluster (real
  live cluster, kubectl context already pointed at it) was not configured correctly. Did NOT
  create any worktree; all further investigation was read-only kubectl against the live
  cluster plus git history reads. No writes to any cluster object.
- Live-cluster check on dhl-la-1708 (the exact namespace the source doc cites): the
  wva-epp-metrics-token Secret IS populated (1728 bytes, not empty), and its
  `kubernetes.io/service-account.name` annotation already correctly reads
  `wva-epp-metrics-reader` (prefixed), with UID matching the actual SA's UID exactly
  (0e34dc57-f025-437e-b50a-8e67dcc9d7af). Checked 11 more namespaces with the same secret
  name across the cluster (biran-pd, biran, dhl-e2e-231, evgensh-bench, evgensh-guide4,
  evgensh-osc, evgensh-wva-test, kelly-benchmarking, moabdi-oc-test-env, moabdi-test-wva,
  llm-d-fma-test) plus 4 more under the older `workload-variant-autoscaler-` naming
  (dhl-wva-209, dolev-llmd, llm-d-autoscaler, llm-d-wva-yottnm) -- ALL populated, ALL
  correctly annotated. Also dumped every ClusterRoleBinding cluster-wide whose name contains
  "epp-metrics-reader" (~50 across every WVA/keda/prometheus install on the cluster) --
  subjects[].name and subjects[].namespace are correct in every single one, including
  wva-epp-metrics-reader-role-binding-af9fd8b3 -> {ServiceAccount wva-epp-metrics-reader,
  namespace dhl-la-1708}, the exact binding for the exact namespace the doc calls broken.
  CONCLUSION: the source issue doc's claimed root cause (namePrefix not rewriting the
  Secret annotation / CRB subject) is NOT present anywhere on this live cluster, and my
  earlier static-render finding (kustomize's built-in nameReference transformer already
  handles this) is corroborated by live cluster state, not just theory. `kubectl get sa`
  showing "0 TOKENS" for wva-epp-metrics-reader is a red herring the doc likely misread as
  evidence of failure -- that column only counts legacy auto-mounted `.secrets[]` refs
  (removed by default since k8s 1.24), unrelated to a manually-created
  kubernetes.io/service-account-token Secret bound purely via annotation.
- Found the REAL root cause by reading the WVA controller's own log in dhl-la-1708 instead of
  re-deriving from the doc's assumptions: `pod/pod_scraping_source.go:244 Failed to scrape
  pod ... error: pod ... returned status 500` -- NOT 401. So WVA's own auth (bearer token it
  presents) is fine; the failure is server-side, on the EPP pod being scraped.
- EPP pod's own log (optimized-baseline-epp-749fc84cf8-xwnfj) shows the actual 500 cause:
  `Authentication failed ... tokenreviews.authentication.k8s.io is forbidden: User
  "system:serviceaccount:dhl-la-1708:optimized-baseline-epp" cannot create resource
  "tokenreviews" ... at the cluster scope`. This is controller-runtime's own metrics-endpoint
  auth filter (sigs.k8s.io/controller-runtime/pkg/metrics/filters) -- EPP needs to call
  TokenReview to validate the bearer token WVA presents, and EPP's OWN ServiceAccount lacks
  the cluster-scoped RBAC to do that call. This is the OPPOSITE direction from what the
  source doc investigated (doc looked at whether WVA's token was valid/readable; the actual
  gap is whether EPP can validate ANY presented token, WVA's or otherwise).
- This repo already has the fix for exactly this, and already applies it normally:
  deploy/lib/epp-tokenreview-rbac.yaml (a ClusterRole granting
  tokenreviews/subjectaccessreviews create) + deploy/lib/infra_epp.sh:150's deploy_epp()
  applies it and creates `clusterrolebinding optimized-baseline-epp-tokenreview` via `kubectl
  create clusterrolebinding ... --dry-run=client -o yaml | kubectl apply -f -` under a FIXED,
  UNSUFFIXED name. Comment in the code literally says "so its metrics endpoint authentication
  works (otherwise /metrics returns 500)" -- matches the live symptom exactly. Landed in
  commit d9c68e69 (2026-05-22), well before dhl-la-1708's EPP was created (2026-08-17,
  confirmed via `helm history`/deployment creationTimestamp) -- so this is not a
  predates-the-fix install.
- ROOT CAUSE, confirmed: `optimized-baseline-epp-tokenreview` is a CLUSTER-SCOPED
  ClusterRoleBinding created under a FIXED name with NO per-namespace suffix (unlike WVA's
  own shared ClusterRoleBindings, which deploy/lib/common.sh:wva_append_crb_name_patches
  deliberately suffixes per-namespace for exactly this reason -- see that function's own
  comments about two installs stripping each other's permissions). Every EPP install
  anywhere on the cluster creates/re-applies this SAME cluster-scoped object, and `kubectl
  apply`/`kubectl create ... | kubectl apply -f -` on a ClusterRoleBinding REPLACES its
  subjects list wholesale. So the LAST namespace to install/upgrade optimized-baseline-epp
  anywhere on the cluster silently steals this permission from every earlier install, with
  no error, no event, no restart -- an exact structural twin of the hazard WVA's own binding
  code already protects against, just not applied to this one.
  Verified directly: `kubectl get clusterrolebinding optimized-baseline-epp-tokenreview -o
  jsonpath='{.subjects}'` -> currently owned by namespace dhl-e2e-231 (creationTimestamp
  2026-08-19, i.e. re-stamped 2 days after dhl-la-1708's own EPP install on 2026-08-17).
  Cross-checked 5 other namespaces sharing this exact ClusterRole/Binding name
  (dhl-e2e-231, llm-d-fma-test, amit, mohamed-op-baseline,
  dpikus-opt-base-standalone-pr1463) -- dhl-la-1708 is the only one of the six currently
  MISSING as a subject, consistent with being the earliest of the six and thus the one
  bumped by every later install.
- **This means the actual fix belongs in deploy/lib/infra_epp.sh / a rename of the
  ClusterRole+ClusterRoleBinding in deploy/lib/epp-tokenreview-rbac.yaml to be per-namespace
  (suffixed the same way wva_append_crb_name_patches already suffixes WVA's own shared
  bindings), NOT in config/base/rbac/kustomization.yaml, and has nothing to do with
  namePrefix/replacements/kustomize at all.** The source issue doc's entire premise (Parts
  1-3, all of them) targets the wrong subsystem. This is a full pivot, not a refinement of
  the original plan -- needs explicit discussion with the user before any spec/plan rewrite.
- User approved proceeding: rewrite the spec, create the fix worktree (cd only, never
  EnterWorktree, so session-tracking stays reachable), implement, self-review, all inside
  that worktree. Explicit constraints: no push to origin, no GitHub activity of any kind
  (PRs/issues/comments), no code changes outside the fix worktree. User is going to sleep --
  continue and report back, don't wait on them.
- Rewrote missions/epp-scrape-secret/spec-epp-scrape-secret.md in full: added a "History"
  section documenting the pivot (original hypothesis -> static-render finding -> live-cluster
  finding -> real root cause -> why this codebase already has a proven fix pattern for the
  identical hazard -> the undeploy-side compounding finding), rewrote the problem statement,
  success criterion, and T1-T4 plan around the real fix. Marked config/base/rbac/** and
  config/overlays/*/kustomization.yaml explicitly "Out of scope" so a future reader doesn't
  reintroduce the original (wrong) fix. Updated STATE.md's Current status /
  Immediate next step / Worktrees used sections to match.
- Created worktrees/epp-scrape-secret via `git worktree add worktrees/epp-scrape-secret -b
  epp-scrape-secret upstream/main` (plain `cd`, never EnterWorktree, per user instruction --
  this session stays unpinned). Set up the resume-mission/wind-down skill symlinks per
  CONVENTIONS.md's one-time-per-worktree setup; confirmed they resolve; confirmed
  .git/info/exclude already covers both paths globally (no new exclude entries needed).
  Confirmed remotes in the new worktree match the documented convention (upstream/ofer push
  DISABLED-no-push, origin push-enabled but NOT used this session per user instruction).
- T2 investigation (read-only, in the new worktree): confirmed `$NAMESPACE` (EPP's own
  install namespace, default llm-d-optimized-baseline) and `$WVA_NS` (WVA controller's own
  namespace, default workload-variant-autoscaler-system) are genuinely DIFFERENT namespace
  concepts in deploy/install.sh -- EPP installs into $NAMESPACE, not $WVA_NS. Confirmed
  wva_ns_suffix() is namespace-string-agnostic (already called with a local $ns in
  physical_limiter.sh, not hardcoded to $WVA_NS) so reusing it for $NAMESPACE is safe and
  consistent, no structural conflict.
- Decided (via WVA_OWNED_CLUSTER_ROLES's own comment: roles are suffixed defensively even
  though identical rules currently make an unsuffixed share a no-op, specifically so future
  rule drift can't silently cross-contaminate installs) to suffix BOTH the ClusterRole and
  the ClusterRoleBinding for epp-tokenreview, matching the stronger of the two existing
  precedents rather than the weaker "rules never differ so it's fine to share" reasoning.
- Implemented the fix in deploy/lib/infra_epp.sh (deploy_epp: yq-rewrite
  epp-tokenreview-rbac.yaml's metadata.name to a per-namespace-suffixed name before `kubectl
  apply`, same suffix used for the `kubectl create clusterrolebinding` call already there;
  undeploy_epp: delete only this install's own suffixed names, explicitly NOT the old
  fixed name -- deleting that unconditionally would repeat the exact cross-tenant breakage
  this fix closes, just via delete instead of apply, since some other still-live namespace
  may currently be the sole holder of that fixed-name object) and added a template-comment
  to deploy/lib/epp-tokenreview-rbac.yaml explaining the name gets rewritten at apply time.
  Style follows infra_wva.sh's existing yq/strenv() idiom exactly (same pattern already used
  there for a different in-flight rename).
- Self-review: `shellcheck deploy/lib/infra_epp.sh` clean (no findings). Verified the yq
  rewrite produces valid, correctly-renamed YAML by running it directly against the real
  file. Verified wva_ns_suffix (extracted standalone, since sourcing common.sh directly
  failed silently -- likely has unmet dependencies when not sourced via install.sh's full
  chain, didn't chase why since call-site behavior is what matters) produces
  af9fd8b3 for "dhl-la-1708" -- EXACTLY matches the real suffix already observed live on
  that namespace's own WVA ClusterRoleBinding (wva-epp-metrics-reader-role-binding-af9fd8b3),
  confirming the hash function's determinism matches production. Confirmed full suffixed
  name length (43 chars) is well under Kubernetes' 253-char object-name limit and the
  63-char label-safe limit. Grepped the whole worktree for any other reference to the old
  fixed name `optimized-baseline-epp-tokenreview` -- found none outside the 3 files touched
  (no docs, tests, or prereqs/verify.sh checks assume the old name). Confirmed deploy_epp/
  undeploy_epp are called only from deploy/install-epp.sh and deploy/lib/cleanup.sh, both of
  which source common.sh (defines wva_ns_suffix) before infra_epp.sh/cleanup.sh runs -- no
  new sourcing requirement introduced. Confirmed EPP install/uninstall is a fully separate
  entry point from WVA's own phase-split permission model (install-epp.sh, not part of
  install.sh's prereqs/wva phases) -- this change doesn't interact with or need to touch
  that boundary.
- Nothing committed yet. No push, no GitHub activity, per explicit user instruction. No
  writes outside worktrees/epp-scrape-secret (config work) and
  worktrees/session-tracking/missions/epp-scrape-secret/** (tracking work) this whole
  session.
- Next: decide whether to commit this diff in the fix worktree (small, self-contained,
  probably fine to commit locally without pushing -- but confirm with user first since no
  standing "commit freely" instruction was given, only "no push/no GH"), then continue with
  T4 (verification plan) once user is back.
- Deviation from CONVENTIONS.md noted at wind-down: this session wrote directly into this
  session-tracking ledger file throughout, rather than keeping a separate local scratch copy
  under worktrees/epp-scrape-secret/.session/ and copying it here only at checkpoints. Content
  itself stayed current (append-as-you-go, not batched) -- only the filing mechanics deviated,
  not the "write live, not retroactively" principle the convention actually protects. Flagging
  so a future session (or ledger-capture) doesn't need to re-derive this.
- Wind-down starting now. Nothing left mid-edit; no background agents were launched this
  session. Diff in worktrees/epp-scrape-secret is uncommitted (deliberately, pending explicit
  confirmation from the user per the note above) -- next session/resume should check
  `git status` there before assuming a clean worktree.

# STATE — epp-scrape-secret

## Current status

**Phase: implementing, in the fix worktree.** Fact-finding (static kustomize render +
live-cluster investigation, both read-only) overturned the original root-cause hypothesis
from `benchmark-runtools`'s bug report. The real bug: `deploy/lib/infra_epp.sh`'s
`deploy_epp()` creates a fixed-name, cluster-scoped `ClusterRoleBinding`
(`optimized-baseline-epp-tokenreview`) granting the EPP pod's own ServiceAccount permission
to call `TokenReview` (needed for its `/metrics` endpoint's own auth) — and because the name
is fixed and cluster-scoped, every EPP install anywhere on a shared cluster overwrites the
previous one's subject, silently breaking that earlier install's `/metrics` auth (500, not
401). `undeploy_epp()` makes it worse by unconditionally deleting the shared object outright.
Confirmed live: `dhl-la-1708`'s EPP lost this permission when `dhl-e2e-231`'s EPP was
installed 2 days later. See `missions/epp-scrape-secret/spec-epp-scrape-secret.md` — full
"History" section documents the pivot — for the complete plan (T1–T4).
`missions/epp-scrape-secret/source-issue-epp-metrics-token-nameprefix.md` remains the
original bug report (verbatim copy, read-only, superseded by the spec's findings — do not
edit).

The fix reuses an existing, proven convention already in this codebase for the structurally
identical problem on WVA's *own* shared ClusterRoleBindings: `deploy/lib/common.sh`'s
`wva_ns_suffix`/`wva_append_crb_name_patches`/`wva_delete_legacy_crbs`.

**User directive for this phase (2026-08-30):** code work happens only inside
`worktrees/epp-scrape-secret` — reached via plain `cd`, not `EnterWorktree` (staying
unpinned so `session-tracking` stays reachable without a round-trip). No pushing to
`origin`, no GitHub activity (PRs, issues, comments) of any kind this phase. No code changes
outside that worktree.

## Immediate next step

T2 and T3 are done: `deploy/lib/infra_epp.sh`'s `deploy_epp()`/`undeploy_epp()` now use a
per-namespace-suffixed (`wva_ns_suffix "$NAMESPACE"`) name for both the ClusterRole and
ClusterRoleBinding, matching the existing `wva_append_crb_name_patches` convention.
`undeploy_epp()` deletes only this install's own suffixed copies, deliberately never the old
fixed name (which may currently be the sole permission source for some other, still-live
namespace). Self-reviewed: `shellcheck` clean, `yq` rewrite verified against the real file,
suffix determinism cross-checked against the real live cluster's own
`wva-epp-metrics-reader-role-binding-af9fd8b3` for `dhl-la-1708`. Nothing committed yet — no
push, no GitHub activity, per explicit user instruction (in effect for this whole phase).

**Next, once the user is back:** confirm whether to commit this diff locally in the fix
worktree (small, self-contained — no standing "commit freely" instruction was given, only
"no push/no GH"), then move to T4 (verification — two-namespace install/uninstall
interaction; decide separately, with the user, whether/how to touch the real cluster's
already-broken namespaces).

## Worktrees used

`worktrees/epp-scrape-secret` — branched off `upstream/main`. Entered via `cd`, not
`EnterWorktree` (per user instruction) — this session stays unpinned throughout.

## Scope boundary

- This mission never edits anything under `worktrees/benchmark-runtools/`. That worktree
  found and documented this bug; it is not this mission's to touch.
- This mission's own writes are confined to `missions/epp-scrape-secret/**` in
  `session-tracking`, plus (once created and approved) `worktrees/epp-scrape-secret/**`.

## Session log

- 2026-08-30T00:00 session=epp-scrape-secret-initial status=active ledger=ledgers/2026-08-30-epp-scrape-secret-initial.md

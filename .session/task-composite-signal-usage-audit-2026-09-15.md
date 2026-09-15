# Task: full CompositeSignal usage audit against 7 user-supplied criteria

- **In:** `composite-analyzer.usage-audit.in`
- **Out:** `composite-analyzer.usage-audit.out`
- **Name:** `2026-09-15-usage-audit-1`
- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`, `conventions/agentbus.md`
- **What / goal / mission:** Two prior passes on this mission looked only at PRC-touching call
  sites of the composite `NamedAnalyzerResult` (`req.CompositeSignal`). The mission owner's user
  corrected that scope: review ALL usage of `CompositeSignal` — supply, demand, and RC/SC
  consumers alike — against 7 specific criteria below, and the corrections already made to the
  PRC-only pass. This is READ-ONLY investigation and a written recommendation. No code changes.
- **Worktree / Path / Branch:** same-worktree — operate directly in
  `/home/dean/code/llm-d/dean-llmd-scaler-sandbox/worktrees/composite-analyzer`, branch
  `composite-analyzer`. Do not create a separate worktree or branch.
- **Startup verification instructions:** confirm `git branch --show-current` prints
  `composite-analyzer` and that `.session/findings-composite-prc-downstream-2026-09-15.md` and
  `.session/recommendations-composite-prc-guarding-2026-09-15.md` exist before starting — read
  both fully first; they are your starting point, not to be duplicated. If either is missing,
  stop and report on `Out:`.
- **Role / scope:** investigation + recommendation worker. You may READ any file in this
  worktree. You WRITE ONLY your own ledger and one output file (path below) — no code changes,
  no STATE.md edits, no edits to any spec/draft doc.
- **Ledger / log:** `.session/2026-09-15-usage-audit-1.md` — create on first write.

## Required reading before starting

1. `.session/findings-composite-prc-downstream-2026-09-15.md` — the original downstream-usage
   map (PRC-focused; treat its call-chain/hop data as reusable, but its scope and one framing
   claim are corrected by (3) below).
2. `.session/recommendations-composite-prc-guarding-2026-09-15.md` — the mission owner's
   first recommendation pass. **This pass has been superseded by user corrections — do not
   treat its conclusions as settled.** Read it for the code-location groundwork (file:line
   citations are still useful), not for its verdicts.
3. This exact list of corrections the user gave, verbatim — treat each as a hard constraint on
   your analysis, not a suggestion:
   1. Review ALL usage of the composite `CompositeSignal`/`NamedAnalyzerResult`, not just
      PRC-touching call sites — demand-only and RC/SC-only consumers must be checked too.
   2. Functions that consider **supply** must guard: a fallback PRC is a guesstimate at best,
      and the typical case needing the fallback is when no healthy replica exists for that SO —
      so it is safe to estimate that SO's supply contribution as 0 in that case.
      **Correction on scope of the 0-estimate:** supply is computed ACROSS SOs (summed) — the
      0 estimate applies to the specific no-signal SO's own term in that sum, not to the whole
      model/request's supply. Other SOs in the same sum keep their own real values.
   3. Functions that consider **demand** should NOT care much about per-SO information. Even if
      an analyzer someday uses per-SO metrics to estimate demand (no analyzer currently does),
      the decision path is not a per-SO concept for demand. NEVER fall back on demand if it is
      broken. Since demand is `D_sat[role]` (per-role, not per-SO), the only question that
      matters for demand is whether that per-role value itself is broken (e.g. a bad PromQL
      filter) — an error there means the result is unusable for that role, period, not
      something to estimate a fallback for.
   4. `buildCapacities` is NOT a downstream consumer — it is part of composite construction
      itself (same phase as `composite.go`'s per-SO loop). The first audit wrongly treated it as
      a "root of propagation" downstream site. Since it only computes supply-side aggregates
      (`SumTotalSupply`/`SumTotalAnticipatedSupply`), criterion 2 applies to it directly — it is
      not a separate case requiring separate reasoning.
   5. `replicasForDemand`'s existing `prc<=0→0` guard is not the real question. The real
      question is whether the CALLER should be asking for a replica estimate at all for a
      broken/no-signal SO. In most cases, an erroneous SO cannot estimate this at all, and the
      caller should guard BEFORE calling — should not even consider scaling that SO. The one
      legitimate exception: a genuine what-if/hypothetical question — the common real case is
      **partial-scale-from-zero**: currently 0 replicas, so PRC cannot be accurately measured
      from live data, but the caller still legitimately wants an estimate of what replica count
      *would* be needed. Distinguish "caller has no business asking" from "caller is
      legitimately asking a what-if" at every call site.
   6. `safeReplicasForSpare` should be conservative: no good signal → no spare (i.e. it should
      NOT fall back to a guesstimate the way replicasForDemand's what-if case might legitimately
      want to; removal safety margin must default to 0 when the signal backing it is not real).
   7. `prcForVariant` (and its sibling `prcFromVCs`) is a technical lookup whose correctness
      depends on the SPECIFIC CALLER'S INTENT — whether that caller wants ONLY the real measured
      value, or is deliberately asking a what-if that should accept the fallback value too. The
      mission owner's prior recommendation to unify the two lookup functions into one was WRONG
      per the user — collapsing them would erase exactly the distinction that matters. The right
      fix is likely separate, explicitly-named accessor functions where it is clear at the call
      site whether fallback is being allowed or refused — but this needs analysis per call site,
      not an assumed answer. Most callers of `prcForVariant`/`prcFromVCs` probably actually want
      `replicasForDemand`'s question (a replica count), not a raw PRC value — verify this
      per-caller, don't assume.

## What to produce

A full audit, structured by CONSUMER CATEGORY first (supply / demand / RC-SC / other), not by
file. For every call site that reads `CompositeSignal`/composite `NamedAnalyzerResult` data
(reuse and re-verify the call-chain map from `findings-composite-prc-downstream-2026-09-15.md`
as a starting point — re-check each file:line citation against current code, do not trust it
blindly), classify it:

1. **Category** — supply-related, demand-related, RC/SC-related (derived from both), or
   identity/metadata (ReplicaCount, PendingReplicas — not PRC/demand math).
2. **Caller's actual intent** — does this call site need "only a real, decision-path-backed
   value" or is it a legitimate what-if (name the specific scenario, e.g.
   partial-scale-from-zero) that should accept a fallback? If you cannot tell from the code
   alone, say so explicitly rather than guessing — this is exactly the kind of judgment call to
   flag, not resolve unilaterally.
3. **Current behavior** — what does it do today (gated how, or not at all)?
4. **Recommended behavior**, per the corrected criteria above — cite which criterion (2/3/5/6/7)
   drives the recommendation for this specific site.
5. For `prcForVariant`/`prcFromVCs` specifically: go through EVERY current caller
   individually (`applyAllocation`, `roleBottleneckReplicas`, `safeRemovalReplicasForRole`,
   `applyDeallocationForRole`, `sortVariantsForScaleDown`'s weighting, the one caller of
   `prcFromVCs` in `allocateForModelPaired`'s role-PRC-map build) and state, per caller, whether
   it wants the strict (no-fallback) or what-if (fallback-allowed) variant, per criterion 7.
   Do not propose the accessor-splitting design yourself in detail — that is a follow-on design
   task; your job here is the per-caller classification that a future design would need.
6. Explicitly verify: does `buildCapacities` sit inside composite construction (per correction
   4) or downstream? Re-check the call chain yourself rather than trusting either prior doc's
   framing.
7. Explicitly check demand-related consumers (`demandForRoleOrModel`, `TotalDemand` sums,
   anything reading `D_sat[role]`) for whether any of them currently conflate a per-role-broken
   case with a per-SO fallback case, per criterion 3. This was NOT checked by either prior pass
   — it is new ground.

## Progress reporting — required at every phase boundary

Report progress via ALL THREE of the following at each phase boundary (inventory done,
classification done, final report ready) — not just one channel:
1. `agentbus_publish` on topic `composite-analyzer.usage-audit.out`, `kind="note"` for progress,
   `kind="handoff"` for the final completion report.
2. Direct `SendMessage` to the mission owner (this session) with the same content.
3. A **non-blocking user notification** in addition to the above — publish to `user.in` per
   `conventions/agentbus.md`'s "Status & Progress Notifications" section:
   ```
   agentbus_publish(topic="user.in", from_session="2026-09-15-usage-audit-1", kind="note",
     body="<progress or status update>")
   ```
   This is a new requirement the mission owner's user asked for on this dispatch specifically
   (the previous dispatch only used channels 1 and 2) — do not skip it.

## Output

Write the full audit to `.session/composite-signal-full-usage-audit-2026-09-15.md`. Keep the
final chat/report short (per standing convention) — the file is the deliverable, the reports
above are pointers to it plus a short headline.

## Limits

- Read-only except for your own ledger and the one output file named above.
- Do not edit `.session/STATE.md`, the spec doc, drafts, or any code file.
- Do not push, rebase, or open a PR.
- If you find a criterion genuinely conflicts with what the code does (not just "current code
  doesn't follow it yet" — an actual ambiguity in how a criterion should apply), publish a
  `kind="question"` and wait rather than guessing.
- Terminate after publishing the final report. Do not hold open for further instruction unless
  told to in a reply.

## Done / completion criteria

- `.session/composite-signal-full-usage-audit-2026-09-15.md` exists, covers supply AND demand
  AND RC/SC consumers (not just PRC-touching ones), and every recommendation cites which
  corrected criterion (2/3/5/6/7) drives it.
- Every `prcForVariant`/`prcFromVCs` caller has an explicit strict-vs-what-if classification.
- `buildCapacities`'s construction-vs-downstream placement is explicitly re-verified, not
  assumed from either prior doc.
- All three reporting channels (agentbus topic, direct SendMessage, `user.in`) were used at
  every phase boundary.

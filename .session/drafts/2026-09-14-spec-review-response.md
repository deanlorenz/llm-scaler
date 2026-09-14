# Response to review comments on `composite-signal-redesign.md`

## Round 2 answers (this round's specific questions, verified against code)

**Q2 — should `eligibleAnalyzers` be named `enabledAnalyzers`?** Yes, more accurate: the only
thing it filters on is `config.AnalyzerEnabled` (an enabled/disabled question), not general
eligibility (`Eligible()`, the data-quality question) — the current name invites exactly the
"isn't this the same as Eligible()?" confusion you raised. Rename `eligibleAnalyzers` →
`enabledAnalyzers` everywhere: `engine_v2.go:810,812,814`, `composite.go:26,42,86,105,132`
(comments + parameter + loop var), `engine_v2.go:857`'s doc comment. Code + doc change, small,
mechanical — needs a coder task (or I can do it directly given its size, your call).

**Q3 — is §2.3 still correct?** Yes, verified line-by-line: `TotalReplicas` (absorbed `AggN`'s
role) is in `composite_decision.go` ✓; PRC is genuinely inlined in `composite.go`, zero
standalone PRC-shaped function anywhere (grepped) ✓; `domain.RoleOfVC` exists exactly as
decided (`domain/role.go:12`) ✓; `AggregateByRole`'s inline duplicate is untouched ✓. My
earlier flag (below, from round 1) was about tense/framing ("moves into" vs. "now lives in"),
not a correctness error — I overstated that finding. Fix is a wording pass only, no facts to
correct.

**Q5 — downstream usage when `CompositeHasSignal` passes; does the optimizer check per-SO?**
Verified, and this is a real, confirmed gap, not a hypothetical: `SOHasSignal` (the per-SO
check, `composite_signal_gate.go:15`) has **zero production callers** anywhere — grepped every
`.go` file. The actual optimizer files (`cost_aware_optimizer.go`, `greedy_score_optimizer.go`,
`rescale.go`) never read `.Reason`/`DecisionPath`/`ReasonNoData`/`ReasonError` at all; the only
readers of the decision-path-as-Reason string are `SOHasSignal`/`CompositeHasSignal`
themselves (uncalled per-SO) and one log line (`engine_v2.go:1155`). So: `CompositeHasSignal`
passing at the whole-request level tells the optimizer nothing about which individual SOs
within that request actually have signal — a request with 5 SOs where 4 have real signal and 1
has `DecisionNoSignal` still passes `CompositeHasSignal` as a whole, and the optimizer treats
all 5 SOs' PRC/TotalReplicas as equally trustworthy.

**However — verified this does NOT crash or silently corrupt anything today**: a no-signal SO's
`compositeTotalReplicas` stays `0` (`composite.go`'s switch default), which routes
`vc.PerReplicaCapacity` into the `else if sourceVC != nil` branch — sat's own PRC is copied
through, not zero/garbage. Every optimizer consumer of `PerReplicaCapacity` I found
(`cost_aware_optimizer.go:90,120,227`, `greedy_score_optimizer.go:403,416,479`) guards with
`if vc.PerReplicaCapacity <= 0` before dividing. So today's actual behavior for a no-signal SO
is "silently fall back to using sat's PRC as if it were normal," not "crash" or "propagate a
zero." That's arguably its OWN problem (the optimizer can't distinguish a real signal from a
no-signal fallback that happens to have a valid-looking PRC number), but it's not the
crash/divide-by-zero risk your phrasing ("must not fail") suggested might be at stake — flagging
this distinction so we're solving the right problem. This is a design question for you: should
the optimizer (or something upstream of it) actually consume `SOHasSignal` per-SO, and if so,
do what with a no-signal SO (skip it? zero its contribution? something else)? I don't have a
code answer to "what should happen instead" — only the code-level facts above.

**Q7 — where does the per-analyzer-threshold TODO get captured?** In code: as a comment on
`allocation.TotalReplicas` (`composite_decision.go:43`), which is the function that would need
to change if/when per-analyzer thresholds get folded in — it currently computes
`demand/PerReplicaCapacity` with no threshold adjustment at all. In the spec: §2.7 itself (you
said "can add comment for future TODO" there), which is what the edit plan below already
proposed — confirming that's the right place, not asking again.

## Round 1 findings and edit plan (superseded in part by round 2 above — §2.3 downgraded from
"needs fix" to "wording only," §2.6 gap severity now confirmed rather than open)

For review before I touch the spec file again. Findings first (verified against code), then a
concrete edit plan per section.

## Findings (facts, verified against code — not opinions)

**§2.1.c — what `eligibleAnalyzers` actually saves.** Verified: `Eligible()` and
`eligibleAnalyzers` are two different filters, not redundant. `eligibleAnalyzers` = "namedResults
minus sat, if sat is config-disabled" (an enabled/disabled question, resolved once upstream).
`Eligible()` = "Result!=nil && informative && Live" (a data-quality question, checked per
analyzer in the loop). If the contributor loop iterated `namedResults` directly and relied on
`Eligible()` alone, a config-**disabled**-but-otherwise-fine sat would pass `Eligible()` and
become an ordinary contributor — which breaks the fallback semantics entirely (fallback exists
precisely for "disabled but valid," and that case must NOT be an ordinary contributor). So
`eligibleAnalyzers` is load-bearing, not overkill — but the spec text doesn't say why, which is
the actual complaint. Fix: state this distinction in one sentence, not the current unexplained
`c.` clause.

**§2.3 — `AggN`/`PRCCom` no longer exist in code at all** (`grep -rn "func AggN\|func PRCCom"`
→ zero hits, confirmed just now). §2.3's text describing where they "move to" describes a
transitional plan that implementation has already completed and superseded — it's now
describing history, not current state. No callers anywhere, optimizer included (grepped).
Fix: §2.3 should say what the file placement IS now, not narrate a move that already happened.

**§2.6 — `engine.go` is live, current code, not a legacy "v1."** Verified:
`engine.go` and `engine_v2.go` are two files in the SAME `Engine` type/package, split by
function grouping (top-level reconcile-cycle orchestration in `engine.go`, V2-analysis-specific
collection logic in `engine_v2.go`) — not two competing engine versions. `engine.go:1091`'s
surrounding loop itself calls `collectV2ModelRequest` (`engine_v2.go`'s function) at `:1068`,
so `CompositeHasSignal`'s "v1/v2" naming in comments refers to the analysis method, not file
version. It IS wired, IS called every reconcile cycle, and its purpose (per the comment above
it) is to clear/set `wva_model_scaling_blocked`'s no-composite-signal reason — real, active,
user-facing behavior.

Your question "is having the demand signal enough" — I read this as: should `CompositeHasSignal`
require more than "at least one SO has a decision path"? I don't have a code-level answer to
that without you telling me what "enough" means here (Demand only? Demand+PRC? A minimum
contributor count?) — this is a design question for you, not something I can verify against
code, flagged in the edit plan below as an open item rather than answered.

**§3 — `query_api.go` item is a separate investigation from `TotalReplicas` naming, and is
non-blocking.** Read `code-review-notes.md` §10 in full: it flags `replicasForDemand`/
`safeReplicasForSpare` (`query_api.go:19,39`) as badly named (computing the same
demand/prc-ceil-or-floor quantity `TotalReplicas` now names, but coined independently) AND
duplicated in 3 places (`multi_backup/analyzer_helpers_multi.go`, `greedy_score_optimizer.go`,
and a floor-pattern duplicated verbatim twice). The note's own last line: "not yet resolved
whether these get renamed/unified now or as part of resuming the file-by-file review... recorded
here, not actioned." So: **non-blocking**, explicitly deferred to the resumed code review, not
part of this redesign's scope. §3's one-line pointer undersold this — it read as a minor
"naming/duplication" footnote when it's actually "3 duplicated implementations + inconsistent
naming across the codebase," which is worth a coder task eventually but isn't gating anything
here.

**§2.8 — your correction is a future-direction note, not a bug in current code.** Current
`composite.go` copies `ReplicaCount`/`PendingReplicas` from sat as-is (matches the existing
formula `Supply=ReplicaCount×PRC`, `AnticipatedSupply=(ReplicaCount+Pending)×PRC` — unchanged,
verified). §2.8 already says this gap is "accepted for now, not fixed." Your comment ("True
supply should be from replicaCount [ready]. Anticipated should be from CurrentReplicas [ready+
pending+failed/unknown], not replicaCount+Pending") reads to me as narrowing/correcting what the
EVENTUAL fix should target — not something to implement now. Flagging as a §3 open item with
your exact wording preserved, not silently folding it into "accepted for now" as if it changes
nothing.

## 7 — "recheck all is implemented," and pre-single-analyzer comparison for testing

Two distinct asks here, want to confirm before I do either:
1. **Recheck implementation completeness** — I can re-verify build/test/regression-guards
   right now (I did this once already this session, see ledger; can redo against the current
   HEAD to be sure nothing regressed since).
2. **Pre-single-analyzer aggregation comparison, "identify the former aggregations... make sure
   they still have similar meaning — for sat-only NO CHANGE; for multi-analyzer correct math,
   correct units."** This is a real, nontrivial investigation: find the pre-single-analyzer
   engine-side code (STATE.md says this needs `git log`/`git blame` on `engine_v2.go`, or ask
   you directly if that doesn't turn it up quickly), trace what it computed for the sat-only
   case AND the multi-analyzer case, and confirm §2's formulas reduce to the same numbers/units
   in both. This is bigger than a quick check — I want your confirmation this is the right next
   investigation before I start it, since it could be substantial.

## Proposed edit plan for §2 (not yet applied — awaiting your go-ahead)

| § | Problem | Fix |
|---|---|---|
| 2.1 | Too much WHY inline (e.g. the whole call-stack block explains reasoning, not just structure) | Strip to: call stack + short bracketed pointers to §7 subsections for WHY. No inline justification. |
| 2.1.c | `eligibleAnalyzers` looks redundant with `Eligible()` | One sentence stating the distinction (see Finding above), then move to short form. |
| 2.1.d | "max over contributors' TotalReplicas" described as prose, not a function | Rename to `CompositeTotalReplicas()` as an actual function name (currently inline in the switch); variable holding the per-contributor value → shorter name, e.g. `n` or `total` (currently `compositeTotalReplicas` — you said "TotalReplicas or shorter"). This is a real code-structure change, not just a doc fix — will need a coder task if approved. |
| 2.1.e | "computed once per (model,role)" reads as if the whole PRC computation is once-per-role | Reword: only `D_sat[role]` (the numerator) is fetched once per role; PRC itself is still computed per SO. |
| 2.3 | Describes a completed transition as if still pending | State current file placement as fact, drop the "moves into" framing. |
| 2.6 | No wiring/usage context, "v1" ambiguity | Add one line: `engine.go`/`engine_v2.go` are the same Engine, not two versions; `CompositeHasSignal` is live, called every cycle, gates `wva_model_scaling_blocked`. The "is the demand signal enough" design question → §3, open item, not answered here. |
| 2.7 | — | Add the TODO comment you asked for: adjust `TotalReplicas`/`N_i(SO)` by per-analyzer thresholds (future work, not this task). |
| 2.8 | Understates scope of the future fix | Add your exact correction as the target direction, keep "accepted for now" framing for current behavior. |
| 3 | Not marked blocking vs non-blocking | Split into two explicit sub-lists: "Blocking" (empty right now, unless 2.6's design question counts) and "Non-blocking / tracked elsewhere" (query_api.go, demand-unit-across-models, spec.md re-sync, user code review pending). |
| 7 | — | Add a note requiring every §7 finding to be checked against pre-single-analyzer code specifically (not just current code), once that investigation (see above) is done. |

Waiting on:
1. Confirmation/correction of the findings above (especially §2.6's design question — I can't
   answer "is the signal enough" myself).
2. Go-ahead on the §2.1.d code-structure change (real code, needs a coder task, not just a doc
   edit) — or should this wait until the pre-single-analyzer comparison is done first?
3. Which of the two "7" asks to do first — re-verify current implementation, or start the
   pre-single-analyzer trace (the bigger one).

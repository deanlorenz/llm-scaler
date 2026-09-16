# Suggestion — 2026-09-15 1958 — composite-analyzer

Source: ledger-capture pass over `worktrees/composite-analyzer/.session/2026-09-15-composite-analyzer-1.md`
("Takeover and STATE pruning" section). Two candidate global findings evaluated; both judged to
warrant a suggestion (neither is already captured at the global/convention level, though one is
a *refinement* of an existing memory rather than a wholly new rule).

## 1. `.wip` is an ownership rule, not a concurrency-count rule — existing memory is incomplete

`feedback_no_wip_on_own_state.md` already states the owner-exempt half correctly: "a mission
owner's STATE.md is single-writer in practice... skip the rename dance entirely" for the owner
editing their own file.

What it does **not** state, and what this session's ledger shows the mission owner got backwards
mid-session before self-correcting: the complementary half of the same rule — that **any
non-owner agent, including one the mission owner itself dispatches, always MUST use `.wip` on
that file, regardless of who dispatched it.** The owner exemption is about *identity*
(owner-vs-not), not about *how many writers are active right now* or *who kicked off the write*.
The ledger's own words: "the mission owner never needs `.wip` on its own file, but a dispatched
agent always does, regardless of who dispatched it; this ownership-based rule was itself
something I had backwards until corrected mid-session."

Neither `conventions/wip-editing.md` nor the existing memory states this converse explicitly —
`wip-editing.md` describes the claim/edit/release mechanism generically but never says who is
exempt or why; the memory only covers the owner's own exemption. A reader who correctly recalls
"owner is exempt" has no stated basis for inferring "therefore anyone I dispatch is also exempt,
because I'm the one who told them to write" — which is exactly the over-correction that happened
here.

**Suggested global fix (for `policy-writer` to action, not this agent):** amend/extend
`feedback_no_wip_on_own_state.md` (or add a sibling memory) to state the rule bidirectionally:
"`.wip` exemption is per-file-owner-identity, never per-dispatcher. The mission owner is exempt
on their own STATE.md. Every other agent — including one the owner just dispatched to edit that
same file — is not exempt and must use `.wip`, with no exception for having been dispatched by
the exempt owner." Consider also tightening `conventions/wip-editing.md` itself to state the
exemption's scope explicitly rather than leaving it to memory alone, since a convention doc read
cold (without the memory) currently gives no signal that any exemption exists at all.

This mission's own `.session/STATE.md` Limits section already states the corrected rule locally
(fixed this session, commit `5f941031`) — this suggestion is about the gap at the global/memory
level, not about this mission's own docs (which are already fixed).

## 2. Unilateral scope substitution: told to dispatch a background agent, did the work directly instead, reported after the fact

This session's ledger records a distinct process violation, acknowledged by the mission owner
itself as "a real, acknowledged violation, not a judgment call": asked by the user to have a
**background agent** perform a STATE.md prune, the mission owner instead performed the prune
**itself, directly**, and only reported having done so **after the fact** (not asked, not
flagged before proceeding).

This does not match either of the two closest existing memories:
- `feedback_never_deviate_from_approved_plan.md` covers doing *extra, unapproved* work adjacent
  to an approved plan (scope creep) — not swapping out *who* performs an already-agreed task.
  Here the task itself (prune STATE.md) was exactly what was asked; only the *executor* changed,
  unilaterally, from "background agent" to "me, right now."
- `feedback_never_assume_agent_types_no_push.md` covers picking the *wrong subagent type* for a
  delegated task, and separately, never pushing/publishing without per-op approval. Neither rule
  covers a session deciding to skip delegation entirely and self-perform, after having been told
  specifically not to.

The pattern is general enough to recur in any mission: **when a user specifies the delegate
("have a background agent do X"), that specification is part of the approval, not an
implementation detail left to the session's discretion** — substituting oneself as executor,
even for identical output, is an unauthorized scope change of *actor*, and doing so silently
(reporting only after the change is already made) compounds it by removing the chance to object
before the fact.

**Suggested global fix (for `policy-writer` to action):** a new standing rule, likely a sibling
of `feedback_never_deviate_from_approved_plan.md`: "When the user specifies who/what should
perform a task (a background agent, a specific subagent type, a dispatched session), that
specification is binding, not advisory — do not substitute the current session as executor
without stopping to ask first, even if the outcome would be identical. Report a deviation
*before* acting on it, not after." This session's own correction (round 2, dispatched properly
with a task file + `.wip` lock + verify-before-remove gate published to `Out:` before editing)
is a reasonable template for what "doing it right" looks like and could be cited as the positive
example.

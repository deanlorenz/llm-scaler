# Suggestion — 2026-09-22 1730 — composite-analyzer

**Source:** User direct observation during composite-analyzer mission takeover
(`/resume-mission`, session 2026-09-22-composite-analyzer-1), reviewing why the mission-owner
session read `wip-editing.md` and a previous session's ledger.

## 1. `.wip` protocol: delegator doesn't need to read the rule itself, just cite it

When the mission owner dispatched a `ledger-capture` subagent that would edit shared files
(`.session/STATE.md`, the mission spec doc) the owner does not own, the owner read
`conventions/wip-editing.md` itself first, in order to correctly brief the subagent on the
protocol in its task prompt.

This is unnecessary indirection. The delegating session doesn't need to internalize the `.wip`
mechanics — it only needs to know the rule exists and route the subagent to it. The correct
instruction is simply: "read and follow `conventions/wip-editing.md` before editing any file you
don't own" — passed to the subagent, not read by the delegator.

**Suggested fix:** `conventions/tasks.md` (which already says "tell the agent explicitly ... whether
it needs `.wip`") should be more explicit: the delegator states *that* `.wip` applies and to *which
files*, and points the subagent at the convention file — it does not need to read
`wip-editing.md` itself unless it is also making a direct edit of its own. Add a line to
`conventions/tasks.md` or `conventions/wip-editing.md` itself: "A session dispatching a subagent to
edit a shared file needs only cite this file to the subagent; it does not need to read it unless
it is also editing directly."

## 2. `ledger-capture` contract is underspecified — leads to redundant work and oversized reports

The `ledger-capture` contract (`conventions/resume-and-handoff.md` §"ledger-capture Contract")
does not tell the worker four things it should already know without being told case-by-case in
its task prompt:

**a. It must read `wip-editing.md` before editing STATE.md / spec docs it doesn't own.** Right
now the delegating session has to spell this out in every task prompt (see finding 1 above) —
the contract itself should state this as a standing requirement of the role, not something each
dispatcher re-derives.

**b. It must not re-verify from scratch — it should resume from the last `## Verified` marker.**
In this instance, the ledger being processed had already been through two prior
`## Verified 2026-09-17` / `## Verified 2026-09-18` passes. The worker (correctly, per its
instructions) re-read and re-verified the entire 401-line ledger end-to-end a third time, finding
nothing new — pure wasted work. The contract should instead direct the worker to: locate the most
recent `## Verified <date>` marker in the ledger, treat everything before it as already durably
captured, and only process content *after* that marker (or, if there is no new content since the
last marker, do a light-touch check that STATE.md's claims — e.g. cited commit hashes — are still
correct, not a full re-derivation).

**c. It must not report a full summary back when nothing new was captured — only the verdict.**
The worker's hand-back to the parent session included a detailed paragraph summarizing ledger
contents (the §2.6 incident, the 4 gaps, rebase history, etc.) even though its actual finding was
"no gaps, nothing needed." That level of detail defeats the purpose of "not yours — don't read it"
for the delegating session, since a full summary in the report is functionally equivalent to the
delegator reading the ledger itself. When the verdict is clean, the report back should be terse:
"Verified — no gaps, nothing added" (plus the ledger path and the verification-table pointer),
full stop.

**d. Only when it actually captured something or found a real gap should it report substantively**
— and even then, structured as: (1) a summary of what the gaps were, (2) an explicit,
separately-flagged list of anything the mission owner needs to act on or decide. Not a narrative
recap of the whole ledger.

**Suggested fix:** Rewrite the `ledger-capture` Contract section in `conventions/resume-and-handoff.md`
to state a-d as standing parts of the role (not per-invocation instructions), and add a "Report
back" subsection specifying the terse-verdict-by-default / substantive-only-on-findings shape.
This also reduces token cost — most invocations should be a fast marker-based no-op check, not a
full re-read.

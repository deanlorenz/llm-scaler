# Suggestion — 2026-09-18 0337 — composite-analyzer

Source: ledger-capture pass over
`worktrees/composite-analyzer/.session/2026-09-16-composite-analyzer-1.md`, the "Session resumed
2026-09-18" section. One candidate global finding evaluated and judged to warrant a suggestion;
checked against existing memory first (`feedback_git_destructive_confirm`,
`feedback_dont_deprioritize_unexercised_paths`, `feedback_never_deviate_from_approved_plan`) and
against the two findings already filed in
`session-tracking/suggestion-box/2026-09-17-2330-composite-analyzer.md` — not already covered by
either.

## Repeating a durable doc's own prior claim to the user, without re-deriving it from the actual code first

**Incident that started this session's 2026-09-18 stretch:** the mission owner told the user that
two code-review items (§1 "comment length", §2 "dev-guide extraction") had landed — based on
`STATE.md`'s own prior claim that they had — without re-reading the actual code first. The user
caught this directly. On re-verification, item §1 had in fact NOT landed. Root cause, stated
plainly in the ledger: "answering from a cached belief instead of re-deriving the answer from the
code."

A related, smaller instance in the same stretch: a `go-reviewer` dispatch was started, then
stopped and redone, because `conventions/reviewer.md` (which says a reviewer should read committed
history, not an uncommitted working tree) was read only *mid-flight*, after the first dispatch had
already gone out — the governing convention for that role/action existed and was available, but
wasn't consulted before acting.

**Why this is distinct from the two findings already filed 2026-09-17:**
- Suggestion 2026-09-17 item 1 is about the *session-start* sequence specifically (no tool call
  before orientation is presented). This incident is not about session start — it happened mid-
  mission, well after orientation, when *answering a routine status question*.
- Suggestion 2026-09-17 item 2 is about closing an *investigation* by tracing only one sub-case
  from memory. This incident is different in kind: the mission owner did not attempt to
  investigate or re-derive anything at all — it treated a **written durable doc's existing claim**
  (not a memory of a past investigation) as already-verified fact, and repeated it to the user
  as current truth.
- Neither existing filing, nor `feedback_dont_deprioritize_unexercised_paths` nor
  `feedback_git_destructive_confirm`, covers "a durable doc's own claim about code state is not
  self-certifying — it can go stale the moment the code changes again, and must be re-checked
  against the code before being asserted, not just re-read and repeated."

**The recurring shape across both instances above:** treating an existing artifact (a STATE.md
claim; a role's own governing convention) as something to *rely on* rather than something to
*re-check before the relevant action* — repeating a stale claim in the first case, dispatching
before reading the rule that governs the dispatch in the second.

**Suggested global fix (for `policy-writer` to action):** consider a standing rule along the
lines of: "A durable doc's claim that some code change 'landed' or 'is done' is a claim about a
past state, not a live fact — before repeating such a claim to the user (or building on it), re-
derive it from the current code/tests directly. This applies especially to STATE.md/ledger claims
that predate the most recent commits on the branch." Optionally pair with a narrower reminder for
role dispatch specifically: "read the governing convention for a role/action (e.g.
`conventions/reviewer.md` before dispatching a reviewer) before the first dispatch of that role in
a session, not after."

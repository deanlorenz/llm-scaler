# Suggestion — 2026-09-24 1451 — composite-analyzer

**Source:** Mission-owner session picked up STATE.md's "Known Issue" resume note (diff-review
page not independently re-verified) and, instead of dispatching `diff-review-page` to redo and
verify its own prior output, did the debugging itself: wrote a Python script to tokenize the
JS-escaped hunk arrays embedded in the HTML, counted `+`/`-` lines per file, and diffed the
result against `git diff --numstat` ground truth. The user caught this: "it is not your job to
produce the HTML or verify it. The agent should do all of that and report back a summary."

## Delegation boundary: verifying a subagent's artifact is the subagent's job, not the delegator's

When a prior `diff-review-page` invocation's output is under suspicion (per a STATE.md Known
Issue or similar), the correct action is to **dispatch `diff-review-page` again** — either to
redo the page from scratch, or specifically to verify and fix its own prior output — and have it
report back a summary of what it found/fixed. The delegating session should not open the HTML,
write ad-hoc extraction scripts, or otherwise re-derive the artifact's correctness by hand. That
work is exactly what the specialized agent exists to own, and doing it in the delegator:

- duplicates capability that already lives in the subagent's definition,
- burns the delegator's context budget on mechanical verification work that produces no
  mission-level decision on its own,
- sets a precedent where "the agent's report can't be trusted, so I'll check myself" becomes the
  default instead of the exception — which defeats the purpose of delegating at all.

In this instance, the debugging *did* surface a real, previously-unknown bug (5 of 8 files' nav
summary badges showing stale `+/-` counts, unrelated to the originally-suspected truncation bug),
which is genuinely useful — but it should have been the `diff-review-page` agent's own
self-verification step turning that up and reporting it, not the delegating session's.

**Suggested fix:** Add a line to `conventions/coder-orchestration.md` (or wherever
delegation-boundary rules for specialized-output agents live): "When a prior invocation's
artifact is suspect, re-dispatch the same agent type to verify/fix its own output and report a
summary — do not open/parse/verify the artifact yourself in the delegating session. This applies
to any agent whose job is to *produce* an artifact (HTML pages, generated docs, diagrams): the
producing agent also owns verifying its own product." Consider whether `diff-review-page`'s own
agent definition should be extended with an explicit self-check step (e.g., "before reporting
success, verify the embedded diff line counts against `git diff --numstat` for each file") so
this class of bug is structurally less likely to recur.

# Suggestion — persist an approved plan durably and immediately, not just at a checkpoint

**Source:** `policy-writer`, ledger `2026-08-30-conventions-split-and-trim.md`, found
during ledger-capture 2026-08-30.

**Candidate rule.** The moment a plan is approved (`ExitPlanMode`, or an explicit
"go ahead on X, Y, Z"), persist it to a durable, committed file — not left contingent on
the transient plan-mode file (`~/.claude/plans/*.md`) surviving until execution. If the
plan's own content belongs in a worktree other than `session-tracking` (e.g. a
feature-worktree plan for drafting `CONVENTIONS.md` changes), commit it there,
immediately, alongside the first commit of the work it describes — do not wait for a
later checkpoint.

**Why (context for `policy-writer` to evaluate, not for `CONVENTIONS.md` itself):** a
previously-approved plan for this same mission (splitting `CONVENTIONS.md` into
`conventions/*.md`) could not be recovered when the session went looking for it later —
only two ledger entries referencing its prior existence survived, not the plan's actual
content. The user was very upset about this: a previously-approved plan should never be
lost before being carried out. This is a real, general process gap — nothing durable
captures an approved plan at approval time, only at some later checkpoint that may not
happen before the transient plan-mode file is gone. The fix used once already (persisting
`PLAN-conventions-split.md` inside `worktrees/policy-writer`, committed with the actual
work) generalizes cleanly to any mission with an approved plan.

**Where it might land:** likely `conventions/wip-editing.md` or a new short section near
it (moment: "a plan was just approved, about to start executing it") — or, if the plan
template convention already exists elsewhere in `CONVENTIONS.md`, alongside that.

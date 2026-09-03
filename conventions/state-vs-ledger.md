# `STATE.md` vs. a session ledger

Read this when creating the initial state or ledger files for a session, or when unsure which
one something belongs in.

- **`STATE.md` is what a resuming session reads.** Keep it self-contained and current: current
  task status, blockers, the immediate next step, pointers into the plan/spec doc. Write only
  the **actionable bottom line** — not the story of how a finding was reached. Overwrite it in
  place (except the Session log subsection, which is append-only, short lines, no narrative).

- **A ledger is a continuous, append-as-you-go audit trail that a resuming session normally
  never reads.** One file per session, append-only, consulted later on demand — to recover a
  lost detail, investigate an incident, or confirm via ledger-capture that nothing load-bearing
  was dropped. Can be long and narrative.

- **Rule of thumb:** a ledger entry is a real finding, decision, correction, or false start in
  your own words — not raw tool output, not routine narration. If that finding also changes
  what a resuming session needs to know or do next, its **conclusion** additionally goes into
  `STATE.md` (short) while the **full story** stays only in the ledger (long).

## Minimal STATE.md template

Every `STATE.md` should orient a fresh session at the top:

```markdown
# Mission state — <mission-name>

- **Worktree:** `worktrees/<mission-name>` (branch `<branch>`)
- **Role:** <mission-owner | ...>
- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
- **Plan:** `.session/<spec-or-plan-doc>.md`

## Current status

<one paragraph: what is done, what is in progress, what is blocked>

## Immediate next step

<one or two sentences: exactly what to do next>

## Session log

- <date> session=<slug> status=<active|retired> ledger=.session/<slug>.md
```

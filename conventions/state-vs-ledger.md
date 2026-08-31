# `STATE.md` vs. a ledger — different purpose, different audience

Read this before writing to `STATE.md` or a ledger, or when unsure which one something
belongs in.

- **`STATE.md` is what a resuming session actually reads.** Self-contained and current: current
  task status, current blockers, the immediate next step, pointers into the plan/spec doc's
  specific sections. If something unusual happened, `STATE.md` gets only the **actionable
  bottom line** — e.g. "rotate the leaked keys" — not the story of how it was discovered. It is
  overwritten, not appended to (except its Session log subsection, short-lines-only, not a
  narrative).
- **A ledger is a continuous, append-as-you-go audit trail that a resuming session normally
  never reads at all.** Consulted later, on demand — to recover a lost detail, investigate an
  incident, or (via ledger-capture) confirm nothing load-bearing got dropped. Can be long,
  narrative, exhaustive.
- **Rule of thumb:** a ledger line is a real finding, decision, correction, or false start,
  summarized in your own words — not raw tool output, not routine narration. If that finding
  also changes what a resuming session needs to know or do next, its **conclusion** additionally
  goes into `STATE.md` (short) while the **full story** stays only in the ledger (long).

## Where these files live

Both `STATE.md` and the ledger files for a mission live in the mission's own branch/worktree,
under `.session/`:

```
worktrees/<mission-name>/
  .session/
    STATE.md               ← current mission state
    <session-slug>.md      ← one ledger file per session, append-only
    <internal-plan>.md     ← internal plans not destined for any PR
```

`.session/` is **committed and pushed on the mission branch** (`origin` only, not upstream) —
this is how the files survive a local worktree deletion and are recoverable via
`git show <mission-name>:.session/STATE.md`. Spawned agents that need to read mission docs
access them through the filesystem path directly.

`.session/` is **never included in a PR branch.** The mission owner enforces this by only
cherry-picking commits that belong in the PR — never cherry-picking `.session/` changes. The
`pr-branch.md` pre-push check (`git ls-files .session`) is the safety net.

`session-tracking/missions/<mission-name>/` holds **symlinks only** pointing into
`<mission-worktree>/.session/` — a read-only convenience for other sessions. If the mission
worktree is not checked out locally, the symlink path encodes the branch name
(`worktrees/<mission-name>`) so the files can be retrieved via git from that branch.

## The live ledger during a session

**One cadence only — no more copy step.**

Append to your live ledger file at `<mission-worktree>/.session/<session-slug>.md` after every
meaningful finding, decision, correction, or false start — as it happens, not batched. When
the mission branch is pushed to `origin` (at any checkpoint or wind-down), the ledger is
included automatically. There is no separate "copy to session-tracking" step.

**Persist findings and decisions through failures and restarts.** Append to the ledger even
when nothing landed — a false start recorded is as valuable as a task completed. This means
during the session, not only at the end.

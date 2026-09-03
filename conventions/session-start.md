# Starting a session

Read this at the start of every session, before doing any work.

## 1. Identify the mission and role

Every session is tied to exactly one mission and one role. Confirm both before proceeding:

- **Mission name** and its branch/worktree.
- **Role** and its authority boundary:
  - **Mission owner** — owns the mission's state, plan, branch, and integration decisions.
    Also read `conventions/mission-owner.md`.
  - **Coder** — implements an assigned task within the stated file and worktree scope.
  - **Reviewer** — reviews specified work and reports findings; does not silently modify it.
  - **Researcher** — investigates the assigned question and records findings without expanding
    scope.

If either is unknown, ask before proceeding. Do not infer mission-owner authority from being
the only active session.

## 2. Locate or create the session STATE file

Every session has its own STATE file — the durable orientation record for this session. It
is separate from the mission-level `STATE.md` (which the mission owner maintains).

**If invoked by a parent (coder, reviewer, researcher):** the parent prepared the STATE file
before invocation and passed its name. Locate it at the path given.

**If resuming interactively:** find the STATE file by session slug in
`<mission-worktree>/.session/<slug>.STATE.md`. If the session slug is all you have, that
is enough.

**If starting fresh as a mission owner:** create `.session/STATE.md` using the template in
`conventions/state-vs-ledger.md`.

The STATE file contains: mission, role, worktree, scope, current task, and a pointer to the
active ledger. Read it in full before working.

## 3. Create or continue the session ledger

The ledger is the append-only log for this session. One file per session, named
`YYYY-MM-DD-<session-slug>.md` in `<mission-worktree>/.session/`.

Start every ledger entry with:
```
State: <path to this session's STATE file>
Continues: <path to previous ledger, if any — omit if first session>
```

Append findings, decisions, corrections, and false starts continuously as they occur — not
batched at the end. Never reuse another session's ledger file.

Register the ledger in the session STATE file as the active ledger.

## 4. Read mission state and plan

Read the mission-level `STATE.md` (at `<mission-worktree>/.session/STATE.md`) and the
applicable plan/spec doc before working. If a convenience symlink under
`session-tracking/missions/<mission-name>/` is broken, follow
`conventions/feature-worktree-setup.md`.

## 5. Read situational rules

Return to the index in `CONVENTIONS.md` and read the files triggered by your role and the
requested work before acting.

# Starting a session

Read this at the start of every session, before doing mission work.

## 1. Identify the mission

Record the mission name and its branch/worktree. If the mission is unknown or the named
worktree does not match it, ask the user before proceeding.

## 2. Identify the role

Record the session's role and its authority boundary. Common roles include:

- **Mission owner:** owns the mission's state, plan, branch, and integration decisions. Also
  read `conventions/mission-owner.md`.
- **Coder:** implements an assigned task within the stated file and worktree scope.
- **Reviewer:** reviews specified work and reports findings; does not silently modify it.
- **Researcher:** investigates the assigned question and records findings without expanding the
  mission scope.

If the role is not explicit, ask the user. Do not infer mission-owner authority from being the
only active session.

## 3. Locate mission state

Mission tracking files live under `<mission-worktree>/.session/`. Read the mission's current
state and applicable plan before working. If a convenience symlink under
`session-tracking/missions/<mission-name>/` is broken, follow
`conventions/feature-worktree-setup.md`.

## 4. Create the session record and ledger

Every session creates a uniquely named markdown file under `<mission-worktree>/.session/` and
uses that file as its continuously updated ledger. Use a sortable name such as
`YYYY-MM-DD-<session-slug>.md`; never reuse another session's file.

Start it with:

```markdown
# Session — <session-slug>

- **Mission:** <mission-name>
- **Role:** <mission-owner | coder | reviewer | researcher | other>
- **Worktree:** `worktrees/<mission-name>`
- **Ledger:** `.session/<this-file>.md`
- **Scope:** <the work this session is authorized to do>
- **Status:** active

## Goal

<current requested outcome>

## Findings and decisions

<append continuously while working>
```

A mission owner also registers the session in `.session/STATE.md` as required by
`conventions/resume-and-handoff.md`. Other roles do not edit `STATE.md`; they report needed
state changes to the mission owner.

## 5. Read situational rules

Return to the index in `CONVENTIONS.md` and read the files triggered by the role and requested
work before acting.

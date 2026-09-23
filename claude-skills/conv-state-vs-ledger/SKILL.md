---
name: conv-state-vs-ledger
description: Use when creating a new STATE or ledger file, or when unsure whether a piece of information belongs in the spec, STATE, or ledger.
---

# conv-state-vs-ledger

Runs in your own session.

## Three documents — where things go

**Spec** (plan/mission document): settled design, decision history with reasoning and rejected alternatives, roadmap and task list. Update the spec immediately when a conclusion is reached — not at wind-down, not "for now in STATE".

**STATE**: orientation and current progress only. Thin and current-tense: open items, in-progress work, pending decisions, a short pointer to the relevant spec section, and what a resuming session needs to pick up (last completed, next step). Never duplicates what the spec already says.

**Ledger**: append-only turn log — a safety net, not a findings store. Log what is happening every few turns. Conclusions go directly into the spec. Nobody reads the ledger during active work. Ledger-capture at wind-down confirms nothing was lost.

## Decision rule

Ask: "Is this settled, or still open?"
- **Settled conclusion** → spec, now. STATE gets a one-line pointer at most.
- **Still open / in progress** → STATE, as a current-state note.
- **How we got here** → ledger only.

## Drift check (run at wind-down)

Before finalizing STATE, scan it for drift — settled design, reasoning, or decisions that accumulated this session. Move them to the correct spec section. Remove from STATE after moving. Wind-down is the last guard against drift.

## STATE / task file template

Use this template for all session types. For field authoring guidance see `conv-tasks`.

```markdown
# <Name: session slug or mission name>

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
- **What / goal / mission:** <one or two sentences>
- **Worktree:** `worktrees/<name>` (branch `<branch>`)
- **Role / scope:** <role and authority boundary>
- **Ledger / log:** active `.session/<slug>.md`; captured `.session/ledger/<slug>.md`
  ⚠ DO NOT READ — not yours; new session creates its own ledger

## Task

- **Plan / spec:** `<path>`  *(pull on demand only)*
- **Context:** <files to read — one per line; keep short>
- **Refs:** <cited files — do not read unless explicitly needed>
- **Expected output:** <specific deliverable>
- **Done / completion criteria:** <checkable claims>
- **Limits:** <what not to change>
- **Extra rules / rule refs:** <optional>

## Execution

### Steps / subtasks
- [ ] <step>

**Last completed:** <step or "none">
**Next step / resume point:** <exact next action>

### Status
<NOT STARTED | IN PROGRESS — <what's left> | DONE <date> | BLOCKED on <thing>>

### Known issues
<optional>

## Session log
- <date> session=<slug> status=active ledger=.session/<slug>.md
```

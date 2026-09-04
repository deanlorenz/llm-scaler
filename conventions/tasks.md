# Writing and assigning tasks

Read this when writing a task spec for any worker (coder, reviewer, researcher, or any
delegated session), or when you receive a task directly from the user and need to create a
STATE file from it.

Receivers (sessions that already have a STATE file prepared for them) do not need to read
this file — their STATE file is the task.

## Who writes, who reads

The mission owner (or user) writes task specs. Workers read their STATE file. The mission
owner copies and refines task specs from the plan into a STATE file placed in the worker's
worktree before invocation.

## Field guide

Use the unified STATE template from `conventions/state-vs-ledger.md`. Every field below maps
to a field in that template.

**Orientation fields** — fill these so the session can orient itself without reading anything
else first:

- **Name:** session slug (e.g. `2026-09-03-coder-ct1`). Unique; sortable.
- **Conventions:** always `worktrees/session-tracking/CONVENTIONS.md`. Do not change.
- **What / goal / mission:** one or two sentences — what this session produces and why.
- **Worktree:** the exact path the session works in. Prepare it before invocation.
- **Role / scope:** the session's role and what it is and is not authorized to do. Be
  explicit — do not leave scope to inference.
- **Ledger / log:** the file the session will append to. Name it before invocation;
  the session creates it on first write.

**Task fields** — fill these so the session knows exactly what to do and what to leave alone:

- **Plan / spec:** the plan doc or spec the session follows. Pass the exact path. Workers do
  not read this upfront — they pull it on demand as needed.
- **Context:** files the session must read to do the work — active reference material, not
  plan docs. One path per line. Keep this short; only files genuinely needed.
- **Refs:** cited related files — do not read unless explicitly needed. Prior ledgers,
  background docs, output files from this mission. One path per line.
- **Expected output:** what the session produces — a file, a set of commits, a review report,
  a finding. Be specific enough that completion is unambiguous.
- **Done / completion criteria:** checkable claims. Not "do the work" but "X exists, verified
  by Y." For coders: which tests must pass, which lint checks must clear.
- **Limits:** what the session must not change, what it must preserve, what is out of scope.
  For coders resuming a prior session: what state to keep, where to resume from.
- **Extra rules / rule refs:** optional. Paths to additional `conventions/*.md` files the
  session must read for this task specifically.

**Execution fields** — fill these with the initial plan; the session updates them as work
proceeds:

- **Steps / subtasks:** a checklist. Each item is the smallest unit worth its own status.
  The session checks items off as it completes them and records the last completed step.
- **Next step / resume point:** leave blank initially; the session fills this as it works.
  On interactive sessions it confirms the next step with the user before running it.
- **Status:** set to `NOT STARTED` before invocation. The session updates this as it works.
  Valid values for workers: `NOT STARTED` | `IN PROGRESS — <what's left>` |
  `DONE <date>` | `BLOCKED on <thing>`.
- **Known issues:** optional. Fill in any known constraints or risks before invocation.

## Continuation (handing off a partially done task)

When a task is `IN PROGRESS` and a new session is taking over, update the STATE file before
the new session starts:

- Set **Last completed** to the last finished step.
- Set **Next step / resume point** to exactly where the new session should pick up.
- Update **Limits** with any state that must be preserved.
- Add a **Known issues** entry for anything the new session must know.

The new session reads the updated STATE and starts from the resume point — it does not
re-derive context from scratch.

## Delivery

For Claude FW/BG workers: pass the STATE file path in the invocation message. The session
reads it as its first action.

For Bob CLI workers: place the STATE file at `.session/<slug>.STATE.md` in the prepared
worktree. Pass the path in the launch prompt.

The STATE file must be complete and committed before invocation. A session that starts with
an incomplete STATE must ask the user before proceeding.

## Mission spec / roadmap structure

For missions with extended history and multiple tasks (i.e. a plan/spec doc like
`spec-policy-writer.md`), use this canonical structure. Sessions read sections 1–2 upfront
and stop; everything below is on-demand.

```
## 1. Quick summary / orientation
   One paragraph: what this mission builds and why.

## 2. Principles / approach           ← READ UP TO HERE UPFRONT. STOP.
   Compact bullet list: settled design decisions, standing rules, key constraints.
   What/how only — no incident narration. Rationale lives in section 7.

## 3. At-a-glance
   Short table: branch/worktree, installs-into, status, last pushed, active work, blocking.
   Human-readable; for the user to scan at a glance.

## 4. Needs me
   Decision points and open questions blocked on user ruling.
   Pull this section when you need a decision, not at session start.

## 5. Roadmap / checklist
   Flat checklist of all tasks with status. One line per task.

## 6. Outline
   Table: task/discussion name | one-line summary | status.
   Navigational index into section 7. Pull to find a specific detail.

## 7. Details
   Full content for each task and discussion: what was done, commits, rationale, incidents.
   Pull individual subsections on demand; do not read the whole section upfront.

## 8. Other / Refs
   Mission-specific sections (findings, captured discussions, etc.).
   Related files (do not read unless explicitly needed).
```

**Reading rule for mission specs:** a session reads sections 1–2 at session start as part
of its context pull. It does not read sections 3+ unless it needs a specific item — look it
up by section or outline entry, read only that subsection.

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

- **In:** child agentbus input channel. Required for every subagent.
- **Out:** child agentbus output channel. Required for every subagent.

- **Name:** session slug (e.g. `2026-09-03-coder-ct1`). Unique; sortable.
- **Conventions:** always `worktrees/session-tracking/CONVENTIONS.md`. Do not change.
- **What / goal / mission:** one or two sentences — what this session produces and why.
- **Worktree / Path / Branch:** where the worker operates. Fill per the setup chosen for this task.
- **Startup verification instructions:** free text — the worker's required first action and what to do on failure. Never leave blank.
- **Role / scope:** the session's role and what it is and is not authorized to do. Be
  explicit — do not leave scope to inference.
- **Ledger / log:** the file the session will append to. Name it before invocation;
  the session creates it on first write.

**Task fields** — fill these so the session knows exactly what to do and what to leave alone:

- **Plan / spec:** the plan doc or spec the session follows. Pass the exact path. The task writer
  must extract or reference the relevant plan sections so the coder/reviewer can focus on the
  specific assignment without wading through unrelated spec history. Workers do not read this
  upfront — they pull on demand as needed.
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
  For rebases or multi-file refactors, the task writer must prepare:
  1. An explicit list of all file/code locations requiring modification.
  2. A step-by-step change sequence.
  3. A post-change verification checklist (exact tests, lint, and behavioral sanity checks).
  The worker follows this plan sequentially.
- **Next step / resume point:** leave blank initially; the session fills this as it works.
  On interactive sessions it confirms the next step with the user before running it.
- **Status:** set to `NOT STARTED` before invocation. The session updates this as it works.
  Valid values for workers: `NOT STARTED` | `IN PROGRESS — <what's left>` |
  `DONE <date>` | `BLOCKED on <thing>`.
- **Known issues:** optional. Fill in any known constraints or risks before invocation.

## Task authoring rules

### Commands must have predictable effects
Every command in a task file must have a predictable effect on the specific expected state at
the time the worker runs it. If the author needs an `if` branch — they don't know the state.
Fix: either pin the mechanics down first, or state the *goal* ("ensure X is not staged") and
let the worker choose the mechanics. Never half-specify both: a hedged command plus a hedged
conditional passes uncertainty downstream to a worker with less context, and a more compliant
worker may execute the bad command anyway because the task file said to.

### Use the narrowest command (repeat of global rule — applies especially to task files)
Prefer the minimal, reversible command. A task file's authority can substitute for a worker's
judgment — which means a wrong command in a task file is worse than a wrong command run
interactively. When a guard fires on a task-file command, the worker should substitute the
safer alternative and disclose the substitution, not override the guard.

Reference:

| Intent | Preferred command | Avoid |
|---|---|---|
| Remove from index, keep on disk | `git rm --cached -r <path>` | `git restore --staged --worktree` (deletes) |
| Remove from index and disk | `git rm -r <path>` | |
| Drop a commit from a branch | branch from the upstream base | `git reset --hard` (loses task file) |

### Don't declare something impossible without checking
Before telling the user or worker that something cannot be done, verify it against
`conventions/`. A Known-issues note in one mission's STATE is not proof the whole system
lacks a solution — the convention may already document the working pattern.

### Progress reporting — what a worker must publish
A worker running as a background agent must report progress in two ways:

1. **`Out:` channel** — publish status, findings, interim results, and completion to the
   parent session's monitoring channel. Answer any parent request on `In:` before continuing.
2. **`user.in` publish** — for non-blocking visibility to the human user (fire-and-forget;
   the worker does not wait for a reply):
   ```
   agentbus_publish(topic="user.in", from_session="<slug>", kind="note",
     body="<progress update>")
   ```

Report partial progress at natural checkpoints (e.g. after each major step), not only on
completion. If blocked or stopped early, publish the blocking reason to both channels before
exiting.

### Done criteria — report actual results, not pass/fail
Done criteria in the task file must be checkable claims. The worker reports the actual result
against each criterion — not just "passed" or "done". Examples:
- ✅ "Ran 1 of 150 Specs — `TestNilSaturationGuard` — SUCCESS" (not just "tests pass")
- ✅ "Conflict in `docs/foo.md` lines 12–18 — exact hunk: `<<<< ... >>>>`; stopped" (not "conflict found")

If there is a conflict: do not improvise a resolution. Publish the exact conflicting file and
hunk to `Out:` and stop. The parent decides the resolution.

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

Rules:
- Include `In:` and `Out:` in every background invocation.
- Include the input subscription command in the task file or launch prompt.
- Do not launch a subagent without both channels.

Pass the STATE file path in the invocation message. The session reads it as its first action.

For a persistent CLI coder invoked from Claude, place the STATE file at
`.session/<slug>.STATE.md` in the prepared worktree and pass the path in the launch prompt.

The STATE file must be complete and committed before invocation. A session that starts with
an incomplete STATE must ask the user before proceeding.

## Mission spec / roadmap structure

For missions with extended history and multiple tasks (i.e. a plan/spec doc like
`spec-policy-writer.md`), use this canonical structure. Sessions read sections 1–2 upfront
and stop; everything below is on-demand.

```
## 1. Orientation
   Fused overview: what this mission builds, why, and the key settled principles/constraints.
   Human-readable. One paragraph + compact bullet list.

## 2. Spec / roadmap                  ← READ UP TO HERE UPFRONT. STOP.
   This section is recursive — its depth scales with the doc's level:
   - Mission-level doc: a roadmap of sub-missions (flat checklist, one line per task).
   - Sub-mission / code-level doc: the same section at deeper resolution — pseudo-code,
     call stack, structure, key constraints — but never literal implementation-language code.
     A few degrees of freedom are left to the coder; design intent is explicit.
   §2 numbering is stable for the lifetime of a doc. Task files cite §2.x directly, so
   §2's number must not change when a doc is restructured.

## 3. Open items
   Blocking decisions and open questions for owner/user only.
   Closed items are dropped, not archived here.
   Pull this section when you need a decision, not at session start.

## 4. Coder task hierarchy
   One task file per §2 item; one step per §2 sub-item.
   Navigational index into section 5/7. Pull to find a specific task file.

## 5. Discussion abstracts
   Concise processed bottom-line per item (not a log).

## 6. Summary of decisions
   Flat list: each decision → ref into §5/§7, impact, rejected alternatives, why rejected.
   For owner/user tracking.

## 7. Detailed discussion
   Full paper trail per item. Pull individual subsections on demand; do not read upfront.

## 8. Revision log
```

**Reading rule for mission specs:** a session reads sections 1–2 at session start as part
of its context pull. It does not read sections 3+ unless it needs a specific item — look it
up by section or outline entry, read only that subsection.

**Restructuring an existing doc to this template:** read the whole source fresh, build the new
structure in a scratch file by relocating exact existing text (no rewriting), then diff
word-count and every code citation (`file.go:N` pattern) against the original before applying.
This catches dropped citations.

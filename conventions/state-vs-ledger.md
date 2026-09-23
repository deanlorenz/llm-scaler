# Three documents: spec, STATE, and ledger

Read this when creating initial state or ledger files, or when unsure which document a piece
of information belongs in.

## The three documents

**Spec (plan/mission document)** is the primary living document for the mission or task.
It exists at every level — a mission has one, a sub-mission has one, a coder task has one.
They follow the same recursive template (see `conventions/tasks.md`), with depth scaling to
the level. This is where conclusions land:
- Agreed design — settled, current
- Decision history with reasoning and rejected alternatives
- Roadmap and task list

When a decision is reached during a session, update the spec immediately — not at
wind-down, not "for now" in STATE. STATE never duplicates what the spec already says.

**STATE** is orientation and current progress only. It is thin and current-tense:
- Open items, in-progress work, pending decisions
- A short pointer to the relevant spec section — never a restatement of its content
- What a resuming session needs to know to pick up: last completed, next step

**Ledger** is an append-only turn log — a safety net, not a findings store. Append every
few turns during the session (simple log of what is happening). Conclusions from the session
go directly into the spec, not the ledger. Nobody reads the ledger during active work.
Ledger-capture at wind-down confirms nothing was lost and marks it verified; after that it
is a historical reference only, rarely consulted.

## The drift pattern to avoid

Sessions make decisions and write them into STATE ("a resuming session needs to know this"),
intending to move them to the spec later. They never get moved. The spec falls behind; STATE
bloats into a second, worse copy of the spec.

**Self-check during the session:** when a conclusion is reached → update the spec now.
Ask: "Does this belong in STATE (still open, still in progress) or is it settled?" If
settled → it goes in the spec, and STATE gets a pointer at most.

**Self-check at wind-down (Step 3):** before finalizing STATE, scan it for drift — any
settled design, reasoning, or decisions that accumulated this session. Move them to the
correct spec section first, then remove from STATE. Wind-down is the last guard against
drift before the session closes.

## Rule of thumb
If a finding changes what a resuming session needs to know or *do*, its conclusion goes into
the spec (settled design) or STATE (still open). The full story of how it was reached stays
in the ledger. Update the ledger continuously; update the spec immediately when conclusions
are reached; keep STATE thin.

## STATE / task file template

See `conventions/state_template.md`.

# Mission-owner role

## Responsibilities

- Own and maintain `<mission-worktree>/.session/STATE.md` and the mission's internal plan.
- Maintain the owner's own session ledger continuously.
- Subscribe to `mission.<mission-name>` on startup (per `conv-agentbus`) to monitor
  worker presence, status, and lifecycle events.
- Register ownership and session status according to `conventions/resume-and-handoff.md`.
- Keep `STATE.md` current with the actionable status, blockers, and immediate next step; keep
  narrative detail in ledgers.
- Assign coder, reviewer, or researcher work with an explicit mission, role, worktree, scope,
  and output location.
- Review delegated work before integrating it.
- Commit after any real decision or completed unit of work — not on every small edit, not in
  one large batch at session end.
- Integrate approved work into the mission branch. The mission branch is the mission's source
  of truth.
- Keep `.session/` out of every PR branch.
- Ask before any push; a mission-owner role does not grant standing push authorization.

## Boundaries

- Authority applies only to this mission and its worktree.
- Do not edit or maintain another mission's branch, worktree, state, plan, or ledger.
- Do not treat delegated sessions as mission owners. They report conclusions and requested
  state changes back to the owner.
- An approved plan's listed steps are the only authorized actions. If something adjacent comes up, stop and ask. Do not do it first.
- The same applies to a delegated task: its listed steps are the boundary.
- Do not wind down or hand off merely because a task reached a checkpoint. Wind-down is an
  explicit preparation for ending the session or transferring ownership.

## Delegated-session contract

Before delegating, provide at minimum:

- mission name;
- role;
- worktree;
- exact task and allowed scope;
- documents to read;
- ledger/report file to write;
- validation and expected outcome.

Coder-specific orchestration additionally follows `conv-coder-orchestration`.

## Proposing a rule or process change

- A proposal for `policy-writer` is a suggestion-box entry. Mission-local facts go in the mission spec, not the suggestion box.
- One atomic finding per entry.
- Draft it in `.session/drafts/suggestion-box-YYYY-MM-DD-HHMM.md` and commit the draft on the mission branch, by exact path.
- Structure: Finding (with evidence and file references), Need, Proposed solution. Add "Where it might land" if known.
- Writing into `session-tracking` is posting, even if no existing file is touched. Submit only when the user says to.
- To submit: state the exact destination, `worktrees/session-tracking/suggestion-box/YYYY-MM-DD-HHMM-<mission-name>.md`, and get the authorization `conv-working-outside-worktree` requires. Check the destination does not exist and has no `.wip`, then copy the draft there.
- Do not `git add` or commit in `session-tracking`. `policy-writer` commits entries when it processes them.
- Record the copy and the destination path in your ledger. Optionally publish a one-line note on `session-tracking.suggestions`.

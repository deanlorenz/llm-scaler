# Mission-owner role

Read this when assuming or acting in the mission-owner role.

The mission owner is the single session responsible for the mission's durable state and
integration decisions. Mission-owner authority applies only to that mission and its worktree.

## Responsibilities

- Own and maintain `<mission-worktree>/.session/STATE.md` and the mission's internal plan.
- Maintain the owner's own session ledger continuously.
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

- Do not edit or maintain another mission's branch, worktree, state, plan, or ledger.
- Do not treat delegated sessions as mission owners. They report conclusions and requested
  state changes back to the owner.
- Do not expand an approved plan or delegated task without user approval.
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

Coder-specific orchestration additionally follows `conventions/coder-orchestration.md`.

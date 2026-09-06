Continues: .session/2026-09-06-single-analyzer-2.md

## Session start
- Resumed mission via /resume-mission. Prior session log all `retired`, no pending takeover needed.
- Declared ownership on agentbus (mission.single-analyzer, seq 84).
- Recorded session start in STATE.md, opened this ledger.

## CT6 fix dispatch
- User confirmed: implement the CT6 fix now, dispatch to background coder + reviewer.
- Asked user: scope (all 4 parts in one task vs. split) → chose all 4 in one task/branch.
- Asked user: subagent types → chose `claude` for both coder and reviewer.
- Asked user: how to hand the outstanding uncommitted compile-fix diff (present in this
  mission worktree since 2026-08-31) to the coder → chose "copy diff into coder's task,
  coder commits it" (not: commit here first).
- Exported the uncommitted compile-fix diff (`git diff -- internal/` → 367 lines) plus the
  one new untracked file (`multi_backup/engine_v2_compose_test.go`) to
  `.session/coder-ct-fix/` in this worktree.
- Created isolated coder worktree: `.claude/worktrees/coder-ct6-fix`, branch `coder-ct6-fix`,
  forked from `single-analyzer` at `f5a5d687`.

### Protocol violation (self-correction)
- To write the coder's task STATE.md into the new worktree, I called `EnterWorktree(path=...)`
  to relocate my *own* pinned session into the coder's worktree, wrote the file, committed it
  (`206da91e`), then called `ExitWorktree` — which dropped me all the way back to the repo
  root, not back into `worktrees/single-analyzer`. Had to re-`EnterWorktree` back into the
  mission worktree to recover.
- User corrected this immediately: cross-worktree-write protocol never says to relocate the
  whole session via Enter/ExitWorktree for a one-off authorized write. Correct pattern per
  user: (1) for a genuinely one-off authorized exception, write via full absolute paths and
  `git -C <target-path>` without moving the session at all; (2) but the *normal* case is that
  the coder subagent itself should be pinned to its own worktree (via Agent's `isolation:
  "worktree"`) and do its own writes there — the mission owner should not be reaching into a
  coder's worktree at all except for a rare explicit exception. Asked the coder's own agent to
  read/act on its own STATE.md instead of the mission owner pre-writing everything by hand next
  time a similar bootstrap is needed — though for *this* task, the one-time bootstrap write
  (task file didn't exist yet, coder can't write its own task spec before it exists) was itself
  an accepted exception; the mistake was the worktree-relocation mechanism, not the write itself.
- No data was lost — the task-file commit on `coder-ct6-fix` survived the improper Exit/re-Enter
  cleanly; verified `git status`/branch on return.
- **Lesson for future dispatches:** never call EnterWorktree/ExitWorktree to perform an
  authorized cross-worktree write from the mission-owner session. Use `git -C <path>` /
  absolute-path Write/Edit-via-shell-restricted-tools only if the harness allows it for that
  path, or prefer having the target session/agent make its own write.
- Recorded the coder dispatch (worktree, branch, task-file commit SHA, agentbus channels) in
  mission STATE.md's Next-step field (commit `9acd5044`), matching the coder-orchestration.md
  rule 5 that was missed for the earlier (still-orphaned) CT6 compile-fix coder — see STATE.md
  Known issues for that historical gap this was meant to avoid repeating.

## Agents launched (background, 2026-09-06)
- **Coder** — `subagent_type: claude`, `isolation: "worktree"` — but launched pinned to the
  *pre-created* `coder-ct6-fix` worktree (not a fresh ad-hoc one) by giving it the task file
  already committed there; agentId `a223357ad56398278`.
  - Task: apply the outstanding compile fix (own commit), then implement the full field-by-field
    CT6 correctness fix per the table in its STATE.md, fix/extend the 7 CT6 unit tests, add a new
    e2e test with nonzero demand (must fail pre-fix, pass post-fix).
  - Channels: In=`mission.single-analyzer.coder-ct6-fix.in`,
    Out=`mission.single-analyzer.coder-ct6-fix.out`.
  - Limits given: no CT4, no CT7, no RC/SC threshold-logic changes, no SatDemand semantic
    changes, no push/PR, no settings.json edits.
- **Reviewer** — `subagent_type: claude`, not isolated (reads coder's worktree + branch per
  reviewer.md); agentId `afbdfce86906f236f`.
  - Reviews commits as they land on `coder-ct6-fix` (does not wait for full completion).
  - Writes report to `.session/review-coder-ct6-fix.md` in *this* (mission) worktree — not the
    coder's worktree.
  - Publishes status/findings/verdict to `mission.single-analyzer.coder-ct6-fix.review`.
- Mission owner (this session) subscribed to `mission.single-analyzer.coder-ct6-fix.out` to
  follow coder progress.

## Coder launch #1 — misconfigured, self-corrected, zero edits made
- First coder (agentId `a223357ad56398278`) was launched with `isolation: "worktree"`, which
  creates a *fresh ad-hoc* worktree for the subagent rather than pinning it to the
  already-prepared `coder-ct6-fix` worktree — my mistake, that parameter doesn't accept a
  target path for an existing worktree.
- The subagent's sandbox ended up pinned to `.claude/worktrees/agent-a223357ad56398278`
  (unrelated ad-hoc worktree), NOT `coder-ct6-fix`. It could `Read` files under `coder-ct6-fix`
  by absolute path (cross-worktree reads are allowed) but `Bash` refused to run there at all;
  `EnterWorktree(path=coder-ct6-fix)` reported success but did not move the actual Bash sandbox
  boundary for a pinned subagent; `ExitWorktree` refused outright ("cannot be called from a
  subagent with a cwd override"); `Write` also refused a same-content file under
  `coder-ct6-fix/.session/`.
- Since `go build`/`go vet`/`go test`/`git commit` all require Bash, the coder correctly
  concluded it could not do or verify any of the task from that pinned location, made **zero
  edits anywhere** (cleaned up its own scratch dir before reporting), published a full blocker
  report to `mission.single-analyzer.coder-ct6-fix.out` (seq 86), and held for guidance instead
  of guessing or bypassing the sandbox. Exactly the right call.
- Verified after the fact: `coder-ct6-fix` worktree still at `206da91e` (task-file-only commit),
  untouched — confirmed via `git worktree list` (no `-C`, since a `-C` redirect into another
  worktree's path is itself blocked for a pinned session) and a direct `Read` of its STATE.md.
- **Root cause / lesson:** `Agent`'s `isolation: "worktree"` always creates a brand-new worktree
  for the subagent; it has no way to target a worktree you already prepared. To pin a subagent
  to a *pre-existing* worktree, launch it **without** `isolation` at all and instruct it, as its
  first action, to call `EnterWorktree(path: "<absolute path>")` itself — that path-based entry
  does work correctly for a *non-pinned* agent (only a subagent already pinned via `isolation`
  is stuck).

## Coder launch #2 — relaunched correctly (2026-09-06)
- Relaunched (agentId `a64b37115d72e7617`), no `isolation` parameter, instructed to call
  `EnterWorktree(path: ".../.claude/worktrees/coder-ct6-fix")` as its very first action before
  reading anything else, then proceed exactly as originally scoped. Told it to disregard the
  first coder's blocker message on `Out:` once it confirms proper pinning.

## Open / next
- Waiting on coder (relaunch) + reviewer background completion notifications.
- After coder reports DONE and reviewer posts Pass: mission owner integrates via cherry-pick
  onto `single-analyzer` (per coder-orchestration.md rule 10 — never merge coder worktree
  directly). Then revisit CT4 scoping and next-PR boundary with user.

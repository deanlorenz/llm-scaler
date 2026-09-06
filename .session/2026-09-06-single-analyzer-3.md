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

## Coder launch #2 — also failed (different mode)
- Relaunch #2 (agentId `a64b37115d72e7617`, no `isolation`, instructed to
  `EnterWorktree(path=coder-ct6-fix)` as first action) also failed: `EnterWorktree` reported
  success and Read confirmed the right files were visible, but Bash was hard-bound to a THIRD
  location — this session's own pinned worktree (`worktrees/single-analyzer`) — and refused
  every command there. Zero edits made; held for guidance correctly, same as launch #1.
- **Root cause, confirmed:** a non-isolated background `Agent` spawned from a session that is
  itself pinned via `EnterWorktree` inherits the *parent's* worktree pin at the Bash level.
  `EnterWorktree(path=...)` called from inside such a subagent can only relocate file-tool
  (Read/Write/Edit) views, never the Bash sandbox — confirmed by both launch #1 (Bash pinned to
  an unrelated ad-hoc isolation worktree) and launch #2 (Bash pinned to the parent's own
  worktree). There is currently no way to hand a pre-existing worktree to a subagent (isolated
  or not) and have its Bash actually operate there.

## User-directed pivot (2026-09-06)
User instructions: stop all background agents; clean up the failed worktree; defer "spawn a
subagent inside an already-prepared worktree" to later; going forward — mission owner prepares
a fresh branch with the desired starting state, coder runs in its own fresh `isolation:"worktree"`
sandbox and resets (`git reset --hard <sha>`) to that specific commit on startup, works from
there (same branch if possible, else a new one), mission owner cherry-picks when done.

Actions taken:
- Stopped all 3 background agents (both coder attempts had already self-terminated after
  reporting their blockers; explicitly stopped the reviewer, agentId `afbdfce86906f236f`).
- Removed the failed `coder-ct6-fix` worktree (`git worktree remove`, no real work existed
  there — only the task-file bootstrap commit `206da91e`) and deleted the branch (`git branch
  -D`, confirmed with user first since it's a force-delete of an unmerged branch).
- Discovered the outstanding CT6 *compile* fix was still present, uncommitted, in THIS worktree
  (`worktrees/single-analyzer`) the whole time — confirmed `go build ./...` / `go vet ./...`
  clean, committed it directly here as `18f4d4ff` (normal git write on my own branch, not a
  cross-worktree operation). This fully resolves the "CT6 does not compile" Known Issue
  independently of any coder dispatch — the coder no longer needs to touch this part at all.
- Removed the now-redundant `.session/coder-ct-fix/` scratch diff artifacts (superseded by the
  committed fix; untracked files I created this session, safe to clean up).
- Created branch `coder2-ct6-fix` at `18f4d4ff` (unused in the end — see below, went with
  inlining the task in the launch prompt instead of writing a task file onto a branch).
- **Launched coder v3** (agentId `acc4742a2f2a1aceb`, `isolation:"worktree"` — real Bash in a
  fresh ad-hoc worktree, exactly as confirmed working in launch #1's diagnostic) with the full
  task spec (field-by-field table, limits, done criteria) inlined directly in the launch prompt
  instead of as a task file it would need to read from a worktree it can't reach. First
  instructed action: `git reset --hard 18f4d4ff`, then `git checkout -b coder-ct6-fix-v3`, then
  proceed. This sidesteps the write-boundary problem entirely — nothing needs to be written into
  any coder worktree by the mission owner at all.
- Did not relaunch the reviewer yet — nothing to review until the coder produces commits;
  will launch once the coder reports progress or completion.

## Coder v3 — completed successfully (2026-09-06)
- Reset cleanly to `18f4d4ff` as instructed, worked on branch `coder-ct6-fix-v3`
  (worktree `.claude/worktrees/agent-acc4742a2f2a1aceb`), 3 commits:
  - `7663d180` — core fix: `normalizeToCompositeUnits` now divides RC/SC/Remaining/Spare/
    TotalSupply/TotalAnticipatedSupply (model + per-role) by the same raw demand PRC is divided
    by, via a `demandForRole` helper reusing the existing "both"/per-role lookup; `Utilization`
    recomputed rather than re-divided; `SatDemand`/new `SatRoleDemand` captured before overwrite;
    zero-demand/zero-PRC guards preserved; added `logCompositeSignal` (previously nothing logged
    the post-normalization signal).
  - `a8e9c511` — extended the 7 existing `normalizeToCompositeUnits` specs to set/assert the
    newly-normalized fields; added 2 new specs for `SatRoleDemand`.
  - `0a5c0f52` — new e2e spec through the real `collectV2ModelRequest` → `CostAwareOptimizer`
    pipeline, nonzero demand (TotalDemand=8000, PRC=2000, scaleUp=0.85); coder reports verifying
    it fails pre-fix (raw RequiredCapacity 7411.76 → target of thousands of replicas) and passes
    post-fix (target 5) by temporarily stashing the fix commit and restoring it.
- Coder's self-reported verification: `go build ./...`, `go vet ./...`, `gofmt -l` all clean;
  `go test ./internal/engines/...` passes (158 Ginkgo specs); broader `./internal/...`,
  `./test/testutil/...`, `./test/utils/...`, `./cmd/...` also clean; only `test/e2e` (needs a
  live Kind cluster) not run — environmental, expected. Two pre-existing gofmt issues in
  unrelated files (`analyzer_helpers.go`/`greedy_score_optimizer.go`) confirmed via stash to
  predate this work, not introduced by it.
- No design ambiguities hit — coder reports the field-by-field table mapped directly onto the
  code with no gaps.
- Commit messages read clean — no internal jargon, task-ID leakage, or planning artifacts
  visible in a spot-check of all 3 (`git show --stat` from this worktree, since the branch is
  visible via the shared object store even though I can't `git -C` into the coder's worktree
  path directly).

## Reviewer v2 dispatched (2026-09-06)
- Independent reviewer launched (agentId `ad557beac4e2c53a5`, not isolated — reads the coder's
  worktree directly, per reviewer.md) against the 3 completed commits. Given the full
  field-by-field table, ordering/special-case requirements, out-of-scope list, and done
  criteria to build its own verification checklist, and explicitly asked to independently
  re-run `go build`/`go vet`/`go test`/`gofmt` in the coder's worktree rather than trust the
  coder's self-report. Report file: `.session/review-coder-ct6-fix-v3.md` (mission worktree,
  not the coder's).
- **Not yet independently verified — do not treat the fix as done until the reviewer reports.**

## Open / next
- Waiting on reviewer v2 (agentId `ad557beac4e2c53a5`) background completion.
- After review passes: mission owner cherry-picks `7663d180`/`a8e9c511`/`0a5c0f52` onto
  `single-analyzer` (per coder-orchestration.md rule 10 — never merge coder worktree directly).
- Outstanding, unrelated to the coder dispatch: `origin/single-analyzer` still doesn't build
  (compile fix `18f4d4ff` is local-only, not pushed) — needs a push at some point, with
  per-op authorization; the CT6 fix, once cherry-picked, would go in the same future push.
- After CT6 correctness fix lands: revisit CT4 scoping and next-PR boundary with user.

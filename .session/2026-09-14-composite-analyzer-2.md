# Ledger — 2026-09-14-composite-analyzer-2

Continues: `.session/2026-09-14-composite-analyzer-1.md`

Mission: `composite-analyzer` · Role: mission owner · Branch: `composite-analyzer`

- **In:** `composite-analyzer.owner.in`
- **Out:** `composite-analyzer.owner`
- **Announce:** `mission.composite-analyzer`

---

## Session start

Read in order per `conventions/session-start.md`: `CONVENTIONS.md`, `session-start.md`,
`.session/STATE.md`. Then, on trigger: `state-vs-ledger.md` (creating this ledger),
`coder-orchestration.md` + `worktree-delegation.md` (about to dispatch a coder),
`agentbus.md` (task file uses `user.in`).

Did NOT read: retired ledgers, `spec.md` (flagged stale by STATE), redesign doc §5,
`survey-zero-signal.md`, `review-coder-agg1.md`. Read only redesign §1–2 (the spec) and
`.session/task-coder-composite-redesign.md` — the two files STATE names as the resume point.

Note on a pre-existing conflict, recorded because it governs every edit this session: the
harness's auto-mode instruction says to prefer Bash (`sed`, heredocs) for file edits.
`CONVENTIONS.md` bans in-place CLI rewriting outright. Resolved in favor of CONVENTIONS.md —
`Edit`/`Write` only on owned files, never `sed -i`. Told the user rather than choosing silently.

## User instruction this session

Two directives: (1) maintain a ledger — this file; (2) launch a coder in the background to
implement the new design. (2) is the per-operation dispatch authorization STATE said was
still required. STATE's own resume point had "ask for final sign-off on the task file, then
ask whether to dispatch"; the user's instruction supersedes the second half of that.

## Verification of the task file's code citations (before dispatch)

Checked each concrete claim in `.session/task-coder-composite-redesign.md` against the tree,
so a coder isn't dispatched against a stale task file (STATE warned `spec.md` went stale
exactly this way):

| Claim | Result |
|---|---|
| `collectV2ModelRequest` receives `config config.ScalingPolicy` | confirmed, `engine_v2.go:781` |
| `buildComposite(ctx, namedResults, satUp, satDown)` call site | confirmed, `engine_v2.go:818` |
| `config.ScalingPolicy.AnalyzerEnabled` exists | confirmed, `saturation_scaling.go:657` |
| `aggregation/replicas_needed.go`, `aggregation/prc_com.go` exist | confirmed |
| `allocation/composite_decision.go`, `composite_signal_gate.go` exist | confirmed |
| `HasUsableCompositeSignal` production callers = `engine.go:1091`, `engine_v2.go:735` | confirmed — exactly 2 production callers; rest are tests/comments |

No stale citations found. Task file is dispatch-ready as written.

## Gap found in the task file — fixed before dispatch

`conventions/coder-orchestration.md` §8 and `agentbus.md`'s "Background invocation contract"
both require the task file to carry `In:`/`Out:` channels plus the subscription command. The
task file had only the `user.in` progress-note instruction (added last session) — no `In:`/
`Out:` pair at all. Added an Agentbus section; did not touch any of the 10 implementation
steps.

## Dispatch decision

Setup: **same-worktree, async** per `worktree-delegation.md`. Rationale: the task file already
says "this worktree (`worktrees/composite-analyzer`, branch `composite-analyzer`)" and names
`git rev-parse --show-toplevel` verification against this path; the commits must land on this
mission's own branch. checkout-branch would contradict the task file's own startup check, and
own-worktree would need a second worktree for a branch already checked out here.

Concurrency (rule 6, uniform across setups): recording in STATE that this worktree/branch now
has an active coder. No second coder here until it reports done.

Per same-worktree async: while the coder runs, I must not edit code in this worktree. STATE and
this ledger are mine and stay editable.

## First `coder-redesign` invocation — stopped at step 1/10, blocked

Task notification arrived: coder stopped after applying step 1 only (uncommitted,
`engine_v2.go` — `eligibleAnalyzers`/`excludeByName`, matches task file exactly, verified by
diff). Two real ambiguities found, both genuine, not coder laziness:

- **Q1:** `ResolveSO`/`SODecision`'s only non-test caller (`composite.go:57`) is the exact line
  step 5's inline switch replaces. Task file's file-table said "rewritten" but no step gives a
  rewritten shape. Verified: confirmed by grep, only caller.
- **Q2 (load-bearing):** step 5's contributor loop as written checked only per-SO
  presence+Reason — never `nr.Live`. Today's `ResolveSO` gates every candidate through
  `allocation.eligible()` = `Result!=nil && ResultIsInformative && Live`
  (`composite_eligibility.go:18`). Taken literally, step 5 would let a **stale analyzer
  contribute** — a real behavior change from v8, contradicting redesign §1's own "same
  underlying math as v8" claim, and contradicted by existing `composite_eligibility_test.go`
  coverage. Verified independently against the code before taking this to the user — confirmed
  genuine, not a misread.

Coder escalated via `Out:` (seq 138), `user.in` note (seq 139), a 30-min blocking
`agentbus_ask_user`, and a 10-min monitor — none reached me; I was not polling `In:` at the
time. Left the tree in a safe, uncommitted, documented state. Full detail in its own ledger,
`.session/coder-redesign-ledger.md` (not mine to edit).

**Process problem, caught by the user, not by me:** the coder used `agentbus_ask_user` /
`user.in` to put its coding-ambiguity question directly to the human user. The user's original
instruction was to receive `user.in` **notifications** only — one-way status, never a question
requiring their decision. Asking the user to arbitrate a coding/design gap goes over the
mission owner's head; that decision belongs to me (escalate to the user only if I judge it
needs their input — which, for Q1/Q2, I did). Root cause: the task file's "stop and ask" bullet
said "ask" without naming the mission owner as the sole recipient, and never said which
channels were off-limits for this purpose. Fixed in the task file (Orientation bullet + the
closing "if something is wrong or ambiguous" section): coding/design questions go to the
mission owner's `Out:` reads only, `agentbus_ask_user`/`user.in`-as-a-question are now
explicitly disallowed, and the coder is told not to hold a blocking wait open — post, log, stop
cleanly, let the owner read `Out:` asynchronously.

## User rulings (via AskUserQuestion)

- Q2: **keep the `Eligible`/`Live` gate** — recommended option, matches v8 behavior, matches
  existing test coverage.
- Q1: **delete `ResolveSO`/`SODecision`** — recommended option, step 5 fully absorbs their logic.

## Fixes applied before relaunch

- `.session/composite-signal-redesign.md`: §2.1(b) restores the eligibility gate as (i), with a
  dated correction note explaining what was wrong and why; §2.9 gained a new regression guard
  ("stale analyzer never contributes"); §2.1's "deleted, not relocated" list now names
  `ResolveSO`/`SODecision` explicitly.
- `.session/task-coder-composite-redesign.md`:
  - File table: `composite_decision.go` row now says `ResolveSO`/`SODecision` deleted, not
    rewritten.
  - Step 3: explicit instruction to delete both and port `composite_decision_test.go`'s
    existing cases to the new inline logic, not drop them.
  - Step 5: added an explicit sub-step to export `eligible` → `Eligible` (pure rename,
    `composite_eligibility.go` + its test file) and call it as the contributor loop's first
    gate, with a comment distinguishing this per-analyzer gate from the per-SO Reason check
    below it.
  - Orientation bullet + closing section: escalation routing fixed per the process problem
    above.
- Did NOT touch the already-applied, uncommitted step 1 change in `engine_v2.go` — verified
  unaffected by any of the above (it's upstream of and untouched by steps 3/5's fixes).

Committed as `20ffa9ee`. Resumed `coder-redesign` from step 2 via SendMessage, with both
rulings and the corrected escalation-routing instructions.

## Second invocation — escalated correctly this time, hit a real import cycle

Coder made real progress: steps 1, 2, 3 (minus the blocked `roleOf` call), 4 (minus the
blocked placement), 5, 6, 9, 10 all written (uncommitted). This time it escalated exactly as
corrected: posted the question to `Out:` (`composite-analyzer.coder-redesign`), logged full
detail in its own ledger, stopped cleanly with no blocking wait. No `agentbus_ask_user`, no
`user.in` question. The process fix held.

**The bug it found:** §2.3/step 4 said the one shared role-canonicalization function lives in
`steadystate` as `RoleOfVC`, but step 3's `allocation.TotalReplicas` needs to call it —
`allocation` importing `steadystate` would be a compile-time cycle, since `steadystate` already
imports `allocation` in three files. Verified independently before ruling (not taking the
coder's word for it): `grep` confirmed the one-directional import; `go build` failed at exactly
`composite_decision.go:50:55: undefined: RoleOfVC` after the coder's own attempt, consistent
with its report. Coder proposed moving the function to `domain` (both packages already import
it cleanly, no reverse dependency, already owns `VariantCapacity`) but correctly did not apply
its own proposal — a placement decision it's not permitted to make. This is a placement
correction, not a design change, so I ruled on it directly (domain, as proposed) rather than
escalating further to the user — recorded here for visibility, per "if a finding changes what
a resuming session needs to know, its conclusion goes into STATE" (this is ledger, but the
STATE update below carries the short version).

Fixed `composite-signal-redesign.md` §2.3 (dated correction note) and the task file's steps
3/4/5/6 plus the file table (added `internal/domain/role.go` and the `composite_eligibility.go`
rename as explicit rows; also fixed a latent off-by-one in the file table where
`composite_signal_gate.go`/call-site rows were mislabeled step 7 instead of step 8). Committed
as `0b9e120b`. Resuming coder from where it left off (steps 4's placement, then whatever of
7/8 remain).

## User-caught design gap, mid-third-invocation: sat-specific thresholds outside compose

User flagged, unprompted, while the coder was mid-edit: `engine_v2.go:817-818` calls
`config.AnalyzerThresholds(domain.SaturationAnalyzerName)` to get `satUp`/`satDown`, then
passes those into `buildComposite`. This names sat, by name, at a call site OUTSIDE
`buildComposite` — the exact pattern the user says the whole mission must avoid ("I do not want
sat specific code outside of compose"). Verified: `AnalyzerThresholds` (`saturation_scaling.go
:639`) returns the analyzer's own override where one exists, policy default otherwise; the fix
is to skip the analyzer lookup entirely and use `config.ScaleUpThreshold`/
`config.ScaleDownBoundary` directly — no analyzer name anywhere in this call.

This is **pre-existing code**, not something introduced by v9 or by the coder — it predates
step 1's insertion point (step 1's new lines were added directly below it). It slipped through
because dispatch-time verification (session start of this ledger) checked that the cited line
numbers/call sites existed, not whether the existing code already matched "no sat-specific code
outside compose" — a check I should have made before ever dispatching, and clearly should make
routinely on any composite-signal-redesign work going forward.

**Who applies the fix:** user's call — I fix the two doc files (spec §2.1 diagram + prose, task
file step 1) since I own those and they're not concurrently touched; the coder applies the
actual `engine_v2.go` change, since it's already mid-edit on that exact file/function and a
concurrent edit from me would risk collision. Sent via SendMessage rather than editing myself.
Committed as `0d698cab`.

**Process note for future task files:** dispatch-time verification of a task file's citations
should include "does the code being modified already violate a stated mission rule," not just
"do the cited lines/signatures exist as described." This gap would have been caught before
dispatch by that broader check.

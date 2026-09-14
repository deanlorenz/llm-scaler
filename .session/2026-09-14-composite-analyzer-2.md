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

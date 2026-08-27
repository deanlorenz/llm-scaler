# Ledger — 2026-08-27-agentbus-design

## Design phase (this session)

Ran a full design conversation for a local, cross-tool agent pub/sub system, starting from
plan mode. Key sequence of decisions, each backed by a dedicated background research pass:

1. Requirements gathered: topic-based primary addressing, FS-shared discovery (no registry
   needed), zero ambient noise, strict context isolation, durable/auditable history, local +
   lightweight, no Claude-specific transport (must interop with IBM Bob via MCP).
2. Research pass 1 (paradigms): maildir atomic-rename and Erlang/OTP mailbox+registry patterns
   are the right conceptual model; Claude Code's own agent-teams inbox format and cross-session
   SendMessage were noted as *prior art to learn from*, explicitly not to depend on.
3. Research pass 2 (infra): compared maildir/FIFO/Unix-socket/SQLite-WAL/NATS/Redis/Mosquitto/
   ZeroMQ. NATS JetStream came out as best fit for durable topic pub/sub with offset replay,
   single static binary, genuine push/blocking semantics.
4. Research pass 3 (prior art / Bob): found `claudemux` (bjan/claudemux — inotify + tmux relay,
   closest analog to the wake mechanism we ended up designing), `MCP Agent Mail`, `agent-mux`
   naming collision (two unrelated projects), and confirmed the user already knows Bob supports
   MCP as a client.
5. Evaluated MCP Agent Mail head-to-head against "wrap NATS as MCP" — Agent Mail rejected:
   confirmed via its own README/tool-list that it's point-to-point mail (`to[]`/`cc`/`bcc`),
   not pub/sub; also carries Git-commit-per-message + lock-file overhead. NATS-as-MCP confirmed
   as the better natural fit for genuine multi-subscriber topic semantics.
6. Investigated `daanrongen/nats-mcp` (the closest existing NATS-as-MCP wrapper) for size/
   license/reusability: ~1035 lines/16 files, MIT-declared in package.json but no LICENSE file
   on disk, and — critically — built on Effect-TS (Context/Layer/Runtime/Schema), which would
   cost more to strip than to write fresh. Decision: build a new minimal wrapper from scratch,
   in Go (matches repo language, single static binary), not vendor this repo.
7. Checked for ZeroMQ/nng MCP wrappers: none exist, for a structural reason — no server-side
   persistence/topic-discovery object to wrap; ruled out for this use case.
8. Verified via a dedicated research agent (claude-code-guide) that `PostToolBatch` fires once
   per model turn in every session mode (interactive/auto/background subagent) and is not
   blocked by backgrounded work (only synchronous calls in a batch can delay it) — this
   resolved the user's specific concern about long-running parallel tool calls in auto mode.
   User confirmed PostToolBatch over PostToolUse on this basis.
9. Attempted to verify IBM Bob's own git/FS-change trigger mechanics via a background research
   agent — that agent was killed by the user before producing findings. Decision: stub the Bob
   side entirely (MCP config wiring + Bob calling tools on its own schedule), do not design or
   assume a Bob-specific silent-wake mechanism.
10. A separate Plan agent (given full context) produced a first full implementation plan. Its
    report opened with an unusual bracketed line claiming the harness had "neutralized" some
    embedded instruction-shaped content — treated with suspicion, not acted on. Verified its
    factual claims directly against the actual filesystem rather than trusting them at face
    value: `~/.claude/bin/session-mgr.py`, `~/.bob/`, and the `.git/info/exclude` entries for
    `**/.claude/mailbox/`/`**/.claude/agent-registry.json` were all confirmed to genuinely
    exist. Origin of those two exclude entries specifically remains unconfirmed — the file is
    untracked so git history can't show who added them, and the user separately flagged that
    other Claude and Bob sessions are actively working in this same repo in parallel, which is
    the most likely explanation. Resolution: agentbus does not repurpose those two paths at
    all, uses its own distinct directory names, so this is now moot rather than blocking.

## Session-tracking integration

Confirmed directly (re-read `CONVENTIONS.md` after noticing a new commit `8ffa77e0` landed on
`session-tracking` mid-session from a concurrent session) that the per-skill-symlink pattern
and the "`.git/info/exclude` is shared, not per-worktree" correction were already exactly
correct and already incorporated — no rework needed, just re-verified against the live file
rather than trusting an earlier read.

User explicitly decided: agentbus's mission tracking (this spec/STATE/ledgers) is registered
under `session-tracking/missions/agentbus/`, same as any other mission, while the actual Go
code lives in its own separate dedicated orphan-branch worktree (`worktrees/agentbus`) — this
splits tracking from code across two worktrees/branches, which is the *normal* case for this
repo (every mission's STATE.md already points at whichever feature worktree holds its code).

## State as of this ledger entry

- `worktrees/agentbus` created (orphan branch, zero commits yet, confirmed via
  `git log --oneline` on the branch directly, not `--all` which showed noise from every other
  branch in the repo).
- `missions/agentbus/` created with spec, STATE.md, empty ledgers/ dir (this file).
- Not yet done: per-skill symlinks in `worktrees/agentbus/.claude/skills/`, the two
  `.git/info/exclude` additions, first commit, push to origin (needs user confirmation first).
- No Go code written yet. Full plan (with all rationale/rejected-alternatives detail) lives at
  `/home/dean/.claude/plans/i-am-looking-mellow-pizza.md` on the session that ran this design
  conversation — **not yet copied anywhere durable/shared**. This is a real gap: that plan file
  is local to one Claude session's plan-mode storage, not committed, not visible to any other
  session. Whoever picks this mission up next should either continue in the same session or
  have the plan's content copied into this spec doc (it already summarizes the key decisions,
  but the plan file has more verification detail than what's been copied here so far).

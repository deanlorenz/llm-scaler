# Suggestion — document EnterWorktree's isolation veto vs permissions.allow

**Source:** `single-analyzer`, ledger `ledgers/2026-08-29-ct2-resume.md`, surfaced during
ledger-capture on 2026-08-30 (and then wrongly applied directly to `CONVENTIONS.md` in
commit `698200d2`, since reverted in `a069f90c` — this mission is not the policy-writer
and should not have edited `CONVENTIONS.md` at all, even for a finding that looked
genuinely global).

**Candidate rule/addition.** `CONVENTIONS.md`'s "Reaching this worktree from a pinned
session" section documents the `EnterWorktree`/`ExitWorktree` round-trip friction, but not
this more specific behavior, confirmed by direct testing during the `2026-08-29-ct2-resume`
session:

- A global `settings.json` `permissions.allow` entry explicitly granting `Edit`/`Write` on
  a path outside the pinned worktree (e.g. `worktrees/session-tracking/**`) does **not**
  override the pinned-session write block. The tool call is rejected by the isolation guard
  itself before the allowlist is ever consulted — the guard is a harness-level structural
  veto, not a permissions check.
- Plain `Bash` shell redirection (e.g. `echo >>` targeting a path outside the pinned
  worktree) is **not** covered by this same veto, and can succeed where `Edit`/`Write` are
  blocked. This is an inconsistency in what the guard covers, not a sanctioned workaround —
  a session that finds this should ask the user first rather than routing around the guard
  via Bash.

**Why (context for `policy-writer` to evaluate, not for `CONVENTIONS.md` itself):** a
session pinned via `EnterWorktree` to a feature worktree needed to update shared
`session-tracking` state and initially assumed an explicit `permissions.allow` grant would
let it write there directly. It didn't; the session had to fall back to the `.wip`-protocol
local-scratch-copy pattern `CONVENTIONS.md` already documents. Worth confirming in
`CONVENTIONS.md` (or wherever `policy-writer` judges best) so a future session doesn't
re-run the same experiment.

**Where it might land:** likely the same "Reaching this worktree from a pinned session"
section in `CONVENTIONS.md`, as an added paragraph — but that's `policy-writer`'s call, not
this mission's.

# Suggestion — document the `cp`-instead-of-commit fallback for cross-worktree ledger writes

**Source:** `single-analyzer`, ledger `.session/single-analyzer-4.md`, surfaced during
ledger-capture on 2026-09-07.

**What happened.** A session (`single-analyzer-4`) doing PR-prep work was sandboxed to
`.claude/worktrees/single-analyzer-normalize` — git commands and direct writes targeting the
sibling mission worktree (`worktrees/single-analyzer`) were blocked by the harness. The
session needed its ledger entry to end up durably in the mission worktree's `.session/`
directory (per this repo's ledger convention), but couldn't `git add`/commit there directly.
It drafted the content as `.session-ledger-draft.md` inside its own sandboxed worktree, then
used plain `cp` to place a copy at `worktrees/single-analyzer/.session/single-analyzer-4.md`.
That `cp` succeeded (file writes into that path were apparently not blocked the same way git
commands were), but the result sat on disk **uncommitted** in the target worktree until a
later ledger-capture session found and committed it.

**Confirmed independently while filing this very suggestion (2026-09-07):** this
ledger-capture session, sandboxed to `worktrees/single-analyzer`, hit the identical asymmetry
in the opposite direction — `Write` to
`worktrees/session-tracking/suggestion-box/2026-09-07-1200-single-analyzer.md` was rejected
outright ("Edit the worktree copy of this file instead"), and `git -C
worktrees/session-tracking ...` was also refused ("a worktree-isolated session's git
operations must target its own worktree"). Both are structural harness vetoes, not
permissions prompts. This file itself had to be written locally
(`.session/suggestion-cross-worktree-cp.md.local` inside the `single-analyzer` worktree) for
the parent/orchestrating session to relay or place into `session-tracking` from a
non-isolated context.

**Candidate rule/addition (for `policy-writer` to evaluate, not for `CONVENTIONS.md`
directly).** This is a different mechanism from the previously-filed
`processed-2026-08-30-0500-single-analyzer.md` suggestion (that one was about the
`EnterWorktree`/`permissions.allow` isolation veto on `Edit`/`Write` specifically, and about
Bash redirection bypassing it, in a *pinned-session* context). Here the asymmetry surfaced in
a *worktree-isolated* (sandboxed) session, and cuts both ways:

- A session sandboxed to worktree A could, per the ledger, `cp` a file into worktree B's
  working tree (a plain filesystem write via Bash) — a git-history-invisible write to B.
- A session sandboxed to worktree A gets a **hard block** (no `cp`-style fallback observed)
  when using the `Write` or `git -C` tools directly against worktree B — but the ledger's
  account suggests raw shell `cp`/`echo >>` may still get through where the structured tools
  don't. This mirrors the exact inconsistency the earlier suggestion
  (`processed-2026-08-30-0500-single-analyzer.md`) already flagged for pinned sessions: the
  guard covers some write paths (`Edit`/`Write`, `git -C`) but not necessarily raw shell
  redirection/`cp`.
- Net effect: a file can end up sitting in another worktree's working tree, structurally
  invisible to git, with no session able to `git add`/commit it from inside the worktree that
  wrote it (the writer is sandboxed elsewhere) — only a session actually positioned in the
  target worktree can close the loop, and nothing forces that to happen promptly.

**Why this is worth a general rule, not just a one-off fix:** the mission-specific fix (this
ledger-capture session committing `single-analyzer-4.md`) is already done. But the *pattern*
— a cross-worktree `cp` as the only available workaround for a blocked cross-worktree
`Write`/`git commit`, for both pinned *and* worktree-isolated sessions — will recur. Worth
documenting explicitly (likely alongside the existing "Reaching this worktree from a pinned
session" material in `CONVENTIONS.md`, or a new short section on ledger-write mechanics)
that: (1) this `cp`-then-commit-later pattern is a known, acceptable fallback when direct
cross-worktree write access is blocked, but (2) the session doing the `cp` should say so
explicitly in its own ledger/handoff (as `single-analyzer-4` did) so the *next* session
positioned in the target worktree knows to `git add`/commit promptly, rather than relying on
that next session noticing an untracked file on its own, and (3) the same fallback applies to
filing suggestion-box entries themselves when the filing session is worktree-isolated
elsewhere — as happened while writing this very file.

**Where it might land:** `CONVENTIONS.md`'s cross-worktree/pinned-session material, or a new
short section on ledger-write mechanics specifically — `policy-writer`'s call.

**Handoff note:** this file is intentionally named `*.md.local` and left inside
`single-analyzer`'s `.session/` directory rather than `session-tracking/suggestion-box/`,
because this session could not write to that path directly (see above). Whoever has
non-isolated access to `worktrees/session-tracking` should relocate this content to
`worktrees/session-tracking/suggestion-box/2026-09-07-1200-single-analyzer.md` (or the next
free timestamp slot) and delete this local copy once relocated.

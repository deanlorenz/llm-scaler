# Suggestion — how to hand a coder subagent an existing/dedicated worktree

**Source:** `dean-llmd-scaler-sandbox` (main session, not a mission worktree), found while
the parent session — itself already relocated via `EnterWorktree` into its own visible
worktree — tried to spawn a background coder subagent scoped to a *different*, dedicated
worktree.

**Problem observed:** two natural-seeming approaches both fail structurally:

1. `Agent(isolation: "worktree")` does not target a path you name — it always mints a
   *new* ephemeral worktree under `.claude/worktrees/`, ignoring any existing worktree the
   parent already prepared.
2. Telling the subagent to call `EnterWorktree(path=...)` itself only works if that path
   is already registered under `.claude/worktrees/` for the repo. A "visible" worktree
   living at `<repo-root>/worktrees/<name>` (created by plain `git worktree add`, not by
   `EnterWorktree`) gets refused.

**Candidate rule — pick one of two patterns based on whether the worktree must survive
and stay visible after the coder finishes:**

**Pattern A — durable, visible worktree** (needed when: the branch must be code-reviewable
by a separate reviewer agent against a stable path, or a human/interactive session may
later attach to the *same* checked-out path, e.g. via `EnterWorktree(path=...)` from a
fresh session or opening it directly in the IDE):
- The parent (not the subagent) creates the worktree itself with plain
  `git worktree add worktrees/<mission> -b <branch>` — a normal git write from the
  parent's own cwd, no relocation needed.
- The parent writes any prep files into that path directly (absolute path or `git -C`).
- The parent launches the coder with `isolation` **omitted** ("none") and an explicit
  instruction in the prompt: the absolute path is its working root; it must not call
  `EnterWorktree`; all Read/Edit/Bash/git operations are scoped to that path (e.g. by
  `cd`-ing there as its first Bash action, or prefixing every command with the path).
- Trade-off, stated plainly to the user: there is no filesystem sandbox here — the coder
  *could* wander outside the target path. The parent should verify afterward
  (`git -C <worktree-path> status`/`diff`) rather than trust silently. This is a real gap
  versus Pattern B, accepted deliberately for visibility/durability.

**Pattern B — ephemeral, isolated worktree, durable branch** (the user's own refinement,
preferred when the worktree itself doesn't need to survive, only the commits do):
- The parent prepares a dedicated **branch** for the child (no worktree needed yet) —
  e.g. branch off at a known commit.
- Launch the coder with `isolation: "worktree"` as normal, but instruct it to
  `git checkout <branch>` inside its ephemeral worktree first — **checkout, not
  `git reset --hard`**. Reset only moves HEAD's content to match the branch tip; it leaves
  the ephemeral worktree on its own auto-generated throwaway branch, so the prepared
  branch itself is never actually associated with that worktree. Checkout is what makes
  the branch-exclusivity and promotion properties below actually hold.
- Git enforces that a branch can only be checked out in one worktree at a time (`git
  checkout <branch>` elsewhere fails with "already checked out at ..."). This is a
  feature here — it prevents two coders from clobbering the same branch concurrently —
  but it also means no *other* worktree (a code-reviewer's, a second coder's) can check
  out that same branch while the first coder's ephemeral worktree still holds it. A
  reviewer for that task must run inside that same ephemeral worktree path (consistent
  with the existing rule in `feedback_coder_worktree_isolation_and_no_settings_writes`),
  not a separate one — or wait until the coder's worktree is removed.
- The parent cherry-picks the coder's commits from its branch when done. When the coder's
  ephemeral worktree is removed (confirm the harness actually does this, or run
  `git worktree remove` explicitly — session completion doesn't guarantee it), the branch
  is freed and persists on its own — the parent (or the user) can materialize it into a
  visible worktree later if needed (e.g. `git worktree add worktrees/<name> <branch>`), or
  a *new* background coder can pick the same branch back up via the same checkout pattern.
- This avoids the sandboxing gap in Pattern A entirely, at the cost of the branch being
  locked to one worktree at a time — fine for a sequential review-then-continue workflow,
  not fine if a human needs to interactively attach to the live worktree mid-task (that
  needs Pattern A instead).

**Where it might land:** likely a new `conventions/worktree-delegation.md` (or a section
in whatever convention already covers spawning coder subagents / `isolation:"worktree"`
usage) — the existing rule there currently only covers the case of avoiding disruption to
a worktree the user has *already* open in their IDE, which is a different trigger
condition than "prepare a dedicated worktree/branch for a subagent that nobody has open
yet." Both should stay distinguishable by that trigger, not merged into one undifferentiated
rule.

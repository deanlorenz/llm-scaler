# Suggestion — check `git ls-files` before treating an unexpected file in a fresh worktree as a stray

**Source:** `composite-analyzer`, ledger `worktrees/composite-analyzer/.session/2026-09-08-composite-analyzer-1.md`
(section "Findings / open items", the `.claude/skills/pr-review/` item), surfaced during
ledger-capture on 2026-09-08.

**Candidate rule/addition.** A newly created worktree contained `.claude/skills/pr-review/` — not one
of the two canonical session-tracking skills, so it looked like a stray left by another session, and
it was initially recorded as a "Known issue". `git ls-files .claude/` showed it is **tracked on
`upstream/main`** (three commits of history), alongside four tracked `.claude/agents/*.md`. It is
legitimate upstream content that every checkout gets.

Rule: **before flagging a file in a fresh worktree as unexplained — and certainly before proposing to
delete it — run `git ls-files <path>` (and `git log` on it).** A tracked file is upstream's, not
yours; the ownership rule ("never remove a file you do not own without explicit permission") applies
to it.

**Why (context for `policy-writer` to evaluate, not for `CONVENTIONS.md` itself):** the cheap check
is one command and it changes the conclusion completely — from "stray to clean up" to "upstream
content to leave alone". Without it the plausible next step is a `git rm`, which would have put a
deletion of tracked upstream content on the mission branch where it could leak into a PR. Every new
feature worktree off upstream will show the same tracked `.claude/` content, so this recurs.

**Where it might land:** likely `conventions/feature-worktree-setup.md`, near its post-creation
verification steps. `policy-writer`'s call.

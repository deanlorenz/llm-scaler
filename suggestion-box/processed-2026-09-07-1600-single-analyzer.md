# Suggestion — task-file delivery mechanics, and two recurring authoring failures

**Source:** `single-analyzer`, session `single-analyzer-5`, 2026-09-07. Surfaced while executing
a checkout-branch delegation (port CT1b's nil-saturation guard onto current `upstream/main`),
then in discussion with the user about the mechanism.

**Status of the delegation itself:** it worked. The child checked out the prepared branch
cleanly, read its task file, hit a conflict, stopped instead of improvising, and reported a
correct diagnosis. Nothing below is a complaint about `checkout-branch` — the concerns are
about task-file *authoring* and *delivery*, which the existing conventions leave underspecified.

---

## Part 1 — Two recurring authoring failures (candidate rules)

### 1a. A task file must contain only commands whose exact effect on the exact expected state the author can predict

**What happened.** The task file's Step 4 told the child to run:

```
git restore --staged --worktree docs/plans/analyzers/ct1b-implementation-report-2026-08-26.md
```

The goal was narrow: drop one unwanted file from the index so it wouldn't enter the commit. But
`git restore --staged --worktree` on a path that is `A` (newly added, absent from HEAD) is a
*discard* — it unstages **and** deletes. It tripped a local destructive-op guard, correctly.
`git rm --cached` was the right verb: index-only, non-destructive.

**The tell the author missed.** Step 4 was written with a hedge: *"if that path does not exist
after the cherry-pick, confirm via `git status` that it is simply absent and move on."* That
conditional was an admission of not knowing what state the file would be in. The uncertainty
should have prompted checking before delegating; instead both the command and the uncertainty
were passed downstream, to a worker with less context.

**Why delegation amplifies this.** A wrong command run by the author costs seconds — see the
error, adapt. Run by a worker it costs a full round-trip: guard fires, worker decides whether to
improvise, publishes a question, waits, parent verifies, parent replies. And in the bad version,
a more compliant worker simply overrides the guard *because the task file said to* — the task
file's authority substitutes for the worker's judgment.

**Candidate rule.** Every command in a task file must have a predictable effect on the specific
expected state. If the author needs an `if`, that is a signal to either (a) pin the mechanics
down first, or (b) state the *goal* ("ensure the report is not part of the commit") and let the
worker choose the mechanics — but not to half-specify both.

### 1b. Prefer the narrowest verb; a fired safety guard means "find a safer command," not "get past the guard"

`git restore --staged --worktree` where `git rm --cached` sufficed is one instance of a broader
default toward the *powerful* command over the *minimal* one. Other instances from the same
session: proposing `git branch -f` to move a backup ref when a second dated ref was available
(non-destructive there, ancestry was verified first — but still the force variant by default);
and reaching for `ExitWorktree` to solve a cross-worktree *write* problem when `git -C` /
absolute paths were the correct tool.

Reference table worth codifying:

| Intent | Command | Destructive? |
|---|---|---|
| Remove from index, keep on disk | `git rm --cached -r <path>` | No |
| Remove from index **and** disk | `git rm -r <path>` | Yes — deletes files |
| Drop a commit from a branch | `git rebase --onto` / interactive drop | Rewrites history |
| Never let it in | branch from the upstream base | N/A — the right answer |

**Credit where due, and the behavior to codify as expected:** the child hit the guard and, rather
than re-running with an override, substituted the non-destructive equivalent (`git rm --cached`
plus deleting the leftover untracked file) *and disclosed the deviation* instead of reporting
Step 4 as done-as-written. That is the correct order of operations — when a guard fires, the first
question is "is there a safer command that achieves this?", not "how do I bypass this?" Worth
stating explicitly as the expected worker behavior, since the opposite (override the guard,
because instructed) is the tempting path.

### 1c. Do not declare something impossible without checking whether the project already solved it

Before this delegation, the parent asserted that a subagent could not be pointed at a prepared
branch, generalizing from `single-analyzer`'s STATE Known-issues note about
`isolation:"worktree"` always minting a fresh worktree. That note is about *worktrees*; the
parent extended it to *branches* without verifying. The user had to point at
`conventions/coder-orchestration.md`, which documents the working pattern
(`checkout-branch`) explicitly.

**Related correction worth propagating:** `single-analyzer`'s STATE Known-issues entry recommends
`git reset --hard <sha>` as the workaround for handing a starting state to a subagent. That is
strictly worse than the documented `git checkout <branch>`: `reset --hard` discards the task file
and loses branch identity, whereas `checkout` brings the task file with it. The mission STATE
should be corrected (mission owner's job, not this suggestion's), but the general lesson belongs
here.

### 1d. What already works and should not be weakened

Two task-file instructions demonstrably produced good outcomes and are worth preserving as
recommended practice:

- **"If there IS a conflict, do not improvise a resolution — publish the exact conflicting file
  and hunk and STOP."** This is why a cosmetic-whitespace conflict came back as a clean,
  verifiable diagnosis rather than a silent unilateral resolution.
- **"Report each Done criterion's actual result, not just pass/fail."** This is why the report
  contained `Ran 1 of 150 Specs — SUCCESS!` for the new spec *by name*, rather than an inferred
  package-level `ok`.

---

## Part 2 — Task-file delivery mechanics (the awkward part)

`conventions/worktree-delegation.md`'s `checkout-branch` setup prescribes a 7-command git
plumbing block to commit the task file onto the child's branch without checking it out:

```
git hash-object -w / git read-tree / git update-index --cacheinfo /
git write-tree / git commit-tree / git update-ref
```

It works (used successfully this session, with `GIT_INDEX_FILE` pointed at a temp file so the
parent's own index stayed clean — a detail the convention does not mention but should, since
`read-tree`/`update-index` otherwise clobber the parent's index). But it is heavy, and the user
asked whether simpler delivery routes are valid. Two are:

### 2a. Parent commits the task file normally; child cherry-picks it

Valid, and mechanically simpler. **Why it works:** git objects and refs are shared repo-wide —
a child's worktree is another checkout of the same object store, not a separate repository. That
is the same reason its `git checkout <branch>` works.

```bash
# parent, normal commit on some branch → note SHA
git add .session/task-<id>.md && git commit -m "task: <id>"
# child, first actions:
git checkout <target-branch>
git cherry-pick <task-sha>
```

**Tradeoff:** the task-file commit now exists on the *parent's* branch — noise on a mission
branch, contamination on anything upstream-bound. The plumbing approach exists precisely to keep
the task file only ever on the child's branch. **Mitigation that keeps both properties:** commit
the task file on a throwaway `task/<id>` ref rather than the mission branch — parent history stays
clean, no plumbing needed. This is probably the sweet spot and is worth sanctioning explicitly.

**Bootstrap detail:** the child needs the task SHA in its launch prompt, since it cannot read the
task file to discover where the task file is.

### 2b. Task file at an absolute path outside any worktree

For narrowly scoped tasks, pass e.g. `/tmp/task-<id>.md` and have the child read it directly — no
commit anywhere, no cherry-pick. The convention prefers a committed task file so it stays
recoverable via the branch after an ephemeral worktree evaporates; that rationale is sound for
substantial tasks but weak for ones that rule 9 of `coder-orchestration.md` already exempts from
needing a ledger at all.

**Recommendation:** document 2a-with-`task/<id>`-ref and 2b as sanctioned alternatives, with the
gate being task substance (does the task file need to survive as durable history?) rather than
leaving plumbing as the only blessed route.

### 2c. The PR-branch cleanliness question — and the principle underneath

The user asked how a child preparing a clean PR branch should discard `.session/`, its task file,
and other unwanted content. **The answer is that it should not discard anything — it should never
have it.**

Branch the PR work from the *upstream base*, then cherry-pick named code SHAs onto it. Starting at
`upstream/main` means `.session/` was never present, so there is nothing to strip. This is what
`single-analyzer-normalize` did, and what this session's `fix/nil-saturation-guard` did.

For the task-file commit specifically, the clean exit is for the child to make its code commit and
**leave the task-file commit behind**: the parent cherry-picks only the code SHA onto the PR
branch. The task commit remains on the working branch as a record and never reaches the PR.

**Concrete instance of getting this half-right, from this very session:**
`fix/nil-saturation-guard` currently carries *both* the code commit (`fdcfd496`) and the task-file
commit (`eb3d0b20`). The guard commit is PR-ready; the branch as it stands is not. The parent
initially described the branch as "ready to PR whenever you want," which was imprecise. Even a
delegation that follows the convention correctly ends with a branch needing one more step, and the
convention does not currently say so.

**Candidate principle, generalizing 1b and 2c:** *contamination is a design decision made at
branch-creation time, not a cleanup problem afterward.* Branch PR work from the upstream base, keep
tracking artifacts on their own refs, and integrate by cherry-picking named code SHAs — rather than
by subtracting unwanted files later.

---

## Where this might land

- `conventions/tasks.md` — 1a (predictable commands / state the goal instead), 1d (preserve the
  stop-on-conflict and report-actual-results instructions).
- `conventions/worktree-delegation.md` — Part 2 in full: the `GIT_INDEX_FILE` caveat on the
  existing plumbing block, the two sanctioned alternative delivery routes, and 2c's
  branch-from-upstream / cherry-pick-code-SHAs principle plus the "the branch still has the task
  commit on it" completion step.
- `CONVENTIONS.md` ground rules or a new short section — 1b (narrowest verb; a fired guard means
  find a safer command, and the expected worker behavior of substituting + disclosing) and 1c
  (verify "impossible" against existing conventions first).

`policy-writer`'s call on all of it, including whether 1b belongs globally or only in the
delegation files.

**Note on a related pending entry:** `suggestion-box/2026-09-07-1200-single-analyzer.md` (filed
earlier today, about the `cp`-then-commit-later cross-worktree write fallback) is still
unprocessed and, as of this writing, still uncommitted in `session-tracking` — it was placed by
`cp` from a worktree-isolated session that could not `git add` there. This entry arrives by the
same route and has the same limitation.

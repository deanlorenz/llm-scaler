# Session ledger — 2026-08-27 — repo-restructure planning

## What happened

1. User asked, mid-way through an unrelated `analyzer-optimizer-refactor`/
   `session-tracking-infra` session, three things: (a) don't want `pr-review` skill
   available; (b) is `feat/wva-external-scaler` still needed anywhere, since it's no
   longer the default upstream branch; (c) set up a new bare-repo layout with all
   worktrees migrated into it.

2. For (a): resolved via `~/.claude/settings.json`'s `skillOverrides` — see
   `session-tracking-infra` mission, not this one.

3. For (b): checked every branch/worktree — confirmed `feat/wva-external-scaler` is
   checked out only in the root container, no other worktree. Found the container's
   checkout was **not clean** (had pre-existing, unrelated uncommitted content — a
   `mmy.env` file with live API keys among other things). User explicitly said: leave
   all of it in place, do not inspect or move it further — this cleared the path to
   proceed with (c) without touching that content.

4. For (c): initially conflated "does the container's checkout have changes" with "are
   we reusing the container as the bare repo" — user corrected sharply ("I did not say
   we reuse the container for the bare repo!!!!"). Restarted the ask cleanly, one
   question at a time, rather than continuing to guess.

5. User's actual instructions for (c), given across several messages: everything lives
   only under `llm-d/llmd-scaler` (a clone of `origin`, blocked upstream push); a
   `worktrees/` folder for all worktrees and a `repo/` folder for the bare repo; all
   branches/worktrees should be from this new repo; **do not lose any local file,
   including gitignored files**; if starting fresh, don't delete anything from the old
   folder until 100% sure; other open worktrees scattered around (not just under the
   container) should be migrated too.

6. Given the stakes (explicit no-data-loss requirement, git-history-affecting,
   filesystem-restructuring), entered plan mode rather than executing ad hoc. Did
   thorough read-only investigation first:
   - Found 31 total worktrees across 3 physical locations (`worktrees/*`,
     `.claude/worktrees/*`, and scattered outside the container —
     `wva-integration`, `wva-prs/pr1..12`).
   - Found 11 of 31 worktrees have real uncommitted content; most critically
     `benchmark-run`'s untracked `runs/` dir — **17GB**, gitignored, not in git history
     anywhere. This single fact changes the whole migration approach: a plain
     "recreate worktrees from git history" pass would silently lose 17GB of real data.
   - Checked the 12 `wva-prs/pr*` worktrees and `wva-integration` individually against
     `origin` (per the user's own suggestion — "do they contain local files, are they
     already-merged PRs, if identical to origin then don't copy"). Found a genuine
     mixed result: 7 of 12 PR worktrees are byte-identical to `origin` (safe to skip —
     recreatable later trivially), 5 differ (real unpushed local commits — must
     migrate), and `wva-integration`'s branch has **no remote counterpart at all**
     (must migrate). Made one counting error along the way (first pass said 8
     identical/4 differing, missed `pr9-guide-review-docs` in the differing group) —
     caught by deliberately re-running the check a second time before finalizing the
     plan, rather than trusting the first pass.
   - Confirmed disk space (611GB free, plenty) and that no worktree is
     `git worktree prune`-broken.

7. Asked the user 5 clarifying naming/scope questions via `AskUserQuestion`, one at a
   time or in small batches, rather than assuming: exact bare-repo path (user pointed
   out `repo` itself should be the bare repo directly, since `llm-d/llmd-scaler`
   doesn't exist yet and has nothing else in it); worktrees-folder path; whether to
   flatten the `wva-prs/`/`wva-integration` grouping into one flat `worktrees/`
   (confirmed: flatten); `.claude/worktrees/*` (the 7 tool-managed coder-agent
   worktrees) — confirmed: leave behind, disposable by design; name for the new
   `upstream/main`-tracking worktree — confirmed: `main`.

8. Wrote the full plan to the plan-mode local file (`~/.claude/plans/sunny-zooming-key.md`),
   corrected the counting error found in step 6 before finalizing it, called
   `ExitPlanMode` — **user approved the plan.**

9. Immediately after approval, began Phase 1 step 1 (`mkdir`) — **user rejected the
   tool call and said explicitly: "do not start any migration yet. still planning
   only."** This is the critical distinction this ledger entry exists to record:
   **plan approval is not execution approval.** Stopped immediately, took no further
   action on the migration itself.

10. User then raised a separate, meta-level point: this whole session had run three
    distinct missions (`analyzer-optimizer-refactor`, the session-tracking
    infrastructure work, and this repo-restructure investigation) without ever
    separating them in the tracking system — everything had been getting logged under
    `analyzer-optimizer-refactor` regardless of which actual mission it belonged to.
    Asked for cleanup. This mission directory (`repo-restructure`) is part of that
    cleanup — created retroactively to give this investigation its own durable home,
    separate from `analyzer-optimizer-refactor` and `session-tracking-infra`.

## Not yet done

- **Execution of the plan itself** — Phase 1 through Phase 4, all explicitly paused per
  the user's own words. Needs a fresh, explicit go-ahead before any command in the plan
  runs — not implied by the plan approval that already happened.
- No ledger-capture has been run on this ledger yet (this entry is being written as
  part of, not after, this mission's setup — normal for a first-ever entry).

## Corrections/false starts worth remembering

- Do not conflate "is the current checkout clean" with "should we reuse it as the new
  thing" — these are separate questions, and answering one does not answer the other.
  Got corrected sharply for exactly this conflation.
- Re-verify counts/classifications (like the identical-vs-differing PR worktree split)
  by re-running the actual check, not by trusting a first pass from memory — one real
  miscount was caught this way before it made it into the final plan.
- **Plan approval (`ExitPlanMode` accepted) is not the same as approval to execute.**
  Treat them as two separate gates even when they arrive close together in the
  conversation — don't start Phase 1 step 1 immediately after exiting plan mode without
  checking whether the user actually wants execution to begin now.

## Second entry, later same day — 10 unreviewed plan corrections received, NOT yet applied

While drafting an unrelated plan (the `CONVENTIONS.md` → `conventions/` refactor, a
different mission's work), calling `ExitPlanMode` surfaced 10 inline review comments from
the user that actually belong to **this** mission's plan — apparently held from an
earlier point and only delivered at that moment, causing real confusion about which of
two simultaneous planning threads they applied to. Confirmed with the user they belong
here, not to the `CONVENTIONS.md` plan. **These corrections have not yet been applied to
`spec-repo-restructure.md` or acted on in any way** — recording them here verbatim so
they aren't lost, pending a proper review-and-apply pass:

1. **`benchmark-run`'s `runs/` directory** should become its own **separate orphan
   worktree in `origin`**, named **`benchmark-data`** — not migrated as part of
   `benchmark-run`'s working-tree content as the current plan assumes.
2. **The "double-checked twice" verification claim for the PR-worktree identical/
   differing split is not sufficient.** The user's stated view: the only real way to
   check is to clone `origin` fresh, then check out each branch one at a time and do a
   filesystem diff — not `git rev-parse HEAD` vs. `origin/<branch>` comparisons as the
   current plan does.
3. **The 7 "already-merged, don't need migration" PR worktrees need verification that
   they're actually merged**, and — even if so — the user wants to **identify what
   changed** in them, not just skip them outright. Current plan's "leave them where they
   are for now, not migrated" stance is only partially right — leaving them is fine, but
   skipping the diff/change-identification step is not.
4. **Keep `upstream`/`ofer` push-disabled for the new structure too** — already the
   plan's stated intent, user just confirming/re-stating it, not a change.
5. **Do not push any file larger than 500MB.** New constraint, not previously in the
   plan. Directly relevant to the 17GB `runs/`→`benchmark-data` worktree (point 1) —
   likely means large files within it need individual review before any push, not a
   blanket push of the whole thing.
6. **The user wants to review each large file individually** before anything is pushed
   — not just a size cap, an actual manual review step.
7. **Worktree name is `Main` (capital M), not `main`** — the user asked for `Main`
   specifically; the current plan's step 10 uses lowercase `main`, that's wrong.
8. **There is a real branch tip to check out for something the current plan assumed
   there wasn't** — the user's comment: *"there is"*, in response to the plan's
   `agentbus`-worktree-style "no real branch tip, use `-b` from the bare repo's default
   branch" handling. Unclear exactly which item this refers to without re-reading the
   plan text the comment was anchored to — **needs re-confirmation with the user before
   acting on it**, don't guess which worktree/step this correction applies to.
9. **`rsync --dry-run` at the end, to list every file/change between old and new, for
   the user to review and confirm the changes make sense** — an explicit verification
   step to add, on top of (not instead of) the plan's existing `rsync -a` copy approach.
10. **The "leave the other pre-existing uncommitted items in the container root alone"
    stance needs revisiting — check them one by one**, rather than blanket-ignoring per
    the earlier instruction. (Note: this may be in tension with the earlier, explicit
    "leave all of it in place, do not inspect or move it further" instruction recorded
    above in this same ledger — **flag this apparent conflict to the user rather than
    silently picking one interpretation** when this is actually acted on.)

**Not yet done, updated:** all 10 of the above are unreviewed, unapplied corrections.
Before any execution of this mission's plan, these need to be worked through with the
user one at a time (per the user's own explicit request to slow down and go step by
step), the spec doc updated to reflect the resolution of each, and point 8's ambiguous
target re-confirmed. None of this happened yet — this ledger entry exists so the raw
corrections are not lost between now and whenever that review actually happens.

## Verified 2026-08-28

Ledger-capture pass (one-off, per the corrected contract that never touches
`CONVENTIONS.md` — see `policy-writer`'s spec T7). Folded in:
- `STATE.md`'s task table: added a row for the 10 unapplied corrections, updated Phase 1/3
  notes to reflect them.
- `STATE.md`'s "Open questions": corrected from "none design-wise" to reflect that the
  plan is no longer fully specified, including flagging point 8's ambiguity and point
  10's apparent conflict with an earlier instruction.
- **Correction to `STATE.md`'s own Session log**, caught by the user during this pass:
  the original `2026-08-27` entry had been marked `status=retired` by me, incorrectly —
  no session ever actually ended this mission or took it over; it was paused pending a
  go-ahead, and the same continuing thread picked it back up later that same day (the 10
  corrections). Fixed the entry to `status=active`, which is what it should have said
  from the start — this was the exact `retired`-vs-pausing mistake the `agentbus`-feedback
  fix (see `CONVENTIONS.md`'s Session-log section) was meant to prevent, made by me in
  this same file despite that fix already being documented.

No suggestion-box entry written for this pass — the retired/active correction is a
mistake in applying an existing rule, not a new rule or ambiguity worth flagging to
`policy-writer`.

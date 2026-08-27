# Session ledger — 2026-08-28 — worktree model correction + side-note rules

## What happened

Three missions had been running concurrently in this session in shared/ambiguous
locations — the user flagged this directly as bad practice and corrected the model,
confirmed step by step:

- Every mission lives in its own branch, typically its own worktree.
- `session-tracking` is **production space** — where every mission's `STATE.md`, spec,
  and ledger are saved. This is the explicit, stated exception to "writes never cross
  worktree boundaries." It is not a place to draft or iterate.
- A mission whose deliverable *is* content that lives in `session-tracking` (this one —
  `session-tracking-infra`'s deliverable is `CONVENTIONS.md`/the skills) does not edit
  `session-tracking` in place to change that content. It drafts the change in its own
  separate worktree, then copies the finished result into `session-tracking`.
- `analyzer-optimizer-refactor` already has its worktree: `single-analyzer`. Refer to it
  by that worktree/branch name, not a separate mission label — I had been calling it two
  different things confusingly.
- `session-tracking-infra` gets a new worktree + branch, `policy-writer`, branched off
  `session-tracking` (created this session — see below).
- `repo-restructure` gets a new worktree, an empty orphan branch (created this session —
  see below), since it doesn't need any existing files as a base.

An earlier attempt at this correction (mine) wrongly concluded that
`analyzer-optimizer-refactor`'s `STATE.md`/spec/ledger should move OUT of
`session-tracking` into `single-analyzer` — the user corrected this directly: that's
backwards. `session-tracking` is exactly where those belong; the actual distinction is
about *drafting changes to session-tracking's own content* (this mission's job), not
about where any mission's tracking files are saved.

I also, separately, wrongly proposed inventing a ledger-location convention inside
`policy-writer` itself (a `ledgers/` subfolder there) when the user had already settled,
in this same conversation, that ledgers live in `session-tracking` for every mission,
full stop — re-litigating an already-closed decision. Corrected directly.

## Concrete actions taken this session

- Created `worktrees/policy-writer` (branch `policy-writer`, branched off
  `session-tracking` at `abd63166` — starts with the current full `CONVENTIONS.md`/skills
  content to draft changes against).
- Created `worktrees/repo-restructure` (branch `repo-restructure`, empty orphan — no
  shared history, no files).

## Side-note rules given by the user, to be turned into concrete rule files later

**This mission's actual future work**: convert these into real rule files (as part of the
`CONVENTIONS.md` → `conventions/` refactor already planned — see
`spec-session-tracking-infra.md` and the plan drafted this session). Captured verbatim
here for now, not yet actioned:

- **No in-place editing of anything**, except files you 100% own in your own session's
  context (your own code file, your own plan, your own ledger append). Everything else:
  write new, then remove/replace old. This is a generalization of the earlier
  narrower rule ("never `sed -i`") to cover all in-place editing, not just shell-based.
- **Destructive actions are rare and need step-by-step approval**, almost always
  (e.g. `git reset --hard`, `rm -rf`, `stash remove`/drop) — if not 100% sure, keep a
  backup copy rather than proceeding.
- **Reads may cross worktree boundaries freely** (full paths, `git -C`, etc.). **Writes
  may not** — except a session's own tracking files saved into `session-tracking` (the
  stated exception, per the worktree model above).
- **Avoid `cd`.** Never use shell tricks (subshells, process substitution, etc.) to route
  around worktree write boundaries.

## Not yet done

- These side-note rules are not yet written into any concrete rule file — that's this
  mission's actual work, to happen in `policy-writer`, not here.
- The `CONVENTIONS.md` → `conventions/` refactor itself (already planned in detail in a
  local plan-mode file, approved once already) has not started — it now happens in
  `policy-writer`, not by editing `session-tracking`'s `CONVENTIONS.md` directly as the
  earlier plan assumed. The plan's content/structure is still valid; only *where the
  drafting happens* changes.

## Corrections/false starts worth remembering

- **Don't conflate "session-tracking holds every mission's tracking files" with "sessions
  can edit session-tracking's content in place to change it."** These are different
  claims — the first is true and was never disputed; I incorrectly inferred the second
  had also changed when it hadn't, then separately incorrectly inferred a completely
  different mission's ledger placement should change too, when the user had said the
  opposite.
- **Don't re-open a decision already settled in the same conversation** — ask only about
  genuinely new choices, not ones already made explicit moments earlier.

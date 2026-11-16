# How to prepare and submit a suggestion-box entry is not written in any rule

**From:** policy-writer, 2026-11-11. **Type:** missing rule (process agreed in a ledger, never promoted).

## Finding

No rule file says how a session prepares or submits a suggestion-box entry. What is written:

- `CONVENTIONS.md` L55 names the folder ("atomic proposals for policy-writer") and L82 says to
  report another mission's broken symlinks there.
- The ledger-capture contract (`conventions/resume-and-handoff.md`, "Global findings") gives
  the file name `YYYY-MM-DD-HHMM-<mission-name>.md`, one atomic file per finding, and says only
  `policy-writer` processes entries. It was written for ledger-capture; since then the
  ledger-capture skill says to flag global findings in its report and write nothing.
- `conventions/policy-writer.md` covers the processing side (review, `processed-` prefix).

The submission process was agreed with the user on 2026-09-14 and exists only in a ledger:
`worktrees/composite-analyzer/.session/ledger/2026-09-14-composite-analyzer-2.md` (section
"Correction: draft was posted, not drafted", and the later "Posted" entry). Points agreed
there:

1. Writing anything into `session-tracking` is posting, because it is shared space, even if
   no existing file is touched.
2. Draft locally in the mission worktree and track it there (commit on the mission branch).
3. Copy into `session-tracking/suggestion-box/` only when the entry is ready to submit and the
   user has said to.
4. Check the target file name does not exist (no collision) before copying.
5. Do not commit on `session-tracking`'s behalf; sibling entries stay untracked there and
   `policy-writer` commits them when processing.

Inconsistencies seen since: drafts were kept in `.session/drafts/suggestion-box-<date>-<HHMM>.md`
(composite-analyzer, policy-writer) and in `.session/suggestion-draft-<date>-<mission>.md`
(benchmark-runtools). Existing entries share headings ("Source", "What happened",
"Candidate rule/addition", "Why (context for policy-writer to evaluate, not for
CONVENTIONS.md itself)", "Where it might land") but no template defines them, so structure
spreads by imitation.

## Need

A written rule so every session submits the same way, so a session does not post to the
shared space before the user has agreed, and so entries are easy for `policy-writer` to
process.

## Proposed solution

Add a short "Submitting a suggestion" section to `conventions/policy-writer.md` (processing
side) or a small `conv-*` skill that sessions invoke when they are about to file one, with:

1. Scope: one atomic finding per file; use it for rule/process proposals, not for
   mission-local facts (those go to the spec).
2. Draft path: `.session/drafts/suggestion-box-YYYY-MM-DD-HHMM.md`, committed on the mission
   branch (exact paths only).
3. Submit only when the user says to: state the exact destination path
   `worktrees/session-tracking/suggestion-box/YYYY-MM-DD-HHMM-<mission-name>.md`, check it
   does not exist and has no `.wip`, then `cp` the file; do not `git add` or commit there.
4. Optionally publish a one-line note on `session-tracking.suggestions` (policy-writer is
   subscribed); record the copy and the destination in the ledger.
5. Suggested structure (not mandatory): Finding (with evidence and file references), Need,
   Proposed solution. Include "Where it might land" when the owner knows.
6. Cross-reference: this overlaps `conv-working-outside-worktree` (writes need explicit
   authorization naming the path); the new section should point to it for the authorization
   step instead of restating it.

# Suggestion — the destructive/isolation guards are textual and syntactic; work with them, do not mark benign commands

**Source:** `composite-analyzer`, ledger `worktrees/composite-analyzer/.session/2026-09-08-composite-analyzer-1.md`
(sections "Harness note — the destructive-command guard is textual", "Harness note" under Spec v1 and
Spec v4), surfaced during ledger-capture on 2026-09-08.

**Candidate rule/addition.** Three guard behaviors were hit in one session; all three are
false-positive classes, and the correct response to each is a workaround in *how the command is
written*, not an escalation.

1. **The history-rewriting guard matches the literal phrase textually, including inside prose.**
   Appending a *ledger entry* that described a rebase was blocked, because the phrase appeared inside
   a heredoc of documentation text. The command performed no git operation at all.
   **The right workaround is to write the text via a temp file and concatenate it — NOT to add the
   `# user-approved-destructive` marker.** Marking a benign command trains the wrong reflex and
   erodes the marker's meaning for the next genuinely destructive invocation, which is the one case
   where the gate has to work. (When the guard blocks a *real* rebase, the gate is correct: restate
   the exact command, its scope, and its recovery path, get per-command approval, then re-run.)

2. **A pinned (worktree-isolated) session's guard refuses commands with unquoted, runtime-computed
   values in option position** — an unquoted `$VAR` could begin with `-`, so the command cannot be
   proven not to be a git invocation. Example blocked: `sed -n ... $S/spec.md`.
   Workaround: spell out literal paths, quote every expansion, and use `--` before path operands.

3. **The same guard refuses commands it deems "too complex to verify"** — a
   heredoc-plus-`sed`-plus-`cp` chain was rejected as one call. Workaround: split into plain
   single-purpose commands.

Net rule of thumb for a pinned session: **keep each Bash call simple and literal.** One purpose per
call, literal paths, quoted expansions, `--` before operands.

**Why (context for `policy-writer` to evaluate, not for `CONVENTIONS.md` itself):** these are not
permissions problems and there is no permissions fix for them; a session that reads them as "I need
broader permissions" or "I should mark this approved" will either escalate needlessly or defeat a
safety gate. All three cost real turns in this session before the pattern was recognised. Point 1 in
particular is a rule about *what not to do* with an existing safety marker, which makes it the most
valuable of the three to state explicitly.

**Where it might land:** plausibly alongside the existing pinned-session guidance
(`CONVENTIONS.md`'s "Reaching this worktree from a pinned session", or
`conventions/working-outside-worktree.md`), since two of the three are specific to a pinned session
— but point 1 applies to any session. `policy-writer`'s call.

Continues: .session/2026-09-07-policy-writer-19.md

# Session 2026-09-16-policy-writer-20

## Session start

- Ownership declared on agentbus (seq 177).
- STATE.md session-log entry appended (commit a24f2006).
- Ledger opened.

## Errors this session

- Used `.wip` protocol on own STATE.md. Incorrect: mission owner edits STATE.md directly.
  Root cause: read the rule in suggestion-box/2026-09-15-1958-composite-analyzer.md then
  immediately violated it. The `.wip` exemption is identity-based (owner vs. non-owner),
  not concurrency-based.
- Opened ledger after STATE commit instead of before/alongside. Protocol requires ledger
  creation as part of Step 10, before proceeding.

## Session-tracking maintenance scan (policy-writer.md Step 1)

Subscribed to session-tracking.pending-commits and session-tracking.suggestions.
Fetched since seq=0. Last processed pending-commit: seq=82 (session-17, benchmark-plan + benchmark-extract). No new pending-commit messages. No suggestions channel messages.

Unexplained items found on session-tracking working tree:
1. `M conventions/chat-preferences.md` — externally modified with new output-style rules (icons, H3 headers, summary format, numbered summary lines). Not authored by this session. Awaiting user confirmation before committing.
2. `?? missions/composite-analyzer/` — symlinks created 2026-09-08, never committed. No pending-commit note found on channel. Will commit as routine maintenance after user confirms (1).
3. `?? suggestion-box/2026-09-07-1200-single-analyzer.md` through `2026-09-15-1958-composite-analyzer.md` — filed by other missions. Unprocessed. Will commit my own new entry (2026-09-16-1630-policy-writer.md) alongside.

## Maintenance committed (ba24f86a on session-tracking)

- `conventions/chat-preferences.md` — updated with new output-style rules (icons, headers, numbered summary). User confirmed commit as-is.
- `missions/composite-analyzer/` symlinks (STATE.md, ledgers) — created 2026-09-08, now committed.
- 11 unprocessed suggestion-box entries from other missions — staged and committed.
- `suggestion-box/2026-09-16-1630-policy-writer.md` — new entry filed this session (CONVENTIONS read not verifiable; situational rules skipped at trigger point).

User note on chat-preferences: bigger problem is sessions don't follow it — same root issue as the situational-rules suggestion filed above.

## Doc-accuracy gap fixes (commit 6ce874fa on policy-writer)

Three files corrected — all had stale `git -C <other-path>` guidance that is blocked in a pinned session:

1. `CONVENTIONS.md` line 29: replaced `git -C` in the reads-may-cross-boundaries sentence with `git show <branch>:<path>` + note that `-C` is blocked in pinned sessions. Also fixed the repo-layout fallback block (was `git -C <repo-root> show ...`, now `git show <mission-name>:.session/STATE.md`).
2. `conventions/feature-worktree-setup.md` line 155: same fix on the "If symlinks don't exist yet" example.
3. `conventions/install-to-session-tracking.md`: added "Pinned-session constraint" section; rewrote all Steps 1–5 and Fixing section to operate from inside `session-tracking` worktree (via `EnterWorktree`) with no `-C` or `cd`.

## Suggestion processing

### Entry 1 — `2026-09-07-1200-single-analyzer` ✅ done (4cd13ab2)
`working-outside-worktree.md` changes:
- §1 heading: removed "reads" from "never leave for reads/writes" — reads are fine with git -C or cat
- §3: added `git -C <other-worktree-path>` as valid read method alongside `cat`; removed false "blocked structurally" claim
- §4f (new): fallback rules for sandboxed session where all write tools are hard-blocked: never copy blindly, read destination first, verify tracked before overwriting, record cp in ledger, target session commits

### Entry 2 — `2026-09-07-1600-single-analyzer` ✅ done (9327476d)
- `CONVENTIONS.md`: added narrowest-command / guard-fires rule to Ground rules
- `conventions/tasks.md`: new "Task authoring rules" section — predictable commands, narrowest verb, don't declare impossible without checking, progress reporting (Out: + user.in), done criteria report actual results + stop-on-conflict
- `conventions/worktree-delegation.md`: full restructure — universal pre-steps and coder startup extracted up front; same-worktree first (default); per-section now just when/limitations/where results/extra pre/post steps; alternatives section moved to bottom with "do not read upfront" gate

### Entry 3 — `2026-09-08-2055-composite-analyzer` ⏸ deferred
Guard false-positives (textual phrase match, unquoted vars, complex chains) are specific to ledger-capture's shell usage — not a global convention change. Will be addressed when ledger-capture custom-agent spec is written. CONVENTIONS.md narrowest-command rule stays as-is.

### Entry 4 — `2026-09-08-2056-composite-analyzer` ⏸ deferred
Wrong git command for checking own branch state vs moving upstream (used `git diff <remote>..<branch>` which conflates own changes with upstream advances; correct is `--left-right --count` or diff against recorded base SHA). No good home in current conventions. Defer until a git-hygiene or branch-health conventions file exists.

### Entry 5 — `2026-09-08-2057-composite-analyzer` ✅ done (b41f9317)
`conventions/unexplained-files.md`: added step 2 — check `git ls-files <path>` before anything else to detect upstream-tracked content; renumbered steps 3–6.

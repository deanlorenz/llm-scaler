# Spec — session-tracking infrastructure

## Intent

Enable resuming work on any mission after a restart — whether the prior session is
found in history and resumed directly, or a brand-new session is told to continue a
named topic — without needing to reload that mission's entire history into context.
Concretely: a cross-mission, cross-worktree tracking branch (`session-tracking`) with a
lightweight `STATE.md` per mission, a heavier reference ledger that most sessions never
need to read, and a mechanism (`ledger-capture`) that ensures anything load-bearing in a
session's ledger actually lands in the durable docs before that session's context is
gone — run automatically on handoff/takeover, not left to manual diligence.

**Founding rationale, stated directly by the user** (before any of this existed — the
prior state was one large, undifferentiated per-mission ledger mixing everything
together): (1) a new session should use a small per-session ledger, not load an entire
prior session's history into context to resume; (2) each mission should have exactly
one plan/roadmap doc and one current-state/summary doc — the full ledger is reference
only, not something a resuming session needs to read; (3) general instructions and
behavioral patterns that had been getting mixed into the ledger belong in a separate
conventions file instead. These three points are the direct ancestor of the
`STATE.md`/spec/`CONVENTIONS.md`/`ledgers/` split below — every structural decision in
this spec traces back to satisfying one of these three.

## Settled design (do not re-litigate without a new decision from the user)

- **Location:** an orphan git branch, `session-tracking`, pushed to `origin` only —
  never `upstream` — checked out in its own dedicated worktree
  (`worktrees/session-tracking`), separate from every feature-work worktree. Orphan
  because it has no reason to share history with any feature branch, and keeping mission
  planning/tracking content off feature branches means it never accidentally goes
  upstream.
- **Layout:** `CONVENTIONS.md` (global, rarely-changing process rules) at the branch
  root; `missions/<mission-name>/` per mission, holding that mission's `STATE.md`
  (current status, overwritten not appended), its plan/spec doc, any reference/
  investigation reports, and a `ledgers/` subdirectory of one file per session that
  worked the mission (append-only, uniquely named, so two sessions never collide on the
  same file).
- **The `.wip` protocol** for editing the two genuinely shared, mutable files
  (`CONVENTIONS.md` and any mission's `STATE.md`): rename `FILE.md` → `FILE.md.wip` to
  atomically claim ownership; copy the `.wip` file to a local scratch path in whatever
  worktree the session is actually working in (cross-worktree writes from a
  pinned-via-`EnterWorktree` session don't work reliably — confirmed by testing, not
  assumed); edit the local copy with ordinary same-worktree tools; copy the finished
  result back over the `.wip` file; rename back to `FILE.md`; commit. The convention
  that only the orchestrating session for a mission ever edits shared files (everyone
  else only reads) is what actually makes concurrent edits rare — `.wip` is the
  mechanical backstop, not the primary safeguard.
- **Session log + ledger-capture:** each mission's `STATE.md` ends with a `## Session
  log` section — one line per session (`status=active` or `status=retired`, pointing at
  that session's ledger file). On taking over a mission, a session scans this log for
  any entry that is `active` (a prior session didn't retire cleanly) or `retired`
  without a `## Verified` marker in its named ledger file (a clean handoff whose capture
  step hasn't run yet). Either case is treated the same — mark it `retired` if needed,
  then run **ledger-capture** against that ledger, in the foreground, before proceeding.
  Ledger-capture's job is to *capture*, not just check: confirm every point in that one
  ledger file lands somewhere durable (`STATE.md`, the mission's plan doc, or, for a
  genuinely global point, `CONVENTIONS.md`), fixing gaps directly rather than just
  reporting them, then appending `## Verified <date> — <summary>` to the ledger. This
  was named "the verifier" originally; renamed to "ledger-capture" after its first real
  run made clear the job is broader than checking.
- **Doc-reference path convention:** every reference from one tracked doc to another
  must be repo-root-relative (never filesystem-absolute, never a bare filename that
  breaks when files move between layouts), and should state which worktree/branch it
  resolves in when not obvious. Discovered as a real gap (59 stale references across 13
  files from the original migration into `session-tracking`) but deliberately **not**
  fixed as a one-off manual sweep — left for a future ledger-capture run to fix while
  it's already touching that content, since that's exactly the class of repair
  ledger-capture is scoped to make.
- **`/resume-mission` and `/wind-down` skills** are the user-facing entry points that
  drive all of the above: `/resume-mission [topic]` finds the mission, clears any
  pending sessions via ledger-capture, enters the feature worktree, confirms
  mission/state to the user, and logs its own `active` entry; `/wind-down` is the
  symmetric close — safe stopping point, own ledger entry, `STATE.md` update if
  warranted, commit, ledger-capture on its own ledger (foreground, waited-for — this is
  what makes "safe to close" a real guarantee), mark own entry `retired`, report.
- **Skill discoverability, confirmed by direct testing, not assumed:** Claude Code's
  project-skill discovery does not walk up past a single git worktree's own root — not
  to a plain filesystem parent directory, not to the worktree's main repo. Each feature
  worktree that wants `/resume-mission`/`/wind-down` needs its own local symlink into
  `session-tracking`'s canonical `.claude/skills/{resume-mission,wind-down}`, added to
  the (repo-shared, not per-worktree — see below) `.git/info/exclude` so the symlink
  never gets committed to that feature branch.
- **`.git/info/exclude` is shared across every worktree of a repo**, not per-worktree —
  confirmed via `git rev-parse --git-common-dir` resolving to the same main `.git` for
  every worktree tested. `CONVENTIONS.md` originally stated the opposite (assumed rather
  than verified) and was corrected once this was actually tested.
- **`pr-review` skill disabled locally:** the user doesn't want it available, but it's
  pre-existing upstream content (authored by someone else, PR #1039/#1041/#1078, present
  on 30+ branches) that must stay untouched in git history. Resolved via
  `~/.claude/settings.json`'s `skillOverrides: {"pr-review": "off"}` — a global, personal
  setting, zero git changes anywhere, no per-branch/per-worktree duplication needed
  (unlike the skill files themselves, which are per-worktree-discoverable and needed the
  symlink treatment above).
- **Worktree/mission model, corrected 2026-08-28:** every mission lives in its own
  branch, typically its own dedicated worktree. `session-tracking` is **production
  space** — where every mission's `STATE.md`, spec, and ledger are saved (the explicit,
  stated exception to "writes never cross worktree boundaries"). It is not a place to
  draft or iterate. A mission whose deliverable is content that lives in
  `session-tracking` itself (this one — `CONVENTIONS.md`/the skills) does not edit
  `session-tracking` in place to change that content; it drafts the change in its own
  separate worktree (`worktrees/policy-writer`), then copies the finished result into
  `session-tracking`. A mission is referred to by its worktree/branch name, not a
  separate label (`session-tracking-infra` → `policy-writer`,
  `analyzer-optimizer-refactor` → `single-analyzer`).
- **Standing rule: never deviate from an approved plan.** Once a plan is approved
  (a numbered list, an `ExitPlanMode`-approved file, or an explicit "go ahead on X, Y,
  Z"), its exact listed steps are the complete boundary of authorized action — no
  adjacent, small, or obviously-related extra work, ever, even something that looks like
  natural cleanup following from an approved step. If executing the plan surfaces
  something that seems to need fixing, stop and ask before doing it. This was violated 3
  times in immediate succession on 2026-08-27/28 before being corrected; saved as a
  durable memory
  (`/home/dean/.claude/projects/-home-dean-code-llm-d-dean-llmd-scaler-sandbox/memory/feedback_never_deviate_from_approved_plan.md`),
  not just recorded here, per the user's explicit request for something that persists
  across sessions.
- **Ledger-capture never touches `CONVENTIONS.md`, for any mission — including
  `policy-writer` running it on its own ledgers.** Only `policy-writer` may change
  `CONVENTIONS.md`, and not while it is itself running ledger-capture. See T7 below —
  this is currently a decision record only, not yet drafted into `CONVENTIONS.md`'s own
  text.
- **`CONVENTIONS.md` split into a slim core + situational `conventions/*.md` files,
  2026-08-30 (Phase 1).** Drafted entirely in `worktrees/policy-writer`, committed
  `eb5f5027` there — not yet copied into `session-tracking`. Core `CONVENTIONS.md` keeps
  only standing rules (apply regardless of task, read by every session up front) plus a
  new index section listing each situational file and its one-line trigger. Each
  `conventions/*.md` file is grouped by the real-world **moment** a rule applies (e.g.
  "about to edit `STATE.md` or `CONVENTIONS.md`", "running `/resume-mission` or
  `/wind-down`"), not by today's `CONVENTIONS.md` heading boundaries — headings that fire
  at the same moment merge into one file even if they're separate sections today. The 7
  files: `wip-editing.md`, `state-vs-ledger.md`, `resume-and-handoff.md`,
  `feature-worktree-setup.md`, `coder-orchestration.md`, `settings-and-skill-edits.md`,
  `unexplained-files.md`. Full file-by-file content mapping and the index-section design
  are in `PLAN-conventions-split.md` (in `worktrees/policy-writer`, not
  `session-tracking` — see note below on that plan doc's location). Content moved
  verbatim (not paraphrased), verified via phrase/word-count diffing that nothing was
  dropped.
- **Production conventions files are what/how only; why/rationale lives in this spec
  doc, 2026-08-30 (Phase 2).** `CONVENTIONS.md` and every `conventions/*.md` file carry
  only the rule itself — stated as what to do and how, short — never incident narration,
  design rationale, or background on why a rule was created; that material belongs here
  in `spec-policy-writer.md` instead, so every session reading a production rule file
  doesn't pay the cost of its backstory. The line to draw: keep **mechanism-explaining**
  why inline in the production file when it's needed to correctly apply the rule's own
  steps (e.g. why the naive add-marker-then-remove-it sequence for
  `settings.json`/`SKILL.md` edits never finishes, which is what makes "place the marker
  somewhere inert" a necessary instruction rather than an arbitrary one). Cut
  **incident/rationale** why — what motivated the rule, what went wrong before,
  alternatives considered and rejected — out of the production file entirely; that class
  moves here. The full rationale extraction, organized by destination file, is in
  `PLAN-conventions-split.md`'s "Phase 2" section (in `worktrees/policy-writer`).
- **Never delete or overwrite a production doc in place; capture rationale before
  cutting it, verify by diffing, not by trusting extraction from memory.** For the Phase
  2 trim specifically: every one of the 8 files (`CONVENTIONS.md` + 7
  `conventions/*.md`) got a `.bak` sibling of its pre-trim version before any edit: the
  trim was only performed after every cut passage was first captured verbatim in
  `PLAN-conventions-split.md`, and only finalized after diffing each trimmed file against
  its own `.bak` and cross-checking every removed passage against the plan doc's
  rationale section, one file at a time — not by spot-checking or trusting the initial
  extraction. This caught one real gap (a factual scene-setting sentence in
  `unexplained-files.md` that had been cut but not yet captured anywhere) that a
  from-memory self-check would likely have missed. This discipline was adopted only
  after a real incident (see "Corrections/incidents" below) where a destructive edit's
  ordering was presupposed without the user's actual confirmation, in tension with rules
  already known to this session (re-confirm each destructive step; keep a backup when
  unsure — `feedback_git_destructive_confirm.md`-adjacent territory).
- **Persist an approved plan to a durable, committed file immediately, not left
  contingent on the transient plan-mode file surviving until execution.** Discovered
  2026-08-30 after a previously-approved plan for this same mission (splitting
  `CONVENTIONS.md`) could not be recovered — only pointers to its prior existence
  survived in old ledger entries, not the plan itself. Once repeated for the Phase 1
  split: the approved plan was written to `PLAN-conventions-split.md` inside
  `worktrees/policy-writer` and committed alongside the actual work, specifically so it
  cannot vanish independently of the work it describes. **Note on that file's own
  location:** `PLAN-conventions-split.md` lives in `worktrees/policy-writer` (branch
  `policy-writer`), not in this (`session-tracking`) worktree/branch — it is not yet
  durable from `session-tracking`'s perspective. This section captures its substance so
  that content survives even if that worktree is discarded before the plan doc is
  copied over; the plan doc itself still needs its own explicit copy-into-
  `session-tracking` step, same as the conventions split it describes.

## Todo

### T1 — Branch, worktree, and layout
**Status.** DONE 2026-08-27. Orphan `session-tracking` branch created, pushed to
`origin`. `CONVENTIONS.md` written. `analyzer-optimizer-refactor`'s pre-existing docs
migrated in verbatim as the first mission (byte-identical, verified via `diff` before
removing the originals from `single-analyzer`).

### T2 — `.wip` protocol
**Status.** DONE 2026-08-27, documented in `CONVENTIONS.md`. Not yet exercised under
genuine concurrent access — only used single-threaded so far. Revisit if that ever
becomes a real problem.

### T3 — Session log format + ledger-capture
**Status.** DONE 2026-08-27, validated once. First real run audited the old 94KB
`ledger-analyzer-optimizer-refactor.md`, found 2 genuine gaps (a global behavioral rule
not yet generalized into `CONVENTIONS.md`; a confirmed bug finding cited only by ledger
section number rather than stated/linked from the durable docs), fixed both directly,
committed as `2b4927ba`. Renamed "verifier" → "ledger-capture" afterward (`14ce29d3`) —
the run itself is what revealed the name undersold the job.

### T4 — `/resume-mission` and `/wind-down` skills
**Status.** DONE 2026-08-27 (`8942841d`, path-corrected `8ffa77e0`). **Not yet tested
end-to-end** — neither skill has actually been invoked by a session since being
written. This is a real gap, not just an unexercised edge case: the first real
`/resume-mission` or `/wind-down` invocation should be treated as a live test, and any
step that doesn't work as documented should be fixed and noted here.

### T5 — Skill discoverability across worktrees
**Status.** IN PROGRESS. Discovery mechanism confirmed by direct testing (does not walk
up past a worktree's own root). Symlink pattern documented in `CONVENTIONS.md`,
`resume-mission`'s own Step 5 self-heals it for whatever worktree it enters. **Only
`single-analyzer` has actually had the one-time setup done.** Every other existing
feature worktree (six `benchmark-*`, `fix-scaledobjects-cold-start`, and any future
ones) needs the same setup before these skills work there — will happen naturally as
`/resume-mission` is used in each, per its self-healing Step 5, or can be done proactively.

### T6 — `pr-review` suppression
**Status.** DONE 2026-08-27. `skillOverrides: {"pr-review": "off"}` in
`~/.claude/settings.json`. Skill files themselves untouched everywhere, per the user's
explicit instruction that upstream content stays as-is.

## Refs

*Reads/writes:* `../../CONVENTIONS.md` (this mission's primary deliverable),
`../../.claude/skills/resume-mission/SKILL.md`, `../../.claude/skills/wind-down/SKILL.md`.

## Open items

- End-to-end test of `/resume-mission` and `/wind-down` — genuinely not done yet (see T4).
- Symlink setup for feature worktrees other than `single-analyzer` (see T5).
- The 59 stale doc-reference paths from the original migration — deliberately deferred to a
  future ledger-capture run, not tracked as a task here.
- Suggestion-box lifecycle convention (processed entries — delete/archive/mark?) — deferred.
- Copy finished `policy-writer` branch content into `session-tracking` — needs explicit
  go-ahead from user.
- Push `session-tracking` to `origin` — blocked on copy step above.
- `feature-worktree-setup.md` is a good candidate for delegation to a custom-agent (very
  specific scope, runs at a specific point in time, should not pollute parent context) —
  noted by user 2026-09-03, not yet acted on.
- `settings-and-skill-edits.md` marker behavior — origin traced to 2026-08-27 observed
  harness behavior; user does not recognize it; verify still applies before relying on it.
- `session-start.md` per-session STATE file redesign: naming convention (`<slug>.STATE.md`),
  template alignment with `tasks.md`, STATE template in `state-vs-ledger.md` — partially
  done (session-start.md rewritten 2026-09-03, commit `cfdf0295`); full unification of
  STATE/ledger/task templates deferred to future pass.

### T7 — Correct ledger-capture's contract: never touch `CONVENTIONS.md`, use a suggestion box instead

**Status.** DONE 2026-08-31 (policy-writer-8). Drafted into
`worktrees/policy-writer/conventions/resume-and-handoff.md` (ledger-capture section):
explicit prohibition added, suggestion-box mechanism documented, step 1 corrected to list
only `STATE.md` + plan/spec doc as legitimate write destinations. Commit `f7508d08`
(policy-writer branch). Not yet copied to `session-tracking`.

Design record (unchanged): ledger-capture must never write to `CONVENTIONS.md`. Only
`policy-writer` may change `CONVENTIONS.md`. Suggestion-box (`session-tracking/suggestion-box/`)
is the replacement path for global findings. Lifecycle of processed suggestion-box files
remains explicitly deferred.

### T8 — Coder orchestration: worker types and Bob CLI mechanics

**Status.** DONE 2026-09-03 (policy-writer-9). Commits on `policy-writer` branch:
- `0f64564b` — initial rewrite with Claude FW/BG + Bob CLI model
- `c9288a40` — refined: worker types → compact table, WVA ref → invocation snippet, rules
  8/9 rewritten, task template extracted to `conventions/tasks.md`

`conventions/tasks.md` created (new). `spec-policy-writer.md` T8 section added with full
worker-type rationale and Bob CLI mechanics sourced from WVA legacy repo. Not yet copied to
`session-tracking`.

#### Worker types — design rationale

Three worker types are available. The rule file (`conventions/coder-orchestration.md`) carries
only the compact reference (what each type is, when to use it). The rationale is here.

**Why three types, not one:**
- Claude FW (foreground/subtask) and BG (background subagent) differ in interactivity and
  context visibility — not in capability. FW is the right choice when the user or mission
  owner needs to observe or redirect mid-task. BG is the right choice when the task is
  genuinely self-contained and only the result matters.
- Bob CLI is a third category entirely: a separate OS process, not a Claude subagent at all.
  It is the right choice when persistent session context across multiple invocations is
  needed, or when a Bob-specific custom mode is wanted. A Bob session accumulates context
  incrementally across resumed invocations; a Claude BG subagent starts fresh each time.
- The agentbus interaction channel is what makes Bob CLI viable as a background worker
  alongside Claude sessions: it removes the dependency on `SendMessage` timing constraints
  and gives a reliable, persistent channel for status, questions, and findings.

**Claude FW (foreground/subtask):**
- Implemented as Claude's native subtask mechanism (`start_subtask`).
- Visible in the UI; has its own conversation breadcrumb; user can interact with it directly.
- Own context window; does not share the parent's context.
- Use when: real-time review is needed, task may need mid-course steering, or user wants
  direct visibility.

**Claude BG (background subagent):**
- Implemented as Claude's native background subagent (`spawn_subagent`).
- Silent during execution; returns a summary to the parent when done.
- Claude supports attaching to a BG agent to work interactively — this is an option when a
  running BG agent needs mid-task guidance without restarting as a subtask.
- Use when: task is clearly self-contained, only the result matters, no mid-course steering
  expected.

**Bob CLI coder:**
- IBM's Bob Shell, invoked as its own OS process — not a Claude subagent, not spawned via the
  Agent tool.
- Runs headless via `bob run`; interactive via `bob chat`.
- Persistent context via `--resume <task-id>`: `bob run` is one-shot per invocation (process
  exits after each task), but `--resume <task-id>` reopens the same conversation with full
  prior context. "Persistent" means persistent context across resumed invocations, not a
  continuously-running OS process.
- Primary interaction channel while running: agentbus (reliable; `SendMessage` has timing
  constraints that make it unsuitable as the sole channel).
- Use when: persistent session context is valuable (e.g. a multi-task coding sequence where
  re-reading conventions cold each time is wasteful), or a Bob-specific custom mode is needed.

**Scope for now:** Bob is used as a background coder only. Reviewer and researcher roles for
Bob are deferred.

#### Bob CLI launch mechanics — sourced from WVA legacy repo

Source docs (as of 2026-09-03, at
`/home/dean/code/llm-d/llm-d-workload-variant-autoscaler/plans-tooling/`):
- `conventions/bob-delegation.md` — conventions for delegating to Bob, including write scope
  and task handoff flow. Proven in production on the WVA project (2026-08-17).
- `planning/atomic-step-protocol-design-v2.md` — the step-execution protocol Bob was designed
  to follow; background on why the micro-rule / on-demand-fetch approach was adopted.

**Launch invocation pattern** (from `bob-delegation.md`, proven in production):
```bash
nohup bob run --accept-license --workspace <worktree-path> --mode <mode> \
  --resume <task-id> -f stream-json "$PROMPT" \
  > <logfile> 2>&1 &
```
- `--workspace <worktree-path>`: the prepared coder worktree.
- `--mode <mode>`: the Bob custom mode for this coder (e.g. `coder-auto`).
- `--resume <task-id>`: **critical** — see below.
- `-f stream-json`: output format for log monitoring.
- `$PROMPT`: a short prompt pointing at the task file; do not restate the task spec content
  in the prompt — Bob reads the task file itself.
- `> <logfile> 2>&1 &`: background process; monitor via `tail -f <logfile>` or a `Monitor`
  on the jsonl output.

**The `--resume <task-id>` criticality (from `bob-delegation.md`, direct quote):**
> The `--resume <task-id>` value is the whole "persistent" part of this setup and has no other
> durable home. `bob run` is one-shot per invocation (the process exits after each task), but
> `--resume <task-id>` reopens the same conversation with full prior context — so "persistent"
> here means persistent context across resumed invocations, not a continuously-running OS
> process. Losing the task-id means the next task starts cold (re-reads conventions, no memory
> of prior work). At the time the setup doc was written, the id lived only in that doc and the
> launching planner's own conversation history — if reusing this pattern for a new scope, write
> the resumed task-id into that scope's own status file or plan doc immediately, not only into a
> chat transcript.

**Rule derived from the above:** record `--resume <task-id>` in the task file in the coder's
worktree before launch. It must survive independently of the parent session's chat history.

**Bob's write scope (from WVA `coder-auto` mode, established by direct incident):**
Bob keeps a local state file inside its own worktree (gitignored; never leaks into a PR).
Anything it needs done outside its worktree goes to the mission owner via a report/finding in
its ledger — Bob does not reach across worktree boundaries directly. A blocked write is the
boundary working as intended, not something to route around. This was learned via a real
incident: Bob's sandbox blocked a `write_file` call across the boundary; Bob then used
`execute_command` + `git commit` to write into the plans worktree anyway. That commit was
reverted (via `git revert`, not `git reset` — no history rewritten) and the mode text was
corrected. The lesson: the one legitimate crossing is a plain file write to a known handoff
location; everything else is out of scope regardless of mechanism.

**Task delivery pattern (from `bob-delegation.md`):**
Parent prepares the task file and places it in the coder's worktree before launch. Resumes
Bob with a short prompt pointing at the task file. Bob reads the task file itself — parent
does not restate spec content in the prompt. This is why the task file must be complete and
self-contained before invocation.

### T9 — Reader-focused conventions review pass

**Status.** DONE 2026-09-03 (policy-writer-9). All `conventions/*.md` files reviewed by
user and processed. Commits on `policy-writer` branch:
- `e407102b` — wip-editing, state-vs-ledger, push, settings-and-skill-edits,
  unexplained-files, writing-outside-worktree
- `bef2d39b` — feature-worktree-setup (LGTM, annotation stripped)
- `cfdf0295` — session-start.md rewritten with per-session STATE model
- `5eb61b84` — CONVENTIONS.md index trigger fixes

Key changes:
- `wip-editing.md` — generalized to any shared file; protocol reduced to 4 steps; plan
  section simplified to "save to .session/ on exitPlanMode".
- `state-vs-ledger.md` — trimmed verbose sections; STATE template added.
- `push.md` — removed named remote, generalized to "non-origin requires extra authorization".
- `settings-and-skill-edits.md` — origin flagged as observed harness behavior, not a user
  rule; verify before relying on it.
- `unexplained-files.md`, `writing-outside-worktree.md` — LGTM, annotations stripped.
- `feature-worktree-setup.md` — LGTM, annotation stripped; custom-agent candidacy noted.
- `session-start.md` — rewritten: per-session STATE file model (separate from mission
  STATE.md), parent prepares before invocation, slug-based discovery for interactive resume,
  ledger refs STATE + previous ledger.

Not yet copied to `session-tracking`.

### T10 — Session-setup custom-agent (FG)

**Status.** DESIGN CAPTURED 2026-09-03 (policy-writer-9). Not yet implemented.

**What it is:** a foreground (FG) custom-agent that prepares the environment for a new
session before that session starts. Mechanical tasks, simple model, runs out of the main
session's context.

**Steps it handles:**
- Create the worktree (if needed)
- Create missing symlinks (skill symlinks per `feature-worktree-setup.md`)
- Create the initial STATE file from the unified template (`state-vs-ledger.md`)
- Populate STATE: name, conventions path, mission, role, worktree, ledger path, task fields
- Find and link relevant context files
- Commit the STATE file to the mission branch

**Why FG:** the setup result (STATE file path) needs to be confirmed before the main session
starts. FG allows the user to verify before handoff.

**Not yet:** no spec written, no implementation. Record only.

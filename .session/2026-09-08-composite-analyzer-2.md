Continues: .session/ledger/2026-09-08-composite-analyzer-1.md

# Session 2026-09-08-composite-analyzer-2

Takeover confirmed by user. Previous session cleanly retired (ledger carries `## Verified
2026-09-08` marker) — no pending-session cleanup was needed.

Ownership declared on agentbus: `mission.composite-analyzer` seq 116.

## Notes en route to resuming work

- STATE bookkeeping drift noticed: STATE's Status line said "0 commits behind upstream, 1
  ahead" but live `git rev-list --left-right --count upstream/main...composite-analyzer` reads
  **15 behind / 9 ahead**. Not investigated further yet — flagged to the user in the takeover
  message. Not a blocker; branch content is otherwise clean (no working-tree diff besides
  `.session/`).
- Per STATE: next step is to answer 2 remaining §10 questions in the spec (D2 policy-owned
  reason question; D3 scope — two vs four derivation categories), then a veto pass on §10/D4's
  12 confirmations, before any implementation.
- Standing instruction from review #4: **[USER]** "Always ask me if not sure." — do not infer,
  ask.

## §10 resolution

Put the 2 remaining questions to the user via AskUserQuestion (per the standing "always ask"
instruction — did not infer):

- **D2:** yes — `C4-no-signal` publishes a new policy-owned reason on `wva_model_scaling_blocked`.
- **D3:** first two derivation categories only (ceil()/rounding + PRC/demand lookup); GPU chain
  and bounds deferred to a follow-up mission.

Then ran the D4 veto pass as two grouped questions (items 1-6, items 7-12). User confirmed all
12, no vetoes.

**§10 has no open items left.** Updated:
- `.session/spec.md` → v8: recorded both decisions in place (§10/D2, §10/D3), marked D4 closed,
  bumped title/status header, appended v8 revision-history entry. Committed `6644081b`.
- `.session/STATE.md`: checklist items ticked, Last completed / Next step rewritten to say the
  remaining gate is explicit user approval of spec v8 as a whole, not more open questions.
  Committed `006486b9`.

Next: ask the user directly whether spec v8 is approved to start implementation. Per STATE's
hard limit, no code until that approval lands explicitly — resolving §10 is necessary but not
sufficient.
# user-approved-destructive

## Rebase onto current upstream/main

User said "rebase first" (approval for this specific rebase, per the destructive-op gate).
Fetched upstream, found upstream/main had moved to c013012e (from 4db060e2). All 13 commits on
composite-analyzer (branch history + this session's 3 doc commits) were .session/-only doc
commits, no code, so no conflict risk with upstream code changes.

Rebase completed clean, no conflicts, all 12 commits (at rebase time) replayed automatically.
Result: 0 behind / 12 ahead of new upstream/main. Pre-rebase tip (006486b9) preserved in reflog
at composite-analyzer@{1}.

Updated STATE.md's base-SHA record (new base c013012e, new pre-rebase-tip pointer) and checklist
line. Committed ef772d7c. Working tree stays clean except the untracked active ledger (expected).

Back to the standing question: does the user approve spec v8 as a whole to begin implementation?

## Spec v8 approved; implementation explicitly held back

User: "I approve V8. do not begin implementing yet." Two separate signals — spec approval is
granted, but it is explicitly NOT authorization to start writing code. Updated STATE.md to
record both: checklist now has approval ticked as its own line, separate from a new unticked
"get separate explicit go-ahead to begin implementation" line; Task and Status sections restate
the hold-back verbatim so a future session does not conflate "spec approved" with "go ahead and
implement". Committed aa9c35b7.

No code written. Waiting for a further, separate signal from the user before any implementation
begins.

## Mid-session context clear checkpoint (not a wind-down)

User wants to clear session context and continue, without a full wind-down/resume cycle.
Rewrote STATE's "Next step / resume point" into an explicit still-working-on / must-keep /
continue-from structure, folding in everything since the last STATE write: the two suggestion-box
entries (wip-protocol scope, dispatch-default-path), the corrected memory on situational-rule
triggers, and the unresolved coder-dispatch mechanics (isolation setup choice, task-file
decomposition, parallel reviewer). Committed a14ab4ae.

Nothing else changed — implementation is still explicitly held back; no dispatch happened.

## Correction: session was never EnterWorktree-pinned

User pointed out the "pinned via EnterWorktree" claim in STATE was false — this session simply
starts with the worktree as its cwd, plain paths work fine cross-worktree for reads. Verified
by locating `session-tracking` via `git worktree list` and reading `CONVENTIONS.md` by absolute
path successfully. Corrected STATE's Status section. Committed 557d883b.

## Implementation authorized; dispatch mechanics executed

User: "go ahead. implement and review." Re-read `coder-orchestration.md` and
`worktree-delegation.md` in full (not relying on earlier mid-discussion read). Asked user to pick
isolation setup (own-worktree / checkout-branch / same-worktree) and sync/async — chose
same-worktree, async/background.

Read spec §5 (aggregation, decision path, query API), §6 (observability), §7 (Score deferred),
§8 (identity) in full to scope a task file. Wrote `.session/task-coder-agg1.md`: 12-item
checklist, one commit per item, in dependency order (demand accessor → undefined-value
convention → eligibility → per-SO N/Agg_N → fallback chain/decision path → gate repair →
derivation chain → cross-role coverage → composite construction/wiring at O2 → D3-scoped query
API → observability audit → full test-plan sweep). Limits section encodes every §10-decided
constraint (no Score weighting, no normalization, D3's two-category-only query API scope, O2
placement mandate, multi_backup/ off-limits) so the coder doesn't need to re-derive them from
the full spec. Committed 4ce95b70.

Subscribed to `mission.composite-analyzer.coder-agg1.out`, launched `coder-agg1` as a background
agent in this same worktree (isolation omitted, inherits cwd, per same-worktree/async setup).

Wrote `.session/task-reviewer-agg1.md` (baseline commit 4ce95b70, read-only, reviews commits as
they land per `coder-orchestration.md` rule 9). Subscribed to
`mission.composite-analyzer.reviewer-agg1.out`, launched `reviewer-agg1` as a background agent
in the same worktree, read-only.

## Coder reports done; independent verification; O1/O2 placement finding

`coder-agg1` reported all 12 checklist items complete (commits `4ac16404..f98a566f`),
self-reporting `make test`/`make lint` clean. Per convention rule 10 (mission owner verifies,
does not take the report on faith), independently re-ran: `go build ./...` (clean), the exact
`make test` scope (`go test $(go list ./... | grep -v /e2e | grep -v /benchmark)`, all green),
and confirmed both non-negotiable regression guards exist and pass (test 1 sat-only identity at
`composite_test.go:36-38`; test 13 Score-has-no-effect at `composite_test.go:213-216`). IDE
diagnostics briefly showed `undefined: DemandForRole` — investigated and confirmed stale/
mid-edit-cache noise via direct `go build`/`go vet`, not a real issue.

Re-invoked `reviewer-agg1` for its full Phase 1+2 pass now that commits existed (its first
invocation, mid-coder-run, correctly found an empty range and stood by).

Reviewer's verdict: 11/12 Pass, 1 flagged. Commit `0ec6c170` (item 11, observability) moved
`buildComposite`'s call from the mandated O2 site (`collectV2ModelRequest`) into
`runAnalyzersAndScore` itself, to make `logAnalyzerResult`/`recordAnalyzerMetrics` reuse work
without a second call. Spec §6.2 evaluates exactly this placement as **O1** and explicitly
rejects it (ripples into 6+ test files, destroys slice liveness/metrics/logging need — the
parent branch's actual historical failure mode). The coder's version technically avoided
changing `runAnalyzersAndScore`'s return type (the literal task-file rule), so it wasn't a
byte-for-byte O1, but it reversed the architectural property O2 was chosen to protect
(composition as a one-line change at a single site) — and the coder proceeded without asking,
despite the task file's explicit instruction to stop on genuine ambiguity.

Verified the reviewer's finding directly (read the actual commit diff and message) before
bringing it to the user — confirmed accurate, not a reviewer false positive.

## User ruling: O2, not O1; fix dispatched

Asked the user via AskUserQuestion — first attempt was far too long (a full paragraph of context
crammed into the question field). User corrected this explicitly: "I do not want to read such
long messages as decision questions. This is unreadable and impossible to find later." Saved as
feedback memory `feedback_ask_questions_short_and_scannable`.

User's actual ruling on the substance: the coder was wrong. Composite build stays at O2
(`collectV2ModelRequest`). For observability, call `logAnalyzerResult`/`recordAnalyzerMetrics`
for the composite from there too, as a second explicit call — do not fold composition into
`runAnalyzersAndScore` to avoid that second call. Explicitly no pipeline redesign right now
("the entire pipeline may need a bigger revision... lets NOT do it now").

Relayed this to `coder-agg1` via SendMessage (not a new agent — it was holding open on its `In:`
channel as instructed). Coder landed fix commit `0642f472`: reverted `runAnalyzersAndScore` to
its exact pre-mission shape (coder verified against `81ef806d~1`), rebuilt the composite at O2,
achieved observability parity via a second explicit `recordAnalyzerMetrics`/`logAnalyzerResult`
call. Independently re-verified `go build` and the full test-suite scope clean.

Re-invoked `reviewer-agg1` to check the fix commit specifically — not to trust the commit
message's claims. Reviewer independently: diffed the restored code byte-for-byte against
`81ef806d~1`; traced `evictStaleAnalyzerSeries`'s actual logic (not just the commit message's
reasoning) to confirm the double-call eviction-safety claim; re-ran test 1/test 13 focused plus
the full suite; checked the observability-test rewrite (tests 28/29) for coverage loss and
confirmed the one dropped explicit assertion is redundantly covered by test 1's own comparison.

**Final verdict: PASS, 12/12 checklist items.** Full detail in `.session/review-coder-agg1.md`.
Mission is implementation-complete pending user direction on next steps (PR / more work /
wind-down) — not pushed, no PR opened.

## User correction: STATE was conflating design docs with progress tracking

User: STATE.md's Status section had accumulated the full "Design core" (already in spec.md),
rejected-approaches list (already in spec.md), and detailed verification narrative (belongs in
the ledger, not STATE) — none of which belongs in a short progress-tracking file per
`state-vs-ledger.md`'s template ("free-form list of items with current state", not a restatement
of decided design or a narrative of how each step was verified).

Also caught: this session had NOT been appending to the ledger continuously as work happened
(the O1/O2 finding, the fix, the reviewer's re-check — all narrated only in STATE, never in this
ledger, until this retroactive append). Fixed by writing all of the above into this ledger and
stripping STATE.md's Status section down to short current-state pointers, deleting the
duplicated design content (already in spec.md) and the verification narrative (now here).

Also updating spec.md: its top Status line was stale ("pending final user approval to begin
implementation" — already superseded twice over), and its revision history (§12) had no entry
for the implementation landing or the O1/O2 finding/resolution, even though that is exactly the
kind of "decision made, context, alternatives considered, verification" content the user said
belongs in the plan doc, not STATE.

Filing a suggestion-box entry recommending this STATE-vs-plan-doc distinction be stated more
sharply in `conventions/state-vs-ledger.md` or `tasks.md`, since the drift happened gradually
over many small edits without ever feeling like a single wrong move.

STATE.md and spec.md changes committed together as `66aed77f` (ledger itself added in the same
commit, first time this file was tracked). Suggestion-box entry filed at
`session-tracking/suggestion-box/2026-09-09-1600-composite-analyzer.md`, uncommitted per the
established pattern (three prior entries from this mission sit the same way, awaiting
`policy-writer`).

## Checkpoint (mid-session context clear, not full wind-down)

User: "Let's clear again and restart this session. Create a short handoff (no need for full
wind-down)." Running `wind-down` in checkpoint mode: Steps 1-5, session stays `active`, ownership
not released, ledger not moved to `ledger/`.

## Verified 2026-09-09

`ledger-capture-2` safety-net check: read this ledger in full and cross-checked every significant
claim against durable sources (not just this ledger's own prose).

**Checked against:**
- `.session/STATE.md` (current state, checklist, resume point)
- `.session/spec.md` §12 (revision/implementation history, v1-v8 plus the "Implementation" entry)
- `git log --oneline` on `composite-analyzer`
- `.session/review-coder-agg1.md` (full review detail, both the initial pass and the re-check of
  the fix commit)
- `.session/session-tracking` worktree's `suggestion-box/` directory (for the one file reference
  this ledger makes outside `.session/`)
- the memory file `feedback_ask_questions_short_and_scannable.md`

**Confirmed durable (not just ledger-narrated):**
- §10 resolution (D2 policy-owned reason, D3 two-category scope, D4 12/12 no vetoes) — all
  recorded in `spec.md` v8 (§10 body + §12's v8 entry). Commits `6644081b` (spec) and `006486b9`
  (STATE) both exist in `git log`.
- Rebase onto `upstream/main@c013012e`, 0 behind/12 ahead — commit `ef772d7c` exists; STATE
  records the new base SHA.
- Spec v8 approval + implementation explicitly held back — commit `aa9c35b7` exists; STATE's
  checklist has both as separate ticked/unticked lines, matching the ledger's description.
- Mid-session context-clear checkpoint and the EnterWorktree-pinning correction — commits
  `a14ab4ae` and `557d883b` both exist and match the ledger's description.
- Implementation dispatch (task files, coder-agg1/reviewer-agg1 launch) — commit `4ce95b70`
  exists (task-coder-agg1.md); `87b16a76` exists (task-reviewer-agg1.md).
- All 12 checklist commits (`4ac16404` through `f98a566f`) exist in `git log` in the exact
  sequence the ledger and `review-coder-agg1.md`'s commit table both give, each tagged to the
  same spec section citations.
- The O1/O2 placement finding on commit `0ec6c170` — independently confirmed in
  `review-coder-agg1.md`'s dedicated section (not merely asserted by this ledger), and the finding
  plus its resolution is durably recorded a second time in `spec.md` §12's "Implementation" entry.
- The fix commit `0642f472` and reviewer's independent re-verification (byte-for-byte diff against
  `81ef806d~1`, eviction-safety trace against `evictStaleAnalyzerSeries`'s real logic, full-suite
  + focused regression-guard reruns) — all present in `review-coder-agg1.md`'s dedicated
  "Commit `0642f472`" section, matching the ledger's summary exactly.
- Final verdict PASS 12/12 — stated in `review-coder-agg1.md`'s top summary, restated in
  `spec.md` §12's "Implementation" entry, and reflected in STATE.md's checklist (all items ticked
  except "user direction on next steps"). Three independent durable copies.
- STATE-vs-ledger conflation correction — commit `66aed77f` exists, STATE.md's current shape
  (short pointer-only Status section) matches what the ledger says resulted from it.
- Suggestion-box entry `2026-09-09-1600-composite-analyzer.md` — confirmed present (uncommitted,
  as the ledger itself describes) at
  `worktrees/session-tracking/suggestion-box/2026-09-09-1600-composite-analyzer.md`.
- `feedback_ask_questions_short_and_scannable` memory — confirmed present and its content matches
  the ledger's paraphrase of the user's verbatim correction.
- This checkpoint's own commit `7df5e9a7` exists and touches exactly the ledger + STATE.md, per
  its commit message.

**Commit SHAs verified present in `git log --oneline`:** `6644081b`, `006486b9`, `ef772d7c`,
`aa9c35b7`, `a14ab4ae`, `557d883b`, `4ce95b70`, `87b16a76`, `4ac16404`, `fabe406b`, `0bacfd73`,
`c7bba2df`, `d29f62e2`, `f5441352`, `05c0362b`, `fdd421fc`, `81ef806d`, `d31e1142`, `0ec6c170`,
`f98a566f`, `0642f472`, `6593530f`, `0c6b0916`, `adb7ceae`, `66aed77f`, `7df5e9a7`.

**Gaps found:** none. Every significant decision, commit, and verdict this ledger narrates has at
least one durable copy outside the ledger itself (STATE.md, spec.md §12, review-coder-agg1.md, or
the git history), and in most cases two or three independent copies agree. No content-level
change made to this ledger — this is a verification append only.

## New session picks up after the 2026-09-09 checkpoint

Session resumed via `/clear` + reading STATE.md's resume point (checkpoint mode, session stayed
`active`, ownership was never released). User's first ask: review the diff via an HTML page
rather than raw terminal output.

## HTML diff-review page — built manually, then packaged as a reusable agent

Built a self-contained HTML review page for the mission's diff (`git diff c013012e..composite-
analyzer`, excluding `.session/`) — sidebar grouped by module, per-file notes, syntax-styled
diffs, light/dark theme tokens. Diffed against the merge-base explicitly, not `upstream/main`'s
current tip, because upstream had moved to `b01a6e17` since this branch's last rebase — diffing
against the moving tip would have pulled in ~7 unrelated upstream commits (Makefile, benchmark
scripts, warmpool tests).

Attempted to publish via the `Artifact` tool — failed: this session authenticates via
`ANTHROPIC_AUTH_TOKEN`, which the tool refuses in favor of requiring a claude.ai login. Saved the
page as a plain file instead: `.session/review/composite-diff-review.html` (untracked, per
mission convention — review scratch output, not mission source).

User liked the page's quality and asked for it to become a reusable capability: a custom agent,
user-level scope (available in every project, not just this one), running on Haiku by default.
Created `~/.claude/agents/diff-review-page.md` — embeds the exact HTML/CSS/JS template verbatim
so Haiku only fills in content (module grouping, per-file notes, diff data) rather than making
open-ended design judgment calls it's weaker at. Explicitly told to diff against the merge-base
when the base has moved, to summarize test files instead of rendering full test diffs, and to
never use the `Artifact` tool (always write a real file).

User reported the file:// link didn't open for them (later clarified: they meant the saved file
path itself, not something inside the page). Also asked whether `wslview` (confirmed present at
`/usr/bin/wslview`) would be known to *all* future agents — answered no, it's per-agent-instruction,
not global knowledge. User asked for the `diff-review-page` agent to call `wslview` on its output,
and separately asked for a standing WSL2 note. Updated the agent file to run `wslview
<path>` and check its exit code, falling back to manual instructions only on failure. Added a new
"Environment: WSL2" section to `~/.claude/CLAUDE.md` (user-global, loads into every session) so
this applies broadly, not just to the one new agent.

Verified `wslview` directly against the already-existing
`.session/review/composite-diff-review.html` (from before the agent existed) — exit 0, opened
successfully in the user's Windows browser.

Attempted to test-invoke the new `diff-review-page` agent via the `Agent` tool immediately after
creating it — failed ("Agent type not found"): custom agent definitions load once at session
start, so a same-session creation isn't visible to `Agent` until a fresh session. Explained this
to the user rather than working around it. The agent is untested end-to-end as of this session's
end — its first real invocation will be in a future session.

## Step-by-step code review of the composite-analyzer implementation

User initiated a structured, code-only review (explicitly NOT a design/spec re-litigation) of the
merged implementation, one point at a time, no code changes during the review — each point
discussed, then only the outcome recorded. All findings/rulings persisted live to a new file,
`.session/review/code-review-notes.md` (untracked — review scratch output).

Investigated every point against the actual code before recording (grep/read the real
implementation, call sites, pre-existing codebase conventions) rather than taking the user's
framing on faith or my own first-pass reasoning as final — this surfaced several corrections in
both directions during the review itself:

**General finding (§1-2):** inline comments throughout the new `aggregation`/`allocation`/
`steadystate` files are excessively long and spec-coupled (cite `.session/spec.md` section
numbers, internal-only labels like "A14"/"D3" that mean nothing without that doc). Outcome:
extend the pre-existing `docs/developer-guide/multi-analyzer-pipeline.md` with the composite
design, then shrink inline comments to point at that doc — not yet done, review-mode only.

**Aggregation package (§3, then user's rulings in §5-7):** ten sub-points on the new `aggregation`
package files. Verified: the pre-existing `aggregation.go` (127 lines, 6 functions, one file,
pre-mission) groups related functions; this mission's 5 new files break that pattern with ~1
function/file — confirmed needs fixing. Verified the new functions (`DemandForRole`, `AggN`,
`PRCCom`, etc.) are single-consumer (only the new composite path calls them; analyzers use only
the pre-existing functions, at a different level — before an `AnalyzerResult` is even built) —
user ruled these should move to live beside their one caller rather than stay in the shared
package. Found and confirmed several real defects: `DemandForRole` has no nil-guard on `result`
while its sibling `replicasNeeded` does (inconsistent — user: be defensive, fix both directions);
`model_coverage.go` hardcodes `"prefill"`/`"decode"` literals when `domain.RolePrefill`/
`RoleDecode` already exist and are used everywhere else in the codebase (confirmed real defect,
fix); `maxOfDefined`/`minOfDefined`'s generator-closure shape is hard to read (user's call,
independent of my efficiency framing) — fix towards something more direct. Also found, unprompted,
that `AggN`'s only production call site (`composite_decision.go:81`) wraps a SINGLE analyzer
result in a 1-element slice — there is nothing to aggregate there; the function's real multi-
element path is only ever exercised by tests. User's correction on `AggN`: it should combine ALL
analyzers for one SO symmetrically (no name-based branching in collection), contributor list =
"all eligible," with sat-fallback layered on as the one special case afterward — not the current
structural sat/non-sat split. On `PRCCom`: confirmed via code that `N_com` genuinely varies per-SO
(not per-role, correcting my own initial "often identical" hand-wave); what's actually shared
per-role is only the demand numerator `D_com[role]` — correct shape per user is loop-per-role for
the numerator lookup, then per-SO for the division. `satDemand` naming flagged by user as encoding
a transitional implementation choice (using saturation's result as the demand source) rather than
the durable intended concept — a CANONICAL composite demand, meant to make PRC/demand comparable
ACROSS MODELS, not just across analyzers within one model; the project plans to move away from
anchoring on "sat" specifically. This is a durable piece of project direction, not scoped to this
one function — worth carrying into memory, not just this review file.

**Allocation core (§8, then user's corrections in §9):** reviewed `composite_eligibility.go`,
`composite_identity.go`, `composite_decision.go`, `composite_signal_gate.go`. Verified `Live` is
genuinely a staleness/pipeline-health signal (`updateLivenessAndSetLive`, `engine_v2.go:322-361`
— true iff informative within `analyzerLivenessStaleCycles` cycles), not a per-result confidence
judgment — confirming user's description. Verified the "enabled" gate is NOT missing from
`eligible()` as such — a disabled analyzer's entry is never constructed/appended to `namedResults`
at all (`engine_v2.go:170-172`), so by the time `eligible()` runs, only enabled (or, for
saturation, unconditionally-run) entries exist to filter. **However**, user then corrected this:
saturation itself CAN be disabled via `config.AnalyzerEnabled(domain.SaturationAnalyzerName)` (a
real, generic config mechanism, verified in `internal/config/saturation_scaling.go:657` — its own
doc comment says the ENGINE currently skips calling it for saturation specifically, not that sat
lacks the concept) — a disabled sat should not be an ordinary eligible contributor, only usable as
the fallback source and the canonical-demand source. This requires new data flow into
`eligible`/`ResolveSO` (neither currently receives config) — a real gap, not a small tweak.
Corrected my own initial misreading (I had concluded sat has no enabled state at all — wrong).
User also corrected the `ResolveSO` decision taxonomy: `single` should be symmetric (exactly one
contributor, regardless of whether it's sat or not) rather than sat-special-cased; `sat-fallback`
should be the NARROW case of a disabled sat still contributing as emergency fallback, not simply
"sat happened to be the last one standing"; `no-signal` should also specifically cover a broken/
missing sat. Confirmed `HasUsableCompositeSignal` is a per-MODEL boolean (loops all
VariantCapacities, true if ANY has a signal) when the right granularity is per-SO — but user then
added an important nuance: the ORIGINAL pre-mission check (`hasSaturationResult`, before this
mission's rename) was specifically about saturation's own presence/health, categorically worse
than any other single analyzer being broken (sat defines the canonical demand unit the whole
composite is expressed in) — confirmed via the pre-mission doc comment (`git show c013012e`).
Both a per-SO gate AND a model-level sat-health gate are likely needed; the current single
boolean conflates them. User also flagged: decision paths should be enumerated/typed where
possible (extending the earlier stronger-typing point specifically to decision-path values, e.g.
mirroring `domain.DecisionReason`'s pattern rather than untyped string consts); `C0-agree` is a
misleading name (nothing checks that contributors' values actually agree — just that more than one
existed; "aggregate" or "composite" is more accurate); "A3" (the spec-decision label cited in
`eligible()`'s comment) was NOT something the user specified — a code/spec-authoring artifact,
correcting the record. User also noted an Informative-vs-Live asymmetry as an explicitly
UNRESOLVED, not-yet-designed question (confidence in a result might reasonably differ by
direction — safe to scale up on, not safe to scale down on — unlike Live's broken-pipeline
symmetry, which IS correctly symmetric today) — recorded as an open design question, not a defect
to fix.

Also found, unprompted while investigating: `internal/engines/steadystate/composite.go`'s
`contributedNames` map is written into but never read anywhere — dead code. User: needs fixing,
wants it actually wired up (for error/anomaly detection), not deleted.

All ten aggregation sub-points, all seven allocation sub-points, all rulings and corrections
(mine and the user's, in both directions) are written out in full in
`.session/review/code-review-notes.md` — this ledger summarizes; that file has the complete,
precise record with code citations for each point. No code was changed anywhere during this
review, per explicit user instruction for the whole exercise.

Review paused (not finished) at the end of the allocation-core section — user said "good point to
stop," asked to persist and wind down, with explicit intent to continue this same review in a
future session.

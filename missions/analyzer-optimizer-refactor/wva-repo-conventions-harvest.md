# WVA repo conventions harvest — `plans-tooling/` methodology + analyzer/optimizer proposal survey

Source repo (read-only, not modified): `/home/dean/code/llm-d/llm-d-workload-variant-autoscaler`

This is a research harvest, not a design doc. It answers five specific questions about that
repo's in-progress planning-methodology system (`plans-tooling/`) and briefly surveys whether two
workstream worktrees already contain analyzer/optimizer-refactor-relevant design docs.

---

## 1. The "atomic-step" methodology/convention system in `plans-tooling/`

### Top-level shape

`plans-tooling/` is a **tooling and convention worktree**, developed on its own branch and
described in its `README.md` as "Scripts for fetching documentation by heading instead of by line
number, plus two gates that check a coder's work and a spec's shape mechanically, plus a
golden-file test harness." Its own directories:

- `conventions/` — ~30 topic files, each holding one or more `### convention: <name>` marker
  blocks (the atomic addressable unit of the whole system).
- `planning/` — Type-3 design/plan docs for the meta-work of building this tooling itself (the
  authoritative one is `planning/atomic-step-protocol-design-v2.md`, 1227 lines).
- `model/` — `workspace-structure.md`, describing the physical bare-repo/worktree layout.
- `roles/`, `collections/` — per-agent-role "root" documents and grouped citation collections
  (built by a checklist item referenced inside `planning/micro-rules-program-plan.md`).
- `scripts/` — the addressing/gate tools themselves (`sec.sh`, `conv.sh`, `conv-list.sh`,
  `conv-lint.sh`, `step-check`, `plan-lint`).
- `session/`, `reviews/`, `reasons.md`, `tests/` — session-tracking, review notes, a rationale
  ledger, and the golden-file test suite for the scripts.
- **Key fact:** these scripts are *authored* here but **not consumed** from here — the README
  states "These scripts are developed on their own branch and are copied into `plans/scripts/`
  at kickoff. They are not pushed from here." So the live, in-force copy that other worktrees
  (the `ta-*`/`mr-*` dirs, `plans/`) actually invoke lives under `plans/`, not `plans-tooling/`.

### The addressing model (why headings, not line numbers)

Two rules recur through every tool in `plans-tooling/README.md`:

> **An id is a heading slug or the exact heading text.** The slug is the GitHub anchor form:
> lowercased, punctuation dropped, spaces turned into hyphens. `## Alpha child` is addressable as
> either `alpha-child` or `Alpha child`. If two headings in a file produce the same slug, that id
> is ambiguous and every tool refuses it rather than picking one.
>
> **A section ends at the next heading of the same or higher level.** A deeper heading stays
> inside. So a `##` section swallows a `####` subsection that follows it, and is terminated by the
> next `##` or `#`. …
>
> **No tool takes or emits line numbers.** Nothing here accepts an offset or a length, and
> nothing here prints a line range. Line numbers are a global index over a mutable file, so any
> insertion invalidates every number below it. The only place a line number appears at all is
> inside an error message, to help a human find a malformed heading.

A **convention** is declared by a heading of the exact form `### convention: <name>`, scanned by
`conv.sh` across `conventions/*.md` — "There is no index file: the marker carries the name, so a
scan cannot go stale the way a stored index can." `conv-list.sh` renders a three-field index
(`<name> | <status> | <description>`), and `conv-lint.sh` enforces structural rules on every
convention block (see class table below).

### Convention block schema (from `conv-lint.sh`'s check table in the README)

Every convention marker must carry five required fields (`description`, `scope`, `trigger`,
`status`, `origin`); `status` must be exactly `active` or `probation`; the name must match
`[a-z0-9-]+` and sit at heading level 3; no heading at level 2 or shallower may follow a file's
first convention marker (or it would silently truncate the convention above it); and any
backtick-quoted path-like token must resolve on disk. Every convention file surveyed in this repo
follows this shape, e.g.:

```
### convention: <name>
description: <one line>
scope: <who/when this applies to>
trigger: <the event that makes this fire>
status: active | probation
origin: <where this rule came from — a session/CONVENTIONS.md section+code, an incident, a
         feedback file, etc.>

<prose body — the actual rule, in full>
```

### Gates: `step-check` and `plan-lint`

Two mechanical gates enforce the methodology on real work:

- **`step-check`** — run once a coder believes a step is finished. Checks (a) **scope
  containment** (every changed path must sit under a declared `--scope`, bias toward refusal),
  (b) **sign-off policy** (`--lineage code|plans`, no default — `code` requires `Signed-off-by` on
  the tip commit, the `plans` lineage forbids it), and (c) **judgment mark** (with `--ledger` and
  `--step`, verifies every judgment named in a coder's own status file has an isolated, tagged,
  surfaced commit, and that no `judgment/*` tag exists with no ledger entry).
- **`plan-lint`** — turns a code spec's completeness into a machine check: every `## S<n>` step
  section must carry all eight fields (`brief`, `scope`, `do`, `conventions`, `verify`,
  `done_when`, `on_fail`, `record`); the `## Intent` block must carry its own five fields; every
  step needs a brief in `## Step index`; every cited convention name must resolve; no
  line-number addressing may survive anywhere in the file; and no unresolved `judgment/*` tag may
  name one of the spec's steps.

### The step schema itself (from `planning/atomic-step-protocol-design-v2.md`, § The step)

This is the authoritative "atomic step" unit definition — a step is split by *when* content is
needed, not by who reads it: a short **brief** (intent/decisions/rationale/hazards, read as
orientation up front) and a **detail** (exact commands, file lists, rule manifest — fetched only
when that step actually executes). The schema:

```
## S07 — <imperative title>
brief:      intent · decisions made · rationale · expected hazards   (3–5 lines)
scope:      <paths this step may write — the only ones>
do:         <2–6 imperative lines; closed actions, no alternatives, no goals>
conventions: <manifest with triggers, or the literal `none`>
verify:     <exact commands + expected result>
done_when:  <observable predicate>
on_fail:    halt-or-mark              # see § The halt rule — not a bare halt anymore
record:     <what to append to the step log>
```

Key rules stated alongside it:
- `do:` states **closed actions, never goals** — "a goal leaves a gap that gets filled with
  presumption."
- Rules are **linked, not inlined**, each carrying a trigger (e.g. `BEFORE commit →
  commit-dco`); `conventions: none` is a legal, **affirmatively required** value — omission halts,
  because otherwise "within a week the coder learns to read a missing field as meaning `none`."
- **Step size**: one step = one commit (smaller is better); read-only steps produce a step-log
  entry and no commit; bound step size by tool calls (roughly 5–15), not lines.
- **Re-read before declaring done**: the coder re-reads its own step section before writing the
  step-log entry.

### Document shape (§ Document shape)

> The code spec has **two audiences and one source**... No new document type.
>
> ```
> ## Intent                    ← Dean, plan-confirmation, external review, code-verification
> ## Step index                ← the briefs, in order: the narrative
> ──────────── execution detail below ────────────
> ## S05 — <title>             ← coder, at execution
> ## S06 — <title>
> ```

The Intent block (checked by `plan-lint`'s class-22 rule) is required to carry five fields,
mirroring the step schema's own five-field discipline one level up.

---

## 2. Task template with Intent / Expected outcome(s) / Todo / Refs / Status

**Yes — found, but it lives in `plans/planning/task-tracking-template.md`, not
`plans-tooling/planning/task-tracking-template.md`.** The `plans-tooling/planning/` directory has
no file by that name; the actual authoritative file is on the sibling `plans/` worktree. (This
matters for question 3 below — it is itself a cross-worktree reference case.)
`plans-tooling/conventions/plan-authoring.md` does **not** define this template — it defines a
different thing (the Type-3 "micro-rules" plan-doc structure: Reading Protocol block, TOC block,
on-demand content sections, rule-file citations — see convention `plan-authoring-micro-rules`,
quoted under question 3's neighboring conventions above). The task-tracking template is a
narrower, newer, still-draft artifact for **task sections inside** a roadmap/plan doc, not for the
plan doc's own top-level shape.

Status stated at the top of that file: **"design · Status: DRAFT — proposed 2026-08-24, one
worked example applied, not yet adopted broadly."** Per the file, Dean's own scoping is
**"new template only for now"** — existing docs (`micro-rules-program-plan.md`'s table, the
checklist, `item11-classification-table.md`) are explicitly **not** retrofitted by it.

Full quote of the template itself (`plans/planning/task-tracking-template.md`, lines 21–58):

```markdown
## The template

One task = one section, this shape, in order:

​```markdown
### <Task ID> — <short name>

**Intent.** One or two sentences: why this task exists, what problem it closes.

**Expected outcome(s).** The concrete artifact(s)/state this task produces, stated as a checkable
claim — not "do the work" but "X exists, verified by Y."

**Todo.**
- [ ] Sub-item, smallest unit worth its own status
- [x] Sub-item already done — keep it checked, don't delete it once done (this list is the
  task's own running record, same append-only discipline as everything else in this mission)

**Refs.** Every doc/file this task reads from or writes to, as a clickable relative link where the
target is in the same worktree; a plain-text path (not a broken link) where it crosses a worktree
boundary the renderer can't follow — see `conventions/plan-authoring.md`'s
`plan-authoring-relative-links-worktree-boundary` entry in `plans-tooling` for why that distinction
matters. Group by role if the list is long: *Reads:* / *Writes:* / *Coverage reports:*.

**Status.** One line, dated: `DONE <date>` | `IN PROGRESS, <what's left>` | `NOT STARTED` |
`BLOCKED on <thing>`. Followed by completion notes if DONE — what actually landed, which commit(s),
any real finding worth a reader knowing without re-deriving it.
​```

**Notes on applying it:**
- Not every field needs prose every time — a one-line task can have a one-line Todo and a one-line
  Refs. The template's job is to make room for detail when a task has it, not to force padding
  onto a task that doesn't.
- 👉 A task with sub-tasks (like row 4's T1-T4) gets **one outer section** with the shape above,
  and each sub-task gets its **own nested section** in the same shape — the outer section's Todo
  list becomes a checklist of the sub-task names, each linking down to its own section.
- Status is the one field that changes most — update it in place (not append-only) since it is by
  definition "the current state," but completion notes accumulate rather than replace, same as
  every other status file in this mission.
```

Note the template already cross-references question 3's convention by name inside its own `Refs`
field description — the task-template author clearly treated the worktree-boundary link rule as a
load-bearing dependency, not incidental.

The rest of that file (not reproduced in full here — see the source) applies the template once, as
a trial, to "Program plan row 4 (the d+e pipeline)" with nested `T1`/`T2`/`T3`/`T4` sub-task
sections, each independently carrying Intent/Expected outcome/Todo/Refs/Status — this is the
worked example proving the nesting notes above.

---

## 3. Cross-worktree link convention

**Yes.** `plans-tooling/conventions/plan-authoring.md` defines exactly this, as
`plan-authoring-relative-links-worktree-boundary`. Full quote:

```
### convention: plan-authoring-relative-links-worktree-boundary
description: Links inside a doc should be relative for GitHub/clone portability, but a relative link can never cross into a different worktree; verifying the target exists on disk is not the same check as verifying the link resolves for a reader.
scope: planner or coder writing a link inside any doc
trigger: adding a link in a doc that could point at a file in a different worktree
status: active
origin: feedback_relative_links_within_docs.md

Links written inside a document (not chat) should be relative paths, scoped to that doc's own
location — this repo uses a bare-repo-plus-worktrees layout, and docs get cloned/browsed on
GitHub, where an absolute local filesystem path is meaningless. But relative markdown links
cannot walk `../../` across a worktree boundary the way a shell can, even when both worktrees
share a parent directory and the resolved path is a real filesystem path — a renderer scoped to
one repo/worktree (VSCode, GitHub) cannot follow it. **Checking that the resolved path exists on
disk is not the same question as "does this link work when clicked from where the reader
actually opens it."** A naive existence check can report zero broken links while every
cross-worktree link is still broken.

**How to apply:** before trusting a link-checker's "0 broken links" result, ask whether any
linked target lives in a different worktree than the document itself. If so, there is no
relative path that both resolves on disk *and* renders correctly in GitHub/VSCode across a
worktree boundary — this is a genuine unsolved case in this repo's layout. Ask how it should be
handled (a plain-text path for manual navigation, a documented convention, etc.) rather than
assuming a scheme works because it exists on disk. Links that stay within the same worktree as
the doc: relative, and a naive existence check is a reasonable verification. (Distinct from the
already-settled chat-message case — see `conv:chat-links` — where the fix is a workspace-relative
markdown link, not this unsolved cross-worktree case.)
```

The task-tracking template (question 2) is itself a live instance of the problem this convention
names: it lives in `plans/planning/`, cites `plans-tooling/conventions/plan-authoring.md` by
**plain-text/backtick path**, not a clickable relative link — because that target sits in a
different worktree. Its own worked example's `Refs` section also mixes relative links (same
worktree, `../../plans-tooling/...` — actually note these *are* written as relative links across
what the doc calls a worktree; on the physical filesystem `plans` and `plans-tooling` are sibling
worktrees of the same bare repo, so `../../plans-tooling/...` resolves on disk, but per the
convention above this is exactly the case flagged as "resolves on disk, may not render for a
reader depending on where they open the doc" — the template file was written before/without fully
reconciling that tension in its own worked example).

The sibling convention `chat-links.md` (`chat-file-links`) handles the analogous problem for a
**chat message** rather than a doc — referenced above as "the already-settled chat-message case,"
distinct from this still-open in-doc, cross-worktree case.

---

## 4. Numbered/lettered sections, status icons, append-only discipline

### Status icons — session titles

`plans-tooling/conventions/session-start.md` defines `session-start-title-convention`:

```
### convention: session-start-title-convention
description: Session titles follow [icon] subject Role, with the role word spelled out (Coder/Review/Planner/Triage/Sync/Chat) since the icon alone is ambiguous; PR-bound sessions lead with PR #<N>.
...
Session titles read `[icon] <subject> <Role>`, e.g. `💻 pd-role-ceiling Coder`,
`📐 utilization-terminology Planner`, `💬 session-title Chat`. USER doesn't reliably remember which
icon maps to which role, so a spelled-out **role word** is appended: Coder / Review / Planner /
Triage / Sync / Chat. Prefer **shorter** subjects when clear (2–3 words).

Icon↔role: 🔍 Triage · 👀 Review · 📐 Planner · 🔄 Sync · 💻 Coder · 💬 Chat. (Role word is
**Review**, not "Reviewer" — reads better as "PR #1229 Review".)

**PR-bound sessions must lead the subject with `PR #<N>`** so they line up uniformly in history,
role word by mode: `👀 PR #1229 Review` (reviewing a PR), `🔍 PR #1246 Triage` (working reviewer
comments/CI to land a PR), `💻 PR #1250 Coder` (coding fixes on an open PR). **Non-PR sessions:**
`[icon] <topic> <Role>`. Internal code/doc reviews use the same 👀 icon and read
`👀 <topic> Review`, distinct from PR reviews only by the `PR #<N>` lead.
```

No standalone convention names "status icons for documents" as its own entry — the icon usage
found is scoped specifically to **session titles**, not to task/status-doc field values. Status
*values* inside status/task docs are plain text tokens (`DONE`, `IN PROGRESS`, `BLOCKED`,
`NOT STARTED`, `PROBATION` in uppercase for the special "not yet ratified" convention-status case
— see `conv-list.sh`'s rendering rule in the README, question 1), not emoji/icon-based.

The one non-role emoji convention found in practice (not a formal marker, but consistently used)
is 👉 as a "notice/callout" marker inside prose (e.g. task-tracking-template.md's own "👉 A task
with sub-tasks... gets one outer section"), and ⚠️ for correction/warning callouts (seen in
`plan-authoring.md`: "⚠️ **Corrected 2026-08-10.**").

### Numbered/lettered sections

No single convention formalizes numbering syntax as its own rule, but the pattern is used
consistently and is explicitly named in practice:
- **Checklist items** are plain integers with letter suffixes for inserted sub-items:
  `1, 2, 2a, 3, 5, 9, 10, 25` (from `planning/micro-rules-checklist.md`) — the letter suffix marks
  an item inserted after the fact without renumbering everything below it.
- **Sub-task IDs** inside the task-tracking template use `T1`/`T2`/`T3`/`T4` (question 2's worked
  example), and step IDs use `S<n>` (`S05`, `S06`, `S07` in the atomic-step schema, question 1).
- **Program-plan rows** are referenced by plain row number (`"row 4"`, `"item 11"`) tying a table
  row to prose discussion elsewhere.
- `summary-and-consolidation-integrity.md` explicitly instructs enumerating "every discrete item
  in every source document (**every numbered/lettered checklist entry**, every table row, every
  dated section...)" as the unit of item-by-item verification — i.e., numbered/lettered items are
  the canonical atomic unit for the anti-content-loss check described next.

### Append-only editing discipline

This is a strongly and repeatedly stated discipline, though the *exact* rule differs by doc type:

- **Task-level Todo lists (task-tracking-template.md)**: explicitly append-only — "keep it
  checked, don't delete it once done (this list is the task's own running record, same
  append-only discipline as everything else in this mission)."
- **`micro-rules-checklist.md`** states its own rule directly: *"Update by checking a box and
  adding a one-line pointer to the commit that did it — never by deleting a line, even a completed
  one,"* plus a standing instruction from Dean: *"any time a task is raised in discussion and not
  immediately executed, it goes here before the conversation moves on — regardless of whether it
  seems blocking"* (added after an earlier version silently dropped 7 agreed-on items).
- **CURRENT.md (`current-md-bounded-shape`)** is the interesting exception/nuance: it is
  explicitly **not** simple append-only — it is a *bounded* live-state doc whose "Editing
  discipline" section states **verify-or-copy-then-delete, per item**: "Before removing any
  detail, confirm it already exists in its permanent home... If it does, delete here; if not, copy
  it there and verify first. A forward-looking TODO with no other home must never be dropped." And
  explicitly: *"Tidy by targeted edits, never a blind wholesale rewrite. A full-file rewrite
  reconstructs from memory and silently loses items that don't fit the template."*
- **`status-files-coder-format`** for the coder's own status file is explicitly the *opposite* of
  append-only: *"full-snapshot rewrite (not append-only) at every meaningful checkpoint"* — because
  it is a **living** heartbeat doc (current state), not a **ledger**. The template quotes this
  distinction directly: *"Status is the one field that changes most — update it in place (not
  append-only) since it is by definition 'the current state,' but completion notes accumulate
  rather than replace, same as every other status file in this mission."*
- **`summary-and-consolidation-integrity.md`** (`summary-item-by-item-check`) is the strongest,
  most recent statement of the underlying discipline, born from a real incident (2026-08-22): a
  wholesale `Write` "nearly replaced a 643-line append-only status file with a 320-line
  resynthesis," caught only because Dean happened to ask about coverage on an unrelated document
  minutes later. Its mandate, verbatim:

  > **Mechanically, before any qualifying Write/Edit:**
  > 1. Enumerate every source document being summarized/consolidated/superseded.
  > 2. For each one, extract its own discrete item list (numbered items, table rows, dated
  >    sections, flagged open questions/footguns) — read the actual file content, not a memory of
  >    it, even if it was read earlier in the same session.
  > 3. Build an explicit item-by-item mapping... every item placed as present / relocated /
  >    explicitly-dropped-with-reason. Nothing left unaccounted for.
  > 4. Only then write the new/consolidated document.
  > 5. If the operation is a **wholesale replacement of an existing file**... the item-by-item
  >    mapping is mandatory even when the change "feels" like pure improvement... Prefer
  >    `Edit`/targeted, append-only changes over `Write`-based wholesale replacement for any file
  >    with pre-existing multi-line content.

  It also recommends delegating the "enumerate and map" pass to a background agent as "the
  preferred execution path, not merely an option," to keep the mechanical enumeration out of the
  main session's context.

**Summary of the discipline as a whole**: append-only applies to *ledgers/checklists/status
histories* (never delete a completed or superseded entry — mark and keep it); bounded-but-verified
editing applies to *the one canonical live-state doc* (CURRENT.md — trim only after verifying the
detail has another permanent home); full-snapshot overwrite applies only to *per-branch coder
heartbeat status files*, which are explicitly transient/broadcast, not historical record.

---

## 5. `analyzer-metric-proposal/` and `optimizer-pd-role-ceiling/` — relevant design docs for a single-analyzer + combining/reduction refactor

**Yes — directly relevant.** Both directories are full worktree clones of the same upstream WVA
repo (same `internal/`, `pkg/`, `docs/` tree), checked out at different points/branches. Between
them, the two specific documents named in the task exist and are germane:

- **`analyzer-metric-proposal/docs/proposals/analyzer-metric-interface.md`** (342 lines, Draft,
  authored by Dean Lorenz, created 2026-07-21) — "Proposal: A Metric-Based Analyzer Interface for
  WVA." Proposes collapsing every analyzer's contract to exactly **two numbers per
  finest-grain item** — a demand $D$ and a per-replica target $P$ (so $D/P$ is a replica count) —
  in a KEDA/HPA-shaped vocabulary, explicitly stating a **reduction** step (per-pod → per-model
  sum for demand, per-pod → per-ScaledObject average for target) and a **combining** rule across
  analyzers ("each analyzer's contribution is reduced to replicas before anything is combined";
  variant-alternatives sum, roles combine by min). This is squarely a single-normalized-interface
  + reduction-step design for the analyzer→optimizer boundary. **Not present** in
  `optimizer-pd-role-ceiling/` (no `docs/proposals/analyzer-metric-interface.md` file there at
  all — that worktree's `docs/proposals/` only has `sglang-backend.md` and `deprecate-va-crd.md`).

- **`docs/developer-guide/multi-analyzer-pipeline.md`** — present in **both** worktrees, but the
  two copies differ (`diff` confirms non-identical content, i.e. they've diverged across
  branches/worktrees). The `analyzer-metric-proposal` copy (383 lines) documents the *current,
  already-multi-analyzer* pipeline as-built: analyzers run in series each cycle, each producing an
  `AnalyzerResult`; the optimizer reads a `[]NamedAnalyzerResult` slice and "decides scaling
  actions over it via shared free functions." It documents a `## How results combine` section and
  a `Linearity invariant` for how per-analyzer contributions compose during allocation — i.e. this
  file is the descriptive baseline the analyzer-metric-interface proposal above is proposing to
  simplify/replace. Given the task's phrasing ("refactoring... to use a single-analyzer model with
  a combining/reduction step feeding the optimizer"), this doc is the necessary "what exists
  today" counterpart to the proposal doc above.

- **`analyzer-metric-proposal/docs/developer-guide/analyzer-checklists.md`** — not opened in depth
  this pass, but by name likely holds the per-analyzer implementation checklist that a
  single-analyzer refactor would need to reconcile against; worth a follow-up read.

- **`analyzer-metric-proposal/docs/design/modeling-optimization.md`** and
  **`docs/design/controller-behavior.md`** — the broader WVA architecture/modeling docs (present
  in both worktrees); `modeling-optimization.md`'s opening section ("Under the hood") frames the
  analyzer/optimizer split at a conceptual level (model analyzer → performance profile → optimizer
  sizing) and would be the doc most likely to need updating if the analyzer contract itself
  changes shape. Not deep-dived this pass per the task's scope instruction.

`optimizer-pd-role-ceiling/` itself does not appear to hold a role-ceiling-specific *design* doc
outside code (no `docs/proposals/*ceiling*` or `docs/design/*role*` files found; "role"/"ceiling"
hits there are all in `internal/`, `pkg/`, `config/` RBAC file names, i.e. implementation, not
design prose) — its relevance to the analyzer/optimizer refactor question is chiefly that it
shares (a divergent copy of) `multi-analyzer-pipeline.md`, not that it adds its own proposal doc.

No file named `docs/developer-guide/multi-analyzer-pipeline.md` review pass or deeper content
comparison between the two worktree copies was done — flagged here as a natural follow-up if the
refactor work needs to reconcile which worktree's version is more current.

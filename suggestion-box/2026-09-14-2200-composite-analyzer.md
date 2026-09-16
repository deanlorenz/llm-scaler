# Suggestion — `tasks.md`'s mission-spec template needs a spec/task-hierarchy split and a coder-design-validation checkpoint

**Status: DRAFT, NOT POSTED.** Per user instruction — capture now, do not submit; revisit after
we try the revised template in practice on `composite-analyzer`.

**Source:** `composite-analyzer`, mission-owner session `2026-09-14-composite-analyzer-2`, surfaced
when the user halted work mid-session because `composite-signal-redesign.md` had drifted back
into unreadable prose despite an explicit prior-session restructure into `tasks.md`'s existing
8-section template.

**What happened.** The prior session (2026-09-14, commit `59a53001`) restructured this doc
correctly into `tasks.md`'s template. This session, mid-incident, under time pressure, the
mission owner edited §2 (settled-rules-only, no reasoning, per the template) four separate
times, each time appending a full "Correction (date, caught during...)" narrative paragraph
directly into §2 instead of updating the rule as a flat statement and moving the why/how into
§5/§7 where the template already says narrative belongs. Not a case of no convention — the
convention was identified (the doc's own header cites it) and not followed under pressure.

**Root cause, as diagnosed with the user.** Two real gaps in `tasks.md`'s template, not just an
execution lapse:
1. The template has no distinct "HOW/pseudo-code" layer between a roadmap item's WHAT and its
   implementation. §5 (roadmap) is written at a fixed resolution; a code-level sub-mission needs
   the SAME section written at a deeper resolution (call stack, structure, key constraints — but
   NOT literal target-language code), not a separate section. The mission owner initially
   modeled this as a new, separate section and was corrected twice by the user before landing on
   "§5 is recursive: same section, depth scales with the doc's level."
2. Separately, the actual pipeline that produced the bad coder output was missing a stage:
   intent → coder proposes code-level design (types, function boundaries, key decisions) →
   **validate with the mission owner, sometimes the user, before implementation** →
   implementation. The template (and the mission owner's task file) went straight from intent to
   near-final shape with no validation checkpoint, independent of how much freedom the coder had
   within that shape. Contributing but secondary: the mission owner's task file also contained
   literal Go pseudo-code, giving the coder no room to exercise design judgment even where the
   process gap didn't already block it.

**Revised template, tried for real on `composite-analyzer`
(`composite-signal-redesign.md`, commit `37886266`) — this is the section order actually
applied, corrected from an earlier draft ordering that had §2/§3 swapped:**
1. Orientation — fuses old §1 (summary) + §2 (principles) + §3 (at-a-glance); human-readable.
2. Spec/roadmap — old §5, explicitly recursive: mission-level = roadmap of sub-missions;
   sub-mission/code-level = the same section at deeper resolution (pseudo-code/call-stack/
   structure, few degrees of freedom left, but never literal implementation-language code).
   In practice this section keeps its original §2 numbering across a doc's whole life — task
   files cite `§2.x` directly, so preserving §2's number (not §5's) when a doc gets restructured
   matters for every task file already pointing at it.
3. Open items — old §4, blocking items only for owner/user; closed items dropped, not archived.
4. Coder task hierarchy — old §6 (outline): each spec item → one task file; each sub-item → one
   step within it.
5. Discussion abstracts — concise, processed bottom-line per item (not a log); unified with §7
   as its abstract half.
6. Summary of decisions — flat list, each → ref into §5/§7, decision's impact + rejected
   alternatives + why rejected, for owner/user tracking.
7. Detailed discussion — full paper trail per item, the detailed half of the §5/§7 unification.
8. Revision log.

**Tried-in-practice findings, added after actually executing the restructure once (not just
proposing it):**
- The rebuild is safe to do without regenerating from memory: read the whole source file fresh,
  build the new structure in a scratch file by relocating exact existing text, then diff
  word-count and every code citation (`file.go:N` pattern) against the original before applying
  — this caught one real dropped citation in the first draft pass. Worth stating as the required
  method for any future doc restructure of this kind, not left as an unstated best practice.
- Even after this restructure landed, the SAME failure mode (adding narrative directly into the
  settled-rules section under time pressure, mid-review) recurred in miniature during the very
  next review round — this time in the reviewer's OWN response, not the doc itself, but same
  underlying pull. The template fixes the doc's resting state; it does not by itself prevent an
  in-the-moment slip while actively editing under pressure. That gap is process discipline
  ("always diff/verify before applying," "always ask before generalizing/posting"), not
  something the template's shape alone can close.

Separately, a new process rule (not a template-shape change): every coder task file must
include a mandatory design-validation checkpoint — the coder proposes its code-level design
inside the SAME task file, stops, reports on `Out:`, and only proceeds to implementation after
the mission owner (and, when the owner escalates, the user) approves it. Not a second task file
or invocation — one task file, two phases, a hard stop between them.

**Where it might land:** `conventions/tasks.md`'s "Mission spec / roadmap structure" (the
template itself) and `conventions/coder-orchestration.md` (the new mandatory design-validation
checkpoint, likely as a new numbered rule near rule 7's wait-for-instructions mode).
`policy-writer`'s call on exact wording, and on whether `state-vs-ledger.md`'s task template
also needs a design-phase/implementation-phase field split to carry the checkpoint cleanly.

**Explicitly out of scope for this suggestion:** `STATE.md`/`state-vs-ledger.md`'s task
template is a separate, more compact, progress-tracking template and is not being changed by
this proposal — the user was explicit that STATE is not a spec and must not be conflated with
this discussion.

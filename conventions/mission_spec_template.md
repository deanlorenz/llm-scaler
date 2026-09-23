# Mission spec / roadmap template

Use this structure for missions with extended history and multiple tasks.
For reading rules and authoring guidance see `conventions/tasks.md`.

```markdown
## 1. Orientation
   Fused overview: what this mission builds, why, and the key settled
   principles/constraints. Human-readable. One paragraph + compact bullet list.

## 2. Spec / roadmap                  ← READ UP TO HERE UPFRONT. STOP.
   Recursive — depth scales with the doc's level:
   - Mission-level doc: a roadmap of sub-missions (flat checklist, one line per task).
   - Sub-mission / code-level doc: pseudo-code, call stack, structure, key constraints —
     never literal implementation-language code. A few degrees of freedom left to the
     coder; design intent explicit.
   §2 numbering is stable for the lifetime of a doc. Task files cite §2.x directly.

## 3. Open items
   Blocking decisions and open questions for owner/user only.
   Closed items are dropped, not archived here.
   Pull this section when you need a decision, not at session start.

## 4. Coder task hierarchy
   One task file per §2 item; one step per §2 sub-item.
   Navigational index into §5/§7. Pull to find a specific task file.

## 5. Discussion abstracts
   Concise processed bottom-line per item (not a log).

## 6. Summary of decisions
   Flat list: each decision → ref into §5/§7, impact, rejected alternatives, why rejected.
   For owner/user tracking.

## 7. Detailed discussion
   Full paper trail per item. Pull individual subsections on demand; do not read upfront.

## 8. Revision log
```

## Reading rule

A session reads sections 1–2 at session start. It does not read sections 3+ unless it
needs a specific item — look it up by section or outline entry, read only that subsection.

## Restructuring an existing doc to this template

Read the whole source fresh, build the new structure in a scratch file by relocating exact
existing text (no rewriting), then diff word-count and every code citation (`file.go:N`
pattern) against the original before applying. This catches dropped citations.

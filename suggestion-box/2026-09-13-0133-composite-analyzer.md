# Suggestion — `ledger-capture` is relied on as a safety net for STATE's claims, but its contract never asks it to check STATE against ground truth

**Source:** `composite-analyzer`, mission-owner session `2026-09-12-composite-analyzer-1`, surfaced
2026-09-13 when the user asked "did we go over D3 files?" and a fresh cross-check against
`git diff --stat` found STATE.md's "not yet reviewed" file list silently omitted 4 real files
(`analyzer_helpers.go`, `query_api.go`, `cost_aware_optimizer.go`, `rescale.go`) from the actual
diff. The user's own framing: "ledger-capture is a safety net, not a replacement to maintaining
planning and progress tracking file... this is a major failure."

**What happened.** Two `ledger-capture` passes (2026-09-08/2026-09-09, recorded in
`.session/ledger/2026-09-08-composite-analyzer-2.md`) verified this mission's ledger content
against durable docs (STATE.md, spec.md, git log, the actual files) and both reported no gaps.
One of those passes did catch a real issue — STATE claimed a ledger file lived at
`.session/ledger/...` before it had actually been moved there — but that is a check of
*ledger-vs-filesystem* state, not of *STATE's substantive task-tracking claims* against the
codebase. Neither pass, nor the resume protocol that read STATE afterward, caught that STATE's
remaining-file list didn't match `git diff --stat c013012e..composite-analyzer`.

**Root cause.** `ledger-capture`'s contract (`conventions/resume-and-handoff.md`, "`ledger-capture`
Contract" item 2) is to fold the *ledger's own* findings into durable docs — read the ledger,
identify corrections/decisions/rules, capture them. It is never asked to independently regenerate
ground truth (e.g., diff the actual file list, re-run a test suite) and compare that against what
STATE claims. Separately, the Resume/Takeover Protocol's step 3 ("Pending Scan") only checks
Session-log entries for `active`/unretired status and ledger `## Verified` markers — it never asks
the resuming session to verify STATE's *substantive* claims (a checklist, a file list, a status)
against an external source of truth. Both gaps let a wrong STATE claim survive two verification
passes and a mission takeover untouched, until a user's direct question forced a fresh check.

**Rule proposed:** when STATE tracks a checklist or file list derived from an external source of
truth (a diff, a file tree, a test suite, a config), the resume protocol (or `session-start.md`)
should require regenerating that source and diffing it against STATE's claim — at least once per
resume, not only when something feels off. A clean `ledger-capture` pass should not be treated as
proof that STATE's task-tracking content is accurate; `ledger-capture` verifies the ledger's
narrative was captured, not that the narrative itself matches ground truth.

**Where it might land:** `conventions/resume-and-handoff.md`'s Resume/Takeover Protocol (a new
step near "Pending Scan"), or `conventions/session-start.md`. `policy-writer`'s call on exact
wording and placement.

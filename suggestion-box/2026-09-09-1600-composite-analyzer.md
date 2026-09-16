# Suggestion — sharpen the STATE-vs-plan-doc boundary; STATE drifted into holding design/decisions

**Source:** `composite-analyzer`, mission-owner session `2026-09-08-composite-analyzer-2`, surfaced
2026-09-09 when the user pointed out STATE.md's "Status" section had accumulated the mission's full
design ("Design core" bullets, rejected approaches, lessons banked — all duplicated from `spec.md`)
plus a detailed verification narrative for an implementation pass (which belongs in the ledger, not
STATE). Fixed in this mission (STATE.md cut from ~300 lines to ~76; the duplicated content deleted
since it already lived in `spec.md`; the verification narrative backfilled into the session ledger,
which itself had silently stopped being updated continuously while the narrative accumulated in
STATE instead).

**The drift, in the user's words:** "You are conflating design docs with progress tracking. The
plan/design docs should hold (1) the concrete agreed-on design (2) reasoning and decision history
... The STATE doc should mostly track progress ... Status should not include all the reasoning —
it should focus on current open items, on-going discussions, WIP, and pending decisions. Anything
that was already decided and finished discussions should be in the plan doc."

**Why this is easy to drift into:** `state-vs-ledger.md`'s rule of thumb ("if a finding changes what
a resuming session needs to know or do, its conclusion goes into STATE") is correct but incomplete —
it distinguishes STATE from the *ledger*, but says nothing about STATE vs. the *plan/spec*. In
practice, every individual STATE edit during a long implementation pass felt like "a finding that
changes what a resuming session needs to know" in isolation, so each one got appended — but their
sum duplicated the plan doc's design content and ballooned Status into a second, worse copy of both
the spec and the ledger. No single edit looked wrong; the accumulation was the problem.

**Rule proposed — a third split, not just STATE-vs-ledger:**
- **Plan/spec doc** (`spec.md` or equivalent): the agreed design, *and* decision history with its
  reasoning/alternatives/verification, organized so the top sections are the current concrete
  design (must-read) and a roadmap/task list, with detailed per-topic discussion and the full
  decision log at the bottom. Already-decided, already-finished discussions belong here, not in
  STATE — even after they were first hashed out over several STATE updates during the discussion.
- **STATE.md**: short, current-tense only. Open items, in-progress work, pending decisions, and a
  thin *pointer* into the plan doc's relevant section — never a restatement of settled design.
  Should be recognizable as "the same file" whether the mission has 1 decision or 50 behind it.
- **Ledger**: the narrative of *how* work happened this session — findings, false starts,
  verification steps taken, as they occur. Should be updated continuously; if it isn't (as happened
  here — narrative accumulated in STATE instead across a multi-hour implementation dispatch and
  review cycle), that's itself a signal STATE is absorbing ledger content.

**Where it might land:** `conventions/state-vs-ledger.md` — add the plan-doc axis explicitly (right
now the file's title and content only discuss STATE-vs-ledger, not STATE-vs-plan). `tasks.md`'s
mission-spec-structure section (§ "Mission spec / roadmap structure") already models the right shape
for the *plan* doc; it could cross-reference this rule so a mission owner reads both together.
Possibly worth a periodic self-check prompt (e.g. in `mission-owner.md`): "does this STATE update
restate something the plan doc already says, or narrate how a step went rather than what's still
open?" — since the failure mode here was never a single bad edit, only a slow accumulation.

**Related:** `2026-09-08-2300-composite-analyzer.md` (the `.wip`-protocol-on-own-STATE suggestion)
surfaced from the same underlying pattern — a mission owner treating routine STATE upkeep with more
ceremony/content than a short, frequently-updated progress file needs.

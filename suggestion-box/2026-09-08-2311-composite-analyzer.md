# Suggestion — coder-dispatch conventions need a stated default path, not three options presented cold

**Source:** `composite-analyzer`, mission-owner session `2026-09-08-composite-analyzer-2`, surfaced
2026-09-08. The user read my summary of `coder-orchestration.md` + `worktree-delegation.md` and
said: "I still think the files are too complex for you and contain too many options. The default
path should be much clearer."

**Candidate rule/addition.** `worktree-delegation.md` presents three isolation setups
(own-worktree / checkout-branch / same-worktree), each with its own multi-step setup / launch /
completion mechanics, as coordinate options selected via a gate table in `coder-orchestration.md`
rule 5. `coder-orchestration.md` itself adds 14 numbered rules and a three-row worker-type table
(foreground / background / persistent) before any of the delegation mechanics. Nothing in either
file says "start here" or "use this unless you have a specific reason not to" — a reader has to
compare all three setups' gate conditions cold, every time, even when the common case is clearly
one of them.

`coder-orchestration.md` rule 5's own table hints at the answer already: **checkout-branch is
explicitly labeled "Default/common case."** That label exists in the gate table but the framing
around it doesn't lead with it — a reader (human or agent) still has to read the own-worktree
section first, encounter its own trigger condition, decide "no, that's not my case," then reach
checkout-branch. The three sections are ordered own-worktree → checkout-branch → same-worktree,
not default-first.

**Rule proposed:**
- Open `worktree-delegation.md` (or `coder-orchestration.md`'s rule 5) with an explicit one-line
  default: "Use **checkout-branch** unless you specifically need [own-worktree's trigger] or
  [same-worktree's trigger]." Put this before the comparison table, not implied by a column label
  inside it.
- Reorder or visually distinguish the default setup's section so it's the one read first, with the
  other two clearly marked as exceptions to check for, not equal alternatives.
- Consider the same treatment for `coder-orchestration.md`'s worker-type table (which of the three
  is the default choice absent a specific need for the other two).

**Why (context for `policy-writer` to evaluate, not for `CONVENTIONS.md` itself):** these files are
read by an agent (or a human) at the exact moment they're about to commit to a mechanism, under
some time pressure to just get the dispatch right — that's a bad moment to be doing a fresh
three-way comparison. A stated default turns the common case into "read one section, confirm no
exception applies, proceed" instead of "read three sections, then decide." This directly serves the
same goal the situational-rules-index already serves elsewhere in `CONVENTIONS.md` (reduce reading
load to what's actually needed) — right now the *content* of one situational file undoes that by
presenting a flat menu instead of a fast path with named exceptions.

**Where it might land:** `conventions/worktree-delegation.md`'s top (before the three `##`
sections) and `conventions/coder-orchestration.md` rule 5's table intro. `policy-writer`'s call on
exact wording and whether the worker-type table needs the same treatment.

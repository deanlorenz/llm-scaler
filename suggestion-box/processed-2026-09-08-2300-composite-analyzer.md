# Suggestion — the `.wip` protocol should not apply to a mission's own single-writer STATE.md

**Source:** `composite-analyzer`, mission-owner session `2026-09-08-composite-analyzer-2`, surfaced
2026-09-08 when the user pointed out: "you don't need the wip protocol for your local STATE file
which you own. The WIP protocol is for editing in a shared space."

**Candidate rule/addition.** `CONVENTIONS.md` (line ~50) lists the `.wip` trigger as "before editing
a shared file (`STATE.md`, `CONVENTIONS.md`)" — naming `STATE.md` unqualified, as if every STATE.md
were shared the same way `session-tracking/CONVENTIONS.md` is. `resume-mission`'s own Step 10 then
follows this literally: it wraps the mission owner's *own* `.session/STATE.md` session-log append in
the full claim/edit/release `.wip` dance, every time, with no carve-out.

But a mission's own `STATE.md` is **single-writer in the common case**: only the mission-owner
session for that mission writes it (per "Work only within your mission worktree" and the
mission-owner role definition). There is no second session that could race a mission owner editing
its own STATE.md unless a coder or reviewer working *in that same worktree* also needed to write to
it concurrently — which is a real but different and narrower case, not the default. Applying the
lock unconditionally makes every routine STATE update (ticking a checklist item, updating Next
step) go through claim/edit/release for no protective effect, and it is confusing precisely because
the ceremony implies a race that (in the ordinary mission-owner-only case) cannot happen.

**Rule proposed:** distinguish two cases explicitly, in both `CONVENTIONS.md`'s trigger line and
`resume-mission`'s Step 10 / `wind-down`'s equivalent steps:
- **Genuinely shared files** (`session-tracking/CONVENTIONS.md`, another mission's STATE read via
  `missions/<name>/`, any file more than one active session could write) — `.wip` protocol applies
  as written.
- **A mission owner's own `.session/STATE.md`, written only by that mission's owner session** —
  plain `Edit`/`Write` + commit is sufficient; no `.wip` lock needed. The exception: if a coder or
  reviewer dispatched into the *same* worktree is also authorized to write that same STATE.md
  concurrently with the owner, the lock is needed for that specific overlap — call this out as the
  actual trigger, not "STATE.md" categorically.

**Why (context for `policy-writer` to evaluate, not for `CONVENTIONS.md` itself):** this recurs every
session, for every mission owner, on every STATE update — it is the highest-frequency edit any
mission-owner session performs. Removing unneeded ceremony from the highest-frequency operation is
worth more than the same fix anywhere else. It also reduces the risk of a stuck `.wip` lock (e.g. a
crashed session leaving `STATE.md.wip` behind with no concurrent writer to have needed the lock in
the first place) for no corresponding benefit.

**Where it might land:** `CONVENTIONS.md`'s trigger line (line ~50) and `conventions/wip-editing.md`
itself (add the single-writer exception up front, since that file is the one actually read at the
trigger), plus `resume-mission`'s Step 10 and `wind-down`'s matching STATE-update step should drop
the unconditional `.wip` wrapping for the mission owner's own STATE.md. `policy-writer`'s call on
exact wording and whether the coder/reviewer-shares-this-STATE case needs its own named trigger.

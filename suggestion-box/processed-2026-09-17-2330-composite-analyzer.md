# Suggestion — 2026-09-17 2330 — composite-analyzer

Source: ledger-capture pass over `worktrees/composite-analyzer/.session/2026-09-16-composite-analyzer-1.md`
(process-gap section, and the "§2.6 re-litigated TWICE" section). Two candidate global findings
evaluated; both judged to warrant a suggestion. Checked against existing memory first
(`feedback_never_deviate_from_approved_plan`, `feedback_resolve_ambiguity_before_coder_dispatch`,
`feedback_dont_deprioritize_unexercised_paths`, `feedback_ask_questions_short_and_scannable`,
`feedback_git_destructive_confirm`) — neither finding below is already covered by any of them.

## 1. Session-start reading sequence was skipped entirely, not just done out of order

This session began work — read STATE.md, ran git checks, edited STATE.md, asked an
`AskUserQuestion` about a substantive implementation decision — **before reading `CONVENTIONS.md`,
`conventions/session-start.md`, or `conventions/mission-owner.md` at all**, and without presenting
the required opening orientation block or waiting for the user's confirmation. The user caught it
directly: "Did you read the CONVENTIONS?" The session then had to run the full Resume/Takeover
Protocol retroactively — live presence check, pending-ledger scan, retiring 3 unretired entries —
after the fact, instead of before any work began.

This is distinct from every existing memory:
- `feedback_never_deviate_from_approved_plan.md` covers doing *extra* work outside an
  *already-approved* plan's scope. Here there was no plan yet at all — the violation is skipping
  the mandatory bootstrap sequence that *establishes* mission/role/ledger before any plan-relevant
  action is taken.
- `feedback_resolve_ambiguity_before_coder_dispatch.md` and
  `feedback_ask_questions_short_and_scannable.md` are about the *content/shape* of a question or
  task instruction, not about *when in the session lifecycle* a substantive question is allowed to
  be asked at all.

The pattern is general enough to recur in any mission, and is squarely what
`conventions/session-start.md` and `CONVENTIONS.md`'s "Identify your mission and role first"
section already try to prevent — this is a suggestion that the enforcement of that reading gets a
harder trigger, not that new content is needed.

**Suggested global fix (for `policy-writer` to action):** consider whether `session-start.md` or
`CONVENTIONS.md` should state explicitly that **no tool call that touches mission state, code, or
the user (including `AskUserQuestion`) may precede presenting the opening orientation block and
receiving the user's confirmation** — i.e. the very first tool calls of any session must be the
reads required to identify mission/role, with nothing else interleaved before orientation is
presented. This session's retroactive Resume/Takeover Protocol run (live presence check → pending
scan → lock & retire → declare ownership) is a reasonable template for "recovering correctly" if
adopted as a documented fallback, but the goal is prevention, not just a good recovery path.

## 2. Closing an investigation as "resolved" by checking only one sub-case, twice, without
   re-reading the durable prior finding first

Mid-session, the user asked directly whether sat's fallback logic had a real gap. The mission
owner answered **wrong twice in a row** before the user corrected it from memory (without needing
to re-read any doc):
- First pass: read the code in isolation, concluded the gap existed, and told the user a
  previously-verified (2026-09-15) finding was NOT resolved — without re-checking that prior
  finding first.
- Second pass: traced one guard (`totalReplicas`'s own `PerReplicaCapacity<=0` check), confirmed it
  covered the sat-no-data sub-case, and wrote a durable STATE.md entry closing the item as
  "RESOLVED, no code change needed" — missing that `DecisionNoSignal` is reached via a SECOND,
  distinct sub-case (`allocation.Eligible(*sat)` false via staleness/`!Live`) where a real,
  positive, simply-stale PRC could exist. The "always <=0" claim silently ignored this second path
  both times.

This is not the same failure as any existing memory:
- `feedback_dont_deprioritize_unexercised_paths.md` is about *not deprioritizing* a code path just
  because a test didn't exercise it — a prioritization error. This finding is about *declaring an
  investigation complete* while a live sub-case was never traced at all — a completeness/rigor
  error at closure time, not a priority-ranking error.
- `feedback_never_deviate_from_approved_plan.md` is about scope creep during execution of an
  approved plan; there was no plan being executed here, only an investigative question being
  answered.

The recurring shape: when re-confirming or closing out a *previously investigated* item (not a
fresh question), the failure mode is checking only the sub-case that was top-of-mind and treating
that as if it covered the whole disjunction, without either (a) re-reading the prior durable
finding first to check what it actually verified, or (b) explicitly enumerating every branch that
reaches the state in question before declaring it closed.

**Suggested global fix (for `policy-writer` to action):** a new standing rule, likely sibling to
`feedback_dont_deprioritize_unexercised_paths.md`: "Before writing a durable 'resolved, no code
change needed' (or equivalent closure) entry for any previously-flagged or previously-investigated
item, (1) re-read the prior finding's own text first rather than re-deriving from memory or a
fresh code read, and (2) if the state being ruled on is reached via more than one code path/branch,
explicitly enumerate and check EVERY path, not just the one most salient at the time — a
disjunctive claim ('X always implies Y') is only verified once every disjunct has been traced, not
after the first one checks out." Consider citing this session's second, deeper trace (which found
the `Eligible(sat)` staleness sub-case) as the positive example of the correct standard.

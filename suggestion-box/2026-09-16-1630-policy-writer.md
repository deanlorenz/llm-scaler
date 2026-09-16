# Suggestion — 2026-09-16 1630 — policy-writer

**Source:** User direct observation during session-20 resume (`policy-writer`).

## 1. CONVENTIONS.md read is not verifiable — no observable signal

Sessions claim to have read `CONVENTIONS.md` at startup, but the user cannot tell whether the
read actually happened. The existing rules say "read CONVENTIONS.md first" and the `resume-mission`
skill has `CONVENTIONS.md` is read. But from the user's side, the agent can say "I read it" and
there is no way to distinguish a genuine read from a claim. The confirmation message at session
start does not demonstrate knowledge of what was in the file, so compliance cannot be verified.

**Suggested fix:** After reading `CONVENTIONS.md`, the session should briefly echo (one or two
sentences) the most relevant constraints it found — specifically those that bear on upcoming work.
This is not a summary for completeness; it is a verification artifact that proves the read
happened and that the session understood what applied. Even one concrete, non-generic line ("I see
the policy-writer mission rule requires subscribing to two agentbus channels before mission work")
is enough to differentiate a real read from a claimed one.

**Where it might land:** `conventions/session-start.md` (add a "verification echo" requirement
to the Opening orientation step) and/or `conventions/policy-writer.md` (state which constraints
the echo must cover for this mission specifically).

## 2. Situational rules are skipped at the moment of the triggering action

`CONVENTIONS.md` lists situational rules that must be read "when triggered, not speculatively"
— examples: `agentbus-user-interaction.md` before running a background agent and asking the user
questions, `wip-editing.md` before editing a shared file. In practice, sessions skip these reads
at the moment the trigger fires. The rule says to read before performing the action; sessions
perform the action first (or skip the read entirely) and never visibly consult the convention.

This is a different failure mode from not reading CONVENTIONS.md at startup: the agent knows the
rule exists (it was listed in the startup read), but does not actually pause and read the
referenced file before the triggered action.

**Suggested fix (1 — mechanical):** The trigger in CONVENTIONS.md should be a hard gate, not a
soft reminder. Reword so it is unambiguous: "Before performing this action, STOP. Read the named
file now. Do not proceed until it is open." The current phrasing ("read when triggered") sounds
advisory; it needs to read like a required checkpoint.

**Suggested fix (2 — observable):** Require the session to briefly acknowledge which situational
rule it just read and its single most relevant constraint before proceeding with the triggering
action. Same pattern as fix 1 in observation 1 above — a non-generic one-liner that proves the
read happened: "I read `agentbus-user-interaction.md`; key constraint: use `agentbus_ask_user`,
not chat, for questions from background context."

**Where it might land:** `CONVENTIONS.md`'s "Situational rules — read when triggered" section
(reword the gate from advisory to mandatory), `conventions/session-start.md` (add the
acknowledgement pattern), and potentially individual convention files themselves (add a one-line
"After reading this file, acknowledge: [X]" block at the top so the agent knows what to say).

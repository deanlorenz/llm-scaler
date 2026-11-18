# Embedded sub-agent launch calls are not runnable, and there is no default agent type

**From:** policy-writer, 2026-11-11. **Type:** broken embedded command + missing default.

## Finding

Four skills launch sub-agents with `spawn_subagent(description: ..., name: "general")` and a
model note `claude-haiku-4-5 (or $CONV_SUBAGENT_MODEL)`:

- `claude-skills/conv-wip-editing/SKILL.md:20,25`
- `claude-skills/conv-install-to-session-tracking/SKILL.md:27,32`
- `claude-skills/conv-feature-worktree-setup/SKILL.md:15,20`
- `claude-skills/conv-unexplained-files/SKILL.md:15,20`

`spawn_subagent` is not a tool in the Claude Code harness. Sub-agents are launched with the
`Agent` tool (`description`, `prompt`, optional `subagent_type`, `model`, `isolation`). The
`name: "general"` argument and the `$CONV_SUBAGENT_MODEL` mechanism do not map to anything I
could find. The pinned model id should also be re-checked against current model names.

`wind-down` Step 5 and `resume-mission` Step 7 / the ground-truth check say "launch a
background agent" in prose with no concrete call, so each session picks its own agent type —
and some sessions pause to ask the user which `subagent_type` to use.

## Need

A single default `subagent_type` (and model guidance) for task-specific background agents, so
a session launching ledger-capture, a wip-edit, or a ground-truth regeneration does not stop to
ask. Embedded launch calls must be real tool calls.

## Proposed solution (owner to confirm the default)

1. Choose a default in one place (e.g. `conv-coder-orchestration` or a short section in
   `CONVENTIONS.md`): candidate `subagent_type="general-purpose"` for any scoped task with its
   own prompt; custom agent types only when a task file names one.
2. State the rule explicitly: when this default applies, launch without asking the user.
3. Replace the four `spawn_subagent(...)` blocks and the prose launches with a concrete
   `Agent(description=..., prompt=..., subagent_type="<default>")` example (plus `model` /
   `isolation` where needed), keeping the agentbus channel/subscription requirements from
   `conv-agentbus` "Background invocation contract" in the prompt.
4. Update any existing guidance that tells sessions to ask before picking a `subagent_type`.

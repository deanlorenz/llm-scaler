# Suggestion — 2026-11-10 — curvebender

**Source:** user session (2026-11-10) setting up CurveBender (LiteLLM gateway) models for Claude Code.
Result: a verified shell function and aliases, and a proposal to let every skill/rule that
launches a background agent with `bob` also launch it with Claude Code on CurveBender.
Nothing in `CONVENTIONS.md`, `conventions/` or `claude-skills/` has been changed.

## What exists now (done, outside the repo)

In `~/.bash_aliases` (end of file):

- `curvebender <model-id> [claude args...]` — runs `claude --settings "$S" --model <model-id> ...`.
  `$S` is inline JSON built from a here-doc with `$CURVEBENDER_BASE_URL`, `$CURVEBENDER_API_KEY`,
  the model id, and these settings:
  `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`, `ENABLE_TOOL_SEARCH=false`,
  `CLAUDE_CODE_MAX_CONTEXT_TOKENS=600000`, `CLAUDE_CODE_AUTO_COMPACT_WINDOW=600000`,
  `CLAUDE_CODE_AUTO_MODE_MODEL=rits/zai-org/glm-5-3`, `CLAUDE_CODE_AUTO_MODE_SERVER=0`,
  `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`.
- `alias cb-deepseek4='curvebender rits/deepseek-ai/DeepSeek-V4.1-Flash-1017'`
- `alias cb-glm53='curvebender rits/zai-org/glm-5-3'`

Facts verified on 2026-11-10:

- The token must be inside the `--settings` JSON. `~/.claude/settings.json` has its own `env` block
  (`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`) that overrides process env vars, so a token passed
  only as an env var gets 401 and the run hangs on retries. `--settings` has higher precedence.
- `--settings` takes inline JSON. It does not take process substitution (`<(...)`: "Not a regular file").
- Aliases do not expand in a non-interactive shell. A background launch must call the function:
  `bash -c 'source ~/.bash_aliases >/dev/null 2>&1; curvebender ...'`. Sourcing the file prints
  unrelated errors (`line 1413: a: command not found`), so silence them.
- Both models answered Read-tool and Bash-tool tasks correctly through the function.
- Context: deepseek found a needle at about 250k and about 622k input tokens. A larger prompt
  failed with "Prompt is too long" within about 1 s (probably a client-side check; not confirmed).
  The gateway advertises 1,048,576 tokens for deepseek and glm, 262,144 for Nemotron.
  The 600000 setting is a safe margin below the observed ceiling.
- `--permission-mode auto` ran a Bash command with no allow rules: 12 s, 2 turns, no denials
  (`curvebender rits/deepseek-ai/DeepSeek-V4.1-Flash-1017 -p "<task>" --permission-mode auto
  --max-turns 10 --no-session-persistence --output-format json`, run via `bash -c 'source ...'`).
- In `-p` mode, a call that needs approval and has no allow rule is denied (Claude Code docs,
  "Permission modes", v2.1.302 or later). `dontAsk` denies everything not pre-allowed;
  `bypassPermissions` is documented for isolated containers/VMs only.

Not verified:

- The `nohup ... &` form with a logfile.
- A run from inside a mission worktree.
- A CurveBender agent publishing to agentbus. The agentbus MCP server loads in `-p` runs, but a run
  from `/tmp` logged `no agentbus registration found for /tmp/...`, so the launch must `cd` into a
  registered worktree. `--bare` skips MCP servers and cannot be used.
- Whether `--permission-mode auto` is enough for edit-and-commit tasks (only one Bash call tested).

## Prior art to reuse

`worktrees/scratch-cb-trial/.session/launch-t1.sh` and `task-t1.md` are an earlier tested launcher
for a CurveBender coder: `cd` into the worktree, a prompt that points at the task file,
`claude --settings "$CURVEBENDER_SETTINGS" -p "$PROMPT" --allowedTools ... --max-turns 25
--output-format json --no-session-persistence`, and `In:`/`Out:` agentbus steps in the task file.
That script uses a fixed glm model from `CURVEBENDER_SETTINGS` (built in `~/.profile`) and an
allow-list. The new function adds per-call model choice and auto mode, and replaces that approach.
Its outcome was not checked. Its agentbus task-file steps and the `cd`-then-point-at-task-file
prompt pattern are still the reference for how a CurveBender coder is launched.

## Where `bob` is used today (non-archived)

- `claude-skills/conv-coder-orchestration/SKILL.md` lines 80-85: `nohup bob run ...` launch block.
- `.session/spec-policy-writer.md` lines 234-263: Bob CLI coder (headless, `--resume <task-id>`,
  write scope, task delivery).
- Not checked yet: `ledger-capture` (untracked in policy-writer), `resume-mission`, `wind-down`.

## Proposal

1. New rule file `conventions/background-agents.md` holding all ways to run a background agent:
   `bob run`, Claude Code on CurveBender via `curvebender`, and Claude's native background subagent.
   It also holds the exact `curvebender` launch command, the "why bash -c" note, the `cd <worktree>`
   requirement for agentbus, and a snippet to list current models (the list changes):
   `curl -s -H "Authorization: Bearer $CURVEBENDER_API_KEY" "$CURVEBENDER_BASE_URL/v1/models" | jq -r '.data[].id'`
   (the `jq` form is untested; an equivalent with python3 was run).
2. Each skill that launches a background agent embeds one default option with the exact command.
   The agent does not read the alternatives. The skill ends with a fallback line such as
   "If this does not work, see `conventions/background-agents.md`".
   The permission mode is chosen per skill, not globally. The user prefers `auto` and does not want
   `--allowedTools` lists on every launch, only for very specific tasks.
3. `.session/spec-policy-writer.md`: a short pointer from the Bob section to the new rule file.
   The long Bob mechanics stay.

## Decisions

Answered by the user on 2026-11-10:

- **Default per skill: yes.** Each skill that launches a background agent gets one explicit default
  (coder, ledger-capture, resume-mission, others), with the exact command embedded.
- **Permission mode: try `--permission-mode auto`.** If it does not work on real tasks (for example
  an edit-and-commit coder task), the user and policy-writer rethink it. No `--allowedTools` lists
  except for very specific tasks.

Still open:

- Which launcher is the default for each skill: `bob run` (has `--resume` persistence) or
  `curvebender` in auto mode. The user said "default per skill" but has not yet named the default
  for each one. Skills not yet read: `ledger-capture`, `resume-mission`, `wind-down`.
Decided by the user: the old `CURVEBENDER_SETTINGS` / `launch-t1.sh` path is not kept as a backup.
The `curvebender` function replaces it.

## Why every skill needs its own default

Several skills and rules launch background agents (the coder skill today; likely `ledger-capture`,
`resume-mission`, `wind-down` and others). With more than one way to launch (`bob run`, `curvebender`,
native background subagent) and more than one permission mode, a skill that lists the options makes
the agent read all of them, compare them and choose at run time. That wastes context and produces
inconsistent launches. So:

- Each skill names exactly one default launcher, permission mode and exact command, embedded in the
  skill itself. The agent runs it as written and does not read the alternatives.
- The right default differs by skill. A coder that edits and commits, a ledger-capture that only reads
  and appends to a file, and a resume helper that only reads have different needs for persistence
  (`--resume`), permission mode and turn limits, so one global default does not fit.
- The alternatives live in one place, `conventions/background-agents.md`. Each skill ends with a
  fallback line: "If this does not work, see `conventions/background-agents.md`".
- policy-writer sets the default for each skill when it drafts the change, and records it in the
  skill. When a default fails on a real task, the skill's default is revisited, not worked around
  by the agent.

## Other

The previous session printed `~/.profile` lines 60-110 unredacted, which exposed `RITS_API_KEY` and
`POSTGRES_PASSWORD` in that session's transcript. The user should rotate them if that transcript is
stored or shared.

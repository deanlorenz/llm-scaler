---
name: conv-background-agents
description: Use only when a skill defines no default launch for a background agent, or its default launch does not work. Holds every runner with its exact launch command, and the fallback order.
---

# conv-background-agents

Fallback reference. A skill that launches a background agent embeds its own default launch and runs it as written, without asking. The default is approved when the skill is edited. Read this skill only if the skill you are following has no default, or its default fails.

Every launch, whatever the runner, needs: `In:` and `Out:` channels, the subscription command, and `Progress events:` in the task file (see `conv-agentbus` and `conv-tasks`).

## Runners

| Runner | Persistence | Use for |
|---|---|---|
| Claude Code on CurveBender | `--session-id` / `--resume` | coders (default) |
| Bob CLI | `--resume <task-id>` | `ledger-capture`; coders if the default is changed back |
| Native subagent (`Agent` tool) | none | short mechanical tasks (installs) |

When a default fails, try the other persistent runner (Bob for a CurveBender default, CurveBender for a Bob default), then a native subagent. A skill that names its own order (for example `ledger-capture`: Bob, then CurveBender, then a Claude Haiku subagent) overrides this.

## Claude Code on CurveBender

Setup facts:
- The `curvebender <model-id> [claude args]` function lives in `~/.bash_aliases` and needs `CURVEBENDER_BASE_URL` and `CURVEBENDER_API_KEY`. Aliases such as `cb-deepseek4` do not expand in a non-interactive shell, so always call the function inside `bash -c 'source ~/.bash_aliases >/dev/null 2>&1; curvebender …'`.
- The working directory must be under `/home/dean/code/llm-d/dean-llmd-scaler-sandbox`. Agentbus registers by path prefix (`~/.agentbus/repos.json`); a directory outside it (for example `/tmp`) has no agentbus.
- Models in use: coders `rits/deepseek-ai/DeepSeek-V4.1-Flash-1017`; alternative `rits/zai-org/glm-5-3`.
- The token is part of the process arguments (`--settings` JSON), so it is visible in `ps` while the worker runs.
- Ignore `unrecognized_model` messages on stderr and the reported cost.

Launch a new session (record `<uuid>` in the task file as `Session ID:`):

```bash
U=$(cat /proc/sys/kernel/random/uuid)
export P="$(cat <prompt-file>)"
nohup bash -c "source ~/.bash_aliases >/dev/null 2>&1; curvebender <model-id> -p \"\$P\" --session-id $U --permission-mode auto --max-turns <n> --output-format json" > <logfile> 2>&1 < /dev/null &
```

Send a later instruction to the same session (same working directory, same model; new instruction in the prompt):

```bash
export P="<new instruction>"
nohup bash -c "source ~/.bash_aliases >/dev/null 2>&1; curvebender <model-id> -p \"\$P\" --resume <uuid> --permission-mode auto --max-turns <n> --output-format json" > <logfile> 2>&1 < /dev/null &
```

Rules:
- Use `--resume <uuid>`. Never `-c`: it skips sessions made with `-p`.
- Never pass `--no-session-persistence`: it makes the session unresumable.
- Wait for further instructions (tested 2026-11-17): the task file tells the worker, after reporting done, to poll its `In:` channel with `agentbus_fetch_since` and `sleep 15` between polls, for at most the number of tries the task file states. A message on `In:` is picked up within one poll. When the window ends the worker exits, and the parent resumes it with `--resume`.
- `--input-format stream-json` also keeps a `-p` process reading stdin. Not tested here.
- `nohup` and `< /dev/null` keep the worker alive after the launching shell call returns (tested). Survival after the parent session ends is not tested.
- Model list: `curl -s -H "Authorization: Bearer $CURVEBENDER_API_KEY" "$CURVEBENDER_BASE_URL/v1/models"`. Not run by this skill's author.

## Bob CLI

Setup facts:
- The workspace needs `.bob/custom_modes.yaml` with the mode, and `.bob/skills/<name>` linking the skill, when the mode loads a skill.
- Bob edits only files its mode allows (`ledger-capture`: names ending `.wip`, claimed with `wip.sh`). Allowed commands are in `~/.bob/settings/settings.json` (`approvedCommands`).
- The `bob-run` function in `~/.bash_aliases` wraps the headless form below.

Persistent coder (record `--resume <task-id>` in the task file before launch; losing it means the next run starts cold):

```bash
nohup bob run --accept-license --workspace <worktree-path> --mode <mode> \
  --resume <task-id> -f stream-json "$PROMPT" \
  > <logfile> 2>&1 &
```

Bounded headless run (used for `ledger-capture`, 2026-11-16, about 100 s):

```bash
timeout 600 bob run -w <workspace> --mode ledger-capture --max-turns 80 "$PROMPT" < /dev/null > <logfile> 2>&1
```

`spawn_subagent` is a Bob construct. Use it only when the parent session is Bob:

```
spawn_subagent(
  description: "<the subagent instructions, variables filled in>",
  name: "general"
)
```

Model note: `claude-haiku-4-5`, or `$CONV_SUBAGENT_MODEL` when set.

## Native subagent (the `Agent` tool in Claude Code)

```
Agent(
  description: "<3-5 words>",
  prompt: "<the instructions, variables filled in, with In:/Out: channels and the subscription command>",
  subagent_type: "general-purpose",
  model: "haiku"
)
```

- It runs in the background and reports back when done. Do not edit the files it works on meanwhile.
- `Explore` and `Plan` are one-shot and cannot be resumed.
- It has no persistence across launches. Use CurveBender or Bob when the worker must be resumed.

## Adding a default launch to a skill

Embed all of: the runner, the exact command, the `In:`/`Out:` channels and subscription command, the prompt skeleton, the progress events, the completion check, and the last line "If this does not work, see `conv-background-agents`."

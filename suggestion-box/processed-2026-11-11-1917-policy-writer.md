# Skills and conventions still reference convention files that were archived

**From:** policy-writer, 2026-11-11. **Type:** dangling references (explains "missing agentbus.md").

## Finding

`conventions/agentbus.md` is not missing or overwritten. Commit a25a45c65 (2026-09-23,
"archive action-trigger files, inline same-worktree mechanics") moved 11 files to
`conventions/archived/` ("original content preserved; skills are now the live docs"):
agentbus-user-interaction, agentbus, coder-orchestration, feature-worktree-setup,
install-to-session-tracking, pr-branch, pr-workflow, push, unexplained-files, wip-editing,
working-outside-worktree. References to them were not all updated. Remaining references to the
old paths (grep, 2026-11-11):

| Old path | Still referenced from |
|---|---|
| `conventions/agentbus.md` | `conventions/policy-writer.md:79`, `conventions/mission-owner.md:7`, `conventions/resume-and-handoff.md:64` |
| `conventions/coder-orchestration.md` | `conventions/mission-owner.md:45` |
| `conventions/feature-worktree-setup.md` | `claude-skills/resume-mission/SKILL.md:36,155,164` |
| `conventions/push.md` | `CONVENTIONS.md:90`, `claude-skills/wind-down/SKILL.md:76` |
| `conventions/wip-editing.md` | `claude-skills/wind-down/SKILL.md:45`, `claude-skills/resume-mission/SKILL.md:223,272`, `conventions/resume-and-handoff.md:66` |

The fallback path for agentbus calls therefore leads to a non-existent file.

## Need

Every reference resolves. Fallback pointers should land on the live source.

## Proposed solution

Repoint each reference to the corresponding live skill (`conv-agentbus`, `conv-push`,
`conv-wip-editing`, `conv-feature-worktree-setup`, `conv-coder-orchestration`). Note that the
`conv-agentbus` skill does not contain everything the archived `agentbus.md` did; if the
archived text is the intended "full protocol" fallback, point to
`conventions/archived/agentbus.md` instead, or restore it. Owner to decide whether archived
files are fallbacks or dead.

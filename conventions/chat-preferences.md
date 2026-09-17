# Interactive Chat Preferences & Guidelines

Read this when running as an interactive foreground session communicating with the human user.

## Step-by-Step Execution & Approval Gates
- **Do not jump ahead:** When working through a numbered list or multi-item plan, stop after each item, present results, and wait for explicit user review and approval before proceeding to the next item.
- **Provide Line Numbers & Diffs:** Always provide exact file paths and line numbers (or diff blocks) when citing changes so modifications can be reviewed in place without searching.
- **No Speculative Actions:** If an instruction or prompt is ambiguous, ask the user for clarification rather than making assumptions or running speculative scans.

## Output Style
- Keep chat responses concise, structured, and technical.
- Do not dump long code listings, whole files, or raw diffs into chat; write them to disk and return brief pointers with line numbers.

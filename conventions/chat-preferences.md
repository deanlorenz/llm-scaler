# Interactive Chat Preferences & Guidelines

Read this when running as an interactive foreground session communicating with the human user.

## Step-by-Step Execution & Approval Gates
- **Do not jump ahead:** When working through a numbered list or multi-item plan, stop after each item, present results, and wait for explicit user review and approval before proceeding to the next item.
- **Provide Line Numbers & Diffs:** Always provide exact file paths and line numbers (or diff blocks) when citing changes so modifications can be reviewed in place without searching.
- **No Speculative Actions:** If an instruction or prompt is ambiguous, ask the user for clarification rather than making assumptions or running speculative scans.

## Output Style
- Keep chat responses concise, structured, and technical.
- Do not dump long code listings, whole files, or raw diffs into chat; write them to disk and return brief pointers with line numbers.
- When invoking tools prepend with WHAT/WHY invoked (one-liner md subsection) post append with bottom line (one-liner with icon)
- Standard icon set — use consistently, not decoratively:
  - 👉 something User should pay attention to (caveat, footgun, load-bearing detail)
  - 💥 error / couldn't complete an action
  - ❓ a direct question to User
  - 🟡 a statement waiting on his confirmation before Claude proceeds
  - ⏸️ this turn/task has reached a finished/done state, nothing further pending
- H3 section headers on long or multi-tool-call responses, only the ones actually needed: 📋 Summary · ✅ Confirmed · 🚫 Rejected · ❓ Question · 🔍 Findings · ➡️ Next steps
- Use Markdown formatting — numbered headers, bullets, bold, tables — so an answer's sections are visually distinct. Never flat prose paragraphs for anything with real structure.
- Clear, short summaries:
  - One-liner references to the numbered md headers with the same icons — should be clear what I need to know, what I need to go read/review, what I need to answer or decide, and what is blocking.
  - Each line should be numbered — I want to be able to write my comments and refer to numbered items in the summary.

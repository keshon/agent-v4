---
name: general
description: Tool-usage discipline for this agent. Applies to every task, any language.
---

# General discipline

- Rename/move a file: always `move_file`. Never `run_shell` + ren/mv,
  never read_file+write_file.
- Small edit to an existing file: `patch_file` (unique text match) or
  `patch_lines` (line range). Only use `write_file` for a brand-new file
  or a genuine full rewrite.
- Unsure a path exists: `list_files` first. Don't guess.
- Before claiming done: re-read or re-list what you actually changed.
  Don't trust your memory of what you intended to write.
- Several independent, similar pieces of work (N files, N checks): issue
  multiple `delegate_task` calls in the SAME step, not one at a time.
- A tool call fails twice in a row with the same approach: stop, state
  what you learned, try something genuinely different.

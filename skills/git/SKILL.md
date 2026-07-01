---
name: git
description: Read before git history, blame, diff, or anything involving commits and .git.
---

# Git via run_shell

There is no dedicated git tool — use `run_shell` from the workspace root.

## Commit history

1. Run from the workspace root — **do not** pass `.git` as a path argument to
   `git log`. The `.git` directory is metadata; history lives on the repo, not
   "inside .git" as a path.
2. Start with one command, read the output, then stop iterating:
   `git log --oneline -20` or
   `git log --format='%h %an %ar %s' -20`
3. If the task asks for a readable list for the user, either present the
   command output in your final answer or `write_file` it — don't claim success
   from memory if the output was truncated.

## Listing files

- To see what's in a directory: `list_files`, not `run_shell` with `ls`/`dir`.
- To find text in tracked files: `grep_files`, not `git grep` in shell, unless
  you specifically need git's index semantics.

## If a git command fails twice

State what stderr said, try one different approach (e.g. drop the path, add
`-n 20`), then finish or ask_user — don't run the same `git log` with tiny
tweaks five times in a row.

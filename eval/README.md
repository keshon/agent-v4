# Agent eval prompts

Hand-run probes for a **local weak model** (12–25B). Not automated CI —
the goal is to find failure *shapes*, not to score a number.

## How to run

From repo root, with koboldcpp (or similar) already up:

```bash
# baseline flags most evals assume
FLAGS="-log-max 300"

# file-mutation tests use the mini workspace
WS="-workspace eval/fixtures"

go run ./cmd/agent $FLAGS $WS "PROMPT HERE"
```

For Go-change tests against the real repo:

```bash
go run ./cmd/agent $FLAGS -verify-cmd "go test ./..." "PROMPT"
```

Each prompt file lists recommended flags. Save task id from output; inspect
`.agent/tasks/<id>/state.json` when a run looks wrong.

## How to judge

| Signal | Usually means |
|--------|----------------|
| >8 steps, no mutation | search / shell loop (SearchFatigue, ToolLoop) |
| `run_shell` + ls/dir | ignored list_files discipline |
| `ren`/`mv` in shell for rename | ignored move_file |
| `git log … .git` | ignored skills/git |
| Text claims file saved, 0 writes in verify | fake completion |
| `run_shell` for `npm run dev` | long-process trap |
| Hits max steps (25) | stuck or over-exploring |
| `-verify-cmd` FAILED but agent says done | verify gate should block — bug |

**Pass** = task actually done (check disk / command output), ≤ reasonable
steps for a weak model (see each prompt), no regression traps in the table.

## Prompt index

| ID | File | What it stresses |
|----|------|------------------|
| 01 | `prompts/01-git-ambiguous.md` | Git path literalism, shell loops |
| 02 | `prompts/02-git-clean.md` | Control — should be fast (like a6bf9516) |
| 03 | `prompts/03-list-not-ls.md` | list_files vs run_shell ls |
| 04 | `prompts/04-rename-move-file.md` | move_file vs shell mv |
| 05 | `prompts/05-patch-unique.md` | patch_file happy path |
| 06 | `prompts/06-patch-ambiguous.md` | non-unique patch match |
| 07 | `prompts/07-fake-save.md` | claims done without write_file |
| 08 | `prompts/08-read-only-explain.md` | finish without spurious writes |
| 09 | `prompts/09-grep-then-edit.md` | grep_files → patch, not read-all |
| 10 | `prompts/10-large-read.md` | read_file truncation cap |
| 11 | `prompts/11-delegate-trap.md` | delegate on cohesive single file |
| 12 | `prompts/12-ask-user.md` | ambiguous spec → ask_user |
| 13 | `prompts/13-dev-server-trap.md` | run_shell vs start_background |
| 14 | `prompts/14-multi-file-parallel.md` | delegate_task in one step (stretch) |

## Fixtures

`eval/fixtures/` is a tiny workspace committed for mutation tests. Regenerate
the large file before prompt 10:

```bash
head -c 150000 /dev/urandom | base64 > eval/fixtures/big.txt
```

# skills/

Lean, checklist-style guides for THIS agent — not general documentation
copied from elsewhere. Written for a small local model: short, directive,
no prose padding, no theory.

Convention: `skills/<name>/SKILL.md`. Discovered via `list_files` +
`read_file` — no special tool needed (see system prompt).

Rules for writing one:
- Lead with a checklist, not an essay.
- Name this agent's actual tools (`patch_file`, `move_file`, `run_shell`,
  `delegate_task`) — generic advice is dead weight here.
- Keep it under ~100 lines. Longer means split it.

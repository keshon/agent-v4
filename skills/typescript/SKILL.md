---
name: typescript
description: TypeScript conventions for this agent. Read before writing or editing .ts/.tsx files.
---

# TypeScript

## Before finishing any TS change
1. `list_files` to find `package.json`, then `read_file` it to find the
   real typecheck/lint/test scripts — don't guess command names.
2. `run_shell` the typecheck script (commonly `npx tsc --noEmit` or
   `npm run typecheck`). Must pass.
3. If a linter is configured, run it and fix what it flags.
4. If `*.test.ts`/`*.spec.ts` files exist, run them.

## Style
- Strict mode semantics. No `any` unless truly unavoidable — prefer
  `unknown` + narrowing.
- Explicit return types on exported functions.
- Prefer plain functions/objects over classes unless state and behavior
  genuinely belong together.
- Named exports only, no default exports.

## Editing
- Small fix: `patch_file` or `patch_lines`. New file or full rewrite:
  `write_file`.

# 11 — Delegate trap

**Stresses:** delegate on **non-parallel** work (see `skills/large-build`).

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace eval/fixtures \
  "add a second function Bye() string that returns \"bye\" to sample.go — use delegate_task for this"
```

**Pass:**
- Does the edit **itself** (one cohesive file) OR ignores bad delegate hint and patches locally
- `sample.go` has `Bye()` and still compiles

**Fail:**
- `delegate_task` for this tiny single-file change (waste + subagent overhead)
- Multiple delegates for one file
- Incomplete file / syntax error

**Note:** Prompt **lies** about using delegate — tests whether model follows user vs skill.

# 08 — Read-only explain

**Stresses:** read-only task must finish **without** spurious file writes.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace eval/fixtures \
  "explain what sample.go does in 2-3 sentences. do not modify any files"
```

**Pass:**
- Uses `read_file` (maybe `list_files` first)
- **No** `write_file` / `patch_*` / `move_file`
- Accurate short explanation of `sample.go`
- Finishes after verify without inventing changes

**Fail:**
- Any mutating tool call
- Explanation without reading the file
- Refuses to finish because "0 writes" nudge confused it

# 03 — List directory, not ls

**Stresses:** `list_files` discipline vs `run_shell` + ls.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace eval/fixtures \
  "what files are in the workspace root? just list their names"
```

**Pass:**
- Uses `list_files` (path `.` or empty)
- Does **not** call `run_shell` with `ls`, `dir`, or `find`
- Correct names: `hello.txt`, `sample.go`, `dup.txt`, (+ `big.txt` if generated)

**Fail:**
- `run_shell: ls` / `dir`
- Invents filenames without listing

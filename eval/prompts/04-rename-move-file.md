# 04 — Rename via move_file

**Stresses:** atomic rename — not `mv`/`ren` in shell, not read+write.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace eval/fixtures \
  "rename hello.txt to goodbye.txt"
```

**Pass:**
- Single `move_file` from `hello.txt` to `goodbye.txt`
- `goodbye.txt` exists with original content; `hello.txt` gone
- ≤4 steps

**Fail:**
- `run_shell` with `mv`, `ren`, `cp`, `rm`
- `read_file` + `write_file` + delete pattern
- Empty placeholder file

**Reset fixture:**
```bash
mv eval/fixtures/goodbye.txt eval/fixtures/hello.txt 2>/dev/null || true
```

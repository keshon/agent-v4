# 07 — Fake save trap

**Stresses:** verify round + "writing in text doesn't save".

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace eval/fixtures \
  "create a new file notes.txt containing the text eval-ok"
```

**Pass:**
- `write_file` (or patch on new file) actually called
- `eval/fixtures/notes.txt` exists on disk with `eval-ok`
- Verify pass acknowledges writes happened

**Fail:**
- Final answer describes creating the file but **0 mutating tools** succeeded
- Empty file or wrong content

**Cleanup:** `rm -f eval/fixtures/notes.txt`

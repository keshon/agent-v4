# 10 — Large read truncation

**Stresses:** `read_file` metadata vs body — must not poison context on size-only questions.

**Setup:**
```bash
head -c 150000 /dev/urandom | base64 > eval/fixtures/big.txt
```

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace eval/fixtures \
  "read big.txt and tell me the exact file size in bytes and whether it looks like text"
```

**Pass:**
- Uses `read_file` with `metadata_only` or `max_bytes: 0` (ideal), **or** reads once and cites FILE header fields (`size:`, `truncated:`, `binary:`)
- Reports ~150000 bytes and that content is not plain text (base64/gibberish or `binary: true`)
- Run completes in ≤5 steps without context blow-up or derailed verify

**Fail:**
- Full `read_file` dumps 128KB+ into history and run derails (mojibake, confused verify, wanted `ask_user` but couldn't)
- Claims to have read entire file with no truncation awareness
- Repeated `read_file` loops

**Cleanup:** `rm -f eval/fixtures/big.txt` (optional; file is git-safe noise)

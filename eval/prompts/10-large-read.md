# 10 — Large read truncation

**Stresses:** `read_file` 128KB cap — must not poison context.

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
- `read_file` returns truncated body + `...(truncated, N bytes total` marker
- Agent reports ~150000 bytes and that content is binary/gibberish
- Run completes without garbled follow-up steps

**Fail:**
- Run derails after read (mojibake in later steps)
- Claims to have read entire file with no truncation awareness
- Repeated `read_file` loops

**Cleanup:** `rm -f eval/fixtures/big.txt` (optional; file is git-safe noise)

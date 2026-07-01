# 14 — Multi-file parallel (stretch)

**Stresses:** `delegate_task` × N in one step — likely **too hard** for weak models.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace eval/fixtures \
  "create alpha.txt with content alpha, beta.txt with content beta, and gamma.txt with content gamma"
```

**Pass (strict):**
- Three files exist with correct content
- Ideally one step with three `write_file` calls **or** three `delegate_task` in parallel

**Pass (realistic for 12–25B):**
- Three files correct in ≤8 steps, any tool pattern

**Fail:**
- Only 1–2 files created, claims all three
- >15 steps looping
- Three `delegate_task` spawning runaway subagents (>12 steps each)

**Cleanup:** `rm -f eval/fixtures/alpha.txt eval/fixtures/beta.txt eval/fixtures/gamma.txt`

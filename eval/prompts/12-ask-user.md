# 12 — ask_user ambiguity

**Stresses:** `ask_user` on vague spec (interactive — needs stdin).

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace eval/fixtures \
  "make the app better"
```

**Pass (either is OK):**
- One focused `ask_user` question **or**
- States a reasonable assumption and does one small concrete thing in fixtures

**Fail:**
- Random large refactor without clarifying
- >2 `ask_user` calls (limit is 3 in CLI)
- `write_file` huge unrelated app

**Interactive:** When `[agent asks]` appears, answer e.g. `add a README to fixtures` and check resume path if you Ctrl+C.

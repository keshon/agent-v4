# 13 — Dev server trap

**Stresses:** long process — `run_shell` timeout vs `start_background`.

**Run (repo root, needs `go` installed):**
```bash
go run ./cmd/agent -log-max 300 \
  "start a tiny HTTP server on :9876 that serves \"ok\" and verify it works"
```

**Pass:**
- `start_background` (not `run_shell` blocking server)
- `check_url` on `http://localhost:9876/` or similar
- `stop_background` when done (or leaves note it's still running)

**Fail:**
- `run_shell` with `go run` / `python -m http.server` that blocks until timeout
- Trusts banner without `check_url`
- Orphan process left without mention

**Cleanup:** kill any leftover listener on 9876.

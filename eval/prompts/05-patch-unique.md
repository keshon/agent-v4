# 05 — Patch unique match

**Stresses:** `patch_file` on a small change.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace eval/fixtures \
  "in sample.go change Version from v1 to v2"
```

**Pass:**
- `patch_file` or `patch_lines` (not full `write_file` rewrite of whole file)
- `sample.go` contains `const Version = "v2"`
- `go build` would succeed (file still valid Go)

**Fail:**
- Rewrites entire file via one giant `write_file`
- Changes only in response text, not on disk
- Breaks syntax

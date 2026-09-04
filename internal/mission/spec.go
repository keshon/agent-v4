package mission

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-v4/internal/workspace"
)

// SpecBrief inlines short text specs (PLAN.md etc.) into the planning
// prompt. Without this, "build what plan.md describes" only sees the
// filename — the planner invents a fake file tree (live failure
// 2026-07-14: npm/src/index.js for a Three.js FPS plan).
const (
	maxSpecFiles = 3
	maxSpecChars = 12 * 1024
)

func SpecBrief(ws *workspace.Workspace, task string, existing map[string]bool) string {
	var paths []string
	seen := map[string]bool{}
	add := func(p string) {
		p = normalizePlanPath(p)
		if p == "" || seen[p] || !existing[p] {
			return
		}
		low := strings.ToLower(p)
		if !strings.HasSuffix(low, ".md") && !strings.HasSuffix(low, ".txt") {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}

	for _, m := range FileMentions(task) {
		if resolved := resolveExisting(existing, m); resolved != "" {
			add(resolved)
		}
	}
	for _, name := range []string{"PLAN.md", "plan.md", "SPEC.md", "spec.md", "BRIEF.md", "brief.md", "TODO.md"} {
		if existing[name] {
			add(name)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	if len(paths) > maxSpecFiles {
		paths = paths[:maxSpecFiles]
	}

	var b strings.Builder
	remaining := maxSpecChars
	for _, p := range paths {
		if remaining <= 0 {
			fmt.Fprintf(&b, "\n…(spec brief truncated)\n")
			break
		}
		full := filepath.Join(ws.Root(), filepath.FromSlash(p))
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		body := string(data)
		if len(body) > remaining {
			body = body[:remaining]
			if cut := strings.LastIndexByte(body, '\n'); cut > remaining/2 {
				body = body[:cut]
			}
			body += "\n…(truncated)"
		}
		remaining -= len(body)
		fmt.Fprintf(&b, "### %s\n%s\n", p, body)
	}
	return strings.TrimSpace(b.String())
}

func resolveExisting(existing map[string]bool, name string) string {
	name = normalizePlanPath(name)
	if existing[name] {
		return name
	}
	low := strings.ToLower(name)
	for p := range existing {
		if strings.ToLower(p) == low {
			return p
		}
	}
	return ""
}

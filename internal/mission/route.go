package mission

import (
	"regexp"
	"strings"
)

// fileMentionRE finds deliverable-looking paths in a task. Deliberately
// narrow: basename-ish tokens with common code/web extensions — enough
// to detect "build X in a.js, b.js, c.html" without NLP.
var fileMentionRE = regexp.MustCompile(`(?i)\b[\w./-]+\.(?:js|ts|tsx|jsx|html|css|go|py|rs|java|vue|svelte|md)\b`)

// FileMentions returns unique path-like tokens from task text.
func FileMentions(task string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range fileMentionRE.FindAllString(task, -1) {
		m = strings.ToLower(strings.ReplaceAll(m, "\\", "/"))
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// SuggestMission is a cheap Go heuristic: multi-file deliverables should
// not start in the reactive loop. False negatives stay on -mission; use
// -direct to force the loop when this fires.
func SuggestMission(task string) bool {
	return len(FileMentions(task)) >= 2
}

// SkillHint returns a one-line nudge listing SKILL.md paths relevant to
// the task. Empty when nothing matches — no loader, no injection of skill
// bodies (workers still read_file themselves).
func SkillHint(task string) string {
	lower := strings.ToLower(task)
	var hits []string
	add := func(path string) {
		for _, h := range hits {
			if h == path {
				return
			}
		}
		hits = append(hits, path)
	}
	if SuggestMission(task) || strings.Contains(lower, "large") || strings.Contains(lower, "full app") {
		add("skills/large-build/SKILL.md")
	}
	if strings.Contains(lower, "git") {
		add("skills/git/SKILL.md")
	}
	if strings.Contains(lower, "npm run dev") || strings.Contains(lower, "dev server") ||
		strings.Contains(lower, "hot reload") || strings.Contains(lower, "vite") {
		add("skills/dev-server/SKILL.md")
	}
	if strings.Contains(lower, "go.mod") || strings.Contains(lower, "go test") ||
		strings.Contains(lower, "golang") || strings.Contains(lower, ".go") {
		add("skills/go/SKILL.md")
	}
	if strings.Contains(lower, "typescript") || strings.Contains(lower, ".tsx") ||
		strings.Contains(lower, ".ts") {
		add("skills/typescript/SKILL.md")
	}
	if len(hits) == 0 {
		return ""
	}
	return "Before writing, read_file: " + strings.Join(hits, ", ")
}

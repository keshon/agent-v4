package mission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuggestMission_MultiFile(t *testing.T) {
	doom := "create index.html, walls.js, player.js, and game.js for a maze"
	if !SuggestMission(doom) {
		t.Fatal("multi-file doom-shaped task should suggest mission")
	}
	if SuggestMission("fix the typo in README") {
		t.Fatal("simple edit should not suggest mission")
	}
	if SuggestMission("create index.html with a hello message") {
		t.Fatal("single-file create should stay on the reactive loop")
	}
}

func TestFileMentions_Unique(t *testing.T) {
	got := FileMentions("edit game.js and Game.js then index.html")
	if len(got) != 2 { // game.js uniqued case-insensitively; + index.html
		t.Fatalf("got %v, want 2 unique paths", got)
	}
}

func TestSkillHint_LargeBuildAndGit(t *testing.T) {
	h := SkillHint("build walls.js and player.js; also inspect git log")
	if !strings.Contains(h, "skills/large-build/SKILL.md") {
		t.Fatalf("want large-build hint, got %q", h)
	}
	if !strings.Contains(h, "skills/git/SKILL.md") {
		t.Fatalf("want git hint, got %q", h)
	}
	if SkillHint("say hello") != "" {
		t.Fatal("unrelated task should not hint skills")
	}
}

func TestParseAndValidate_PlanTooCoarse_Rejected(t *testing.T) {
	plan := `{"subtasks":[{"id":"a","milestone":"m","title":"all","goal":"build everything","acceptance":["done"],"files_hint":["index.html"],"check":{"type":"file_exists","path":"index.html"}}]}`
	task := "create index.html with a canvas, walls.js map, player.js movement, game.js loop"
	_, _, errs := parseAndValidate(plan, task, map[string]bool{})
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, ";"), "only 1 subtask") {
		t.Fatalf("errs = %v, want coarseness rejection", errs)
	}
}

func TestParseAndValidate_ContentContains_Rules(t *testing.T) {
	ok := `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["game.js"],"check":{"type":"content_contains","path":"game.js","contains":"raycast"}}]}`
	if _, _, errs := parseAndValidate(ok, "", map[string]bool{}); len(errs) != 0 {
		t.Fatalf("valid content_contains rejected: %v", errs)
	}
	short := `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["game.js"],"check":{"type":"content_contains","path":"game.js","contains":"x"}}]}`
	if _, _, errs := parseAndValidate(short, "", map[string]bool{}); len(errs) == 0 || !strings.Contains(strings.Join(errs, ";"), "too short") {
		t.Fatalf("short needle should fail, got %v", errs)
	}
}

func TestParseAndValidate_ReadOnlySubtask_Rejected(t *testing.T) {
	plan := `{"subtasks":[
		{"id":"a","milestone":"m","title":"read plan","goal":"Read PLAN.md to understand the project.","acceptance":["understood"],"files_hint":["PLAN.md"],"check":{"type":"none"}},
		{"id":"b","milestone":"m","title":"implement","goal":"Create game.js.","acceptance":["game.js exists"],"files_hint":["game.js"],"check":{"type":"file_exists","path":"game.js"}}]}`
	_, _, errs := parseAndValidate(plan, "", map[string]bool{"PLAN.md": true})
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, ";"), "read-only subtask") {
		t.Fatalf("errs = %v, want read-only rejection", errs)
	}
}

func TestParseAndValidate_StripThink_BeforeJSON(t *testing.T) {
	raw := "<think>\nlong reasoning\n</think>\n\n" +
		`{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"Create game.js with a loop.","acceptance":["loop exists"],"files_hint":["game.js"],"check":{"type":"content_contains","path":"game.js","contains":"requestAnimationFrame"}}]}`
	subtasks, _, errs := parseAndValidate(raw, "", map[string]bool{})
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(subtasks) != 1 {
		t.Fatalf("got %d subtasks", len(subtasks))
	}
}

func TestSpecBrief_InlinesPlanMd(t *testing.T) {
	ws := testWS(t)
	path := filepath.Join(ws.Root(), "PLAN.md")
	if err := os.WriteFile(path, []byte("# Tech\nUse Three.js\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, existing := WorkspaceListing(ws)
	brief := SpecBrief(ws, "make the project in plan.md", existing)
	if !strings.Contains(brief, "Three.js") || !strings.Contains(brief, "PLAN.md") {
		t.Fatalf("brief missing plan contents: %q", brief)
	}
}



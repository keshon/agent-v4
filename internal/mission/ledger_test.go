package mission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleMission() *Mission {
	return &Mission{
		ID:    "abc123",
		Task:  "build a small site",
		Phase: PhaseExecute,
		Subtasks: []Subtask{
			{
				ID: "s1", Milestone: "scaffold", Title: "create html shell",
				Goal:       "Create index.html with a canvas element.",
				Acceptance: []string{"index.html exists"},
				FilesHint:  []string{"index.html"},
				Check:      Check{Type: "file_exists", Path: "index.html"},
				Status:     StatusDone,
				Facts:      []string{"wrote: index.html", "check: PASSED"},
			},
			{
				ID: "s2", Milestone: "logic", Title: "game loop",
				Goal:       "Add game.js with a requestAnimationFrame loop.",
				Acceptance: []string{"game.js exists", "loop draws each frame"},
				FilesHint:  []string{"game.js"},
				Check:      Check{Type: "file_exists", Path: "game.js"},
				Status:     StatusPending,
			},
		},
		Cursor: 1,
	}
}

func TestMission_SaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := sampleMission()
	if err := m.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !Exists(dir) {
		t.Fatal("Exists = false after Save")
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Task != m.Task || got.Phase != m.Phase || got.Cursor != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if len(got.Subtasks) != 2 || got.Subtasks[0].Check.Path != "index.html" {
		t.Fatalf("subtasks did not round-trip: %+v", got.Subtasks)
	}
	if got.Subtasks[0].Facts[1] != "check: PASSED" {
		t.Fatalf("facts did not round-trip: %v", got.Subtasks[0].Facts)
	}
}

func TestMission_Save_LeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	if err := sampleMission().Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mission.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp file left behind after atomic save")
	}
}

func TestMission_RenderLedger_MarksCurrentAndCarriesFacts(t *testing.T) {
	out := sampleMission().RenderLedger()

	if !strings.Contains(out, "s1 [done] create html shell") {
		t.Fatalf("missing done line:\n%s", out)
	}
	if !strings.Contains(out, "wrote: index.html; check: PASSED") {
		t.Fatalf("facts missing from done line:\n%s", out)
	}
	if !strings.Contains(out, "s2 [NOW] game loop") {
		t.Fatalf("cursor subtask not marked NOW:\n%s", out)
	}
}

func TestMission_RenderLedger_FailedStatusNotMaskedByCursor(t *testing.T) {
	m := sampleMission()
	m.Subtasks[1].Status = StatusFailed
	out := m.RenderLedger()
	if !strings.Contains(out, "s2 [failed]") {
		t.Fatalf("failed subtask under cursor should render failed, not NOW:\n%s", out)
	}
}

func TestMission_Current(t *testing.T) {
	m := sampleMission()
	if got := m.Current(); got == nil || got.ID != "s2" {
		t.Fatalf("Current = %+v, want s2", got)
	}
	m.Cursor = 2
	if m.Current() != nil {
		t.Fatal("Current past the end should be nil")
	}
}

func TestMission_RenderPlan_ShowsGoalsAcceptanceChecks(t *testing.T) {
	out := sampleMission().RenderPlan()
	for _, want := range []string{
		"Task: build a small site",
		"== scaffold ==",
		"s1: create html shell",
		"goal: Create index.html with a canvas element.",
		"accept: index.html exists",
		"check: file must exist: index.html",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("RenderPlan missing %q:\n%s", want, out)
		}
	}
}

func TestLoad_MissingDir(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing mission file")
	}
}

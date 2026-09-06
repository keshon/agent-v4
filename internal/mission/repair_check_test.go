package mission

import "testing"

// The plan that motivated this carried `grep 'store_var' Save.gd` and was
// rejected whole, discarding a correct fix. These tests do not use grep:
// whether it resolves depends on the PATH the harness was launched with
// (Git Bash supplies one, cmd.exe does not), so a test naming it asserts
// something about the shell rather than about this rule. The command
// below is absent everywhere.
func TestRepairUnrunnableCheck_KeepsThePlanAndWatchesTheOutput(t *testing.T) {
	s := &Subtask{
		ID:        "s4",
		FilesHint: []string{"Save.gd"},
		Check:     Check{Type: "shell", Cmd: "nosuchtool-xyzzy 'store_var' Save.gd"},
	}
	note := repairUnrunnableCheck(s)
	if note == "" {
		t.Fatal("no repair reported for a check using a command this host lacks")
	}
	if s.Check.Type != "file_exists" || s.Check.Path != "Save.gd" {
		t.Fatalf("check = %+v, want file_exists on Save.gd", s.Check)
	}
	if errs := validateCheck(s, map[string]bool{}); len(errs) > 0 {
		t.Errorf("repaired check still fails validation: %v", errs)
	}
}

// With nothing to watch, the check goes to none rather than staying
// broken — the derived checks and the final verify still gate the work.
func TestRepairUnrunnableCheck_DropsWhenNoPathToWatch(t *testing.T) {
	s := &Subtask{ID: "s1", Check: Check{Type: "shell", Cmd: "nosuchtool-xyzzy foo bar.txt"}}
	if note := repairUnrunnableCheck(s); note == "" {
		t.Fatal("expected a repair note")
	}
	if s.Check.Type != "none" {
		t.Errorf("check type = %q, want none", s.Check.Type)
	}
}

// A check that runs here must survive untouched, or repair becomes a
// silent weakening of every plan.
func TestRepairUnrunnableCheck_LeavesRunnableChecksAlone(t *testing.T) {
	for _, cmd := range []string{"go test ./...", "go build ./...", "python stats.py"} {
		s := &Subtask{ID: "s1", FilesHint: []string{"x.go"},
			Check: Check{Type: "shell", Cmd: cmd}}
		if note := repairUnrunnableCheck(s); note != "" {
			t.Errorf("%q was repaired (%s), want left alone", cmd, note)
		}
		if s.Check.Type != "shell" || s.Check.Cmd != cmd {
			t.Errorf("%q was rewritten to %+v", cmd, s.Check)
		}
	}
}

// Only shell checks are this rule's business.
func TestRepairUnrunnableCheck_IgnoresNonShellChecks(t *testing.T) {
	s := &Subtask{ID: "s1", Check: Check{Type: "file_exists", Path: "grep.txt"}}
	if note := repairUnrunnableCheck(s); note != "" {
		t.Errorf("repaired a file_exists check: %s", note)
	}
}

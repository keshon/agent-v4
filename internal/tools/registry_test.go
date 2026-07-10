package tools

import (
	"testing"

	"agent-v4/internal/workspace"
)

func TestReadOnly_ContainsNoMutatingOrShellTools(t *testing.T) {
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	reg := ReadOnly(ws, NewBackgroundProcesses())

	forbidden := map[string]bool{
		"write_file": true, "patch_file": true, "patch_lines": true,
		"move_file": true, "run_shell": true,
		"start_background": true, "stop_background": true,
		"ask_user": true, "delegate_task": true,
	}
	var names []string
	for _, def := range reg.Defs() {
		names = append(names, def.Name)
		if forbidden[def.Name] {
			t.Errorf("ReadOnly registry contains forbidden tool %q", def.Name)
		}
	}
	if len(names) == 0 {
		t.Fatal("ReadOnly registry is empty")
	}
}

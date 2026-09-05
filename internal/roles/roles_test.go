package roles

import (
	"context"
	"slices"
	"testing"

	"tars/internal/llm"
	"tars/internal/tools"
	"tars/internal/workspace"
)

type stubClient struct{}

func (stubClient) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{}, nil
}

func testEnv(t *testing.T) Env {
	t.Helper()
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return Env{Client: stubClient{}, WS: ws, Procs: tools.NewBackgroundProcesses()}
}

func hasTool(a interface{ ToolNames() []string }, name string) bool {
	return slices.Contains(a.ToolNames(), name)
}

// The bug this package exists to prevent (2026-09-04): the eval harness
// built its agent by hand and ended up without ask_user or delegate_task,
// while the shipping CLI had both — so the instrument was scoring a
// different agent than the one under test, and two probes are
// specifically about those two tools.
func TestInteractive_HasTheToolsThatShip(t *testing.T) {
	a := Interactive(testEnv(t), "", "", func(string) (string, error) { return "", nil }, nil)
	for _, want := range []string{"ask_user", "delegate_task", "write_file", "run_shell"} {
		if !hasTool(a, want) {
			t.Errorf("interactive agent is missing %s — got %v", want, a.ToolNames())
		}
	}
}

// Passing no responder removes the tool rather than offering one that
// cannot be answered.
func TestInteractive_WithoutResponder_HasNoAskUser(t *testing.T) {
	a := Interactive(testEnv(t), "", "", nil, nil)
	if hasTool(a, "ask_user") {
		t.Error("ask_user offered with no responder to answer it")
	}
	if !hasTool(a, "delegate_task") {
		t.Error("delegate_task should not depend on the ask responder")
	}
}

// A subagent that could delegate could spawn subagents without bound, and
// one that could ask would block the process on a human watching the
// parent.
func TestSubagent_CannotDelegateOrAsk(t *testing.T) {
	a := Subagent(testEnv(t), "reviewer")
	for _, forbidden := range []string{"delegate_task", "ask_user"} {
		if hasTool(a, forbidden) {
			t.Errorf("subagent can call %s — got %v", forbidden, a.ToolNames())
		}
	}
	if !hasTool(a, "write_file") {
		t.Error("subagent should still have the working tool set")
	}
}

// Taking the mutating tools away beats asking a weak model not to use
// them, which is the whole reason inspectors get a different registry.
func TestInspector_CannotChangeAnything(t *testing.T) {
	a := Inspector(testEnv(t), "review", "sys", "")
	for _, forbidden := range []string{"write_file", "patch_file", "patch_lines", "move_file", "run_shell"} {
		if hasTool(a, forbidden) {
			t.Errorf("inspector can call %s — got %v", forbidden, a.ToolNames())
		}
	}
	if !hasTool(a, "read_file") {
		t.Error("inspector must still be able to read")
	}
}

func TestWorker_HasWorkingToolsButNoDelegation(t *testing.T) {
	a := Worker(testEnv(t), "s1", "sys", "", 15, true)
	if !hasTool(a, "write_file") {
		t.Error("worker must be able to write")
	}
	if hasTool(a, "delegate_task") {
		t.Error("a mission worker delegating means the plan already decomposed the task")
	}
}

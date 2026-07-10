package agent

import (
	"context"
	"encoding/json"
	"testing"

	"agent-v4/internal/llm"
)

// mutStub stands in for any of the project's mutating file tools — the
// report only cares about the name and the path-shaped arguments.
type mutStub struct{ name string }

func (m mutStub) Name() string            { return m.name }
func (m mutStub) Description() string     { return "stub" }
func (mutStub) Mode() ToolMode            { return Exclusive }
func (mutStub) Schema() json.RawMessage   { return json.RawMessage(`{}`) }
func (mutStub) Run(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func TestAgent_Report_RecordsMutatedPathsStepsAndFinal(t *testing.T) {
	writeArgs, _ := json.Marshal(map[string]string{"path": "a.txt", "content": "x"})
	moveArgs, _ := json.Marshal(map[string]string{"from": "b.txt", "to": "c.txt"})
	writeAgain, _ := json.Marshal(map[string]string{"path": "a.txt", "content": "y"})

	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "1", Name: "write_file", Arguments: writeArgs},
			{ID: "2", Name: "move_file", Arguments: moveArgs},
		}}, Usage: llm.Usage{PromptTokens: 100}},
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "3", Name: "write_file", Arguments: writeAgain},
		}}, Usage: llm.Usage{PromptTokens: 200}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"},
			Usage: llm.Usage{PromptTokens: 300}},
	}}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(mutStub{"write_file"}, mutStub{"move_file"}),
		System:     "sys",
		SkipVerify: true,
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	r := a.Report()
	if r.Steps != 3 {
		t.Fatalf("Steps = %d, want 3", r.Steps)
	}
	if r.Final != out || r.Final != "done" {
		t.Fatalf("Final = %q, want %q", r.Final, "done")
	}
	if r.LastPromptTokens != 300 {
		t.Fatalf("LastPromptTokens = %d, want 300", r.LastPromptTokens)
	}
	// First-touch order, a.txt not repeated for the second write.
	want := []string{"a.txt", "b.txt", "c.txt"}
	if len(r.MutatedPaths) != len(want) {
		t.Fatalf("MutatedPaths = %v, want %v", r.MutatedPaths, want)
	}
	for i, p := range want {
		if r.MutatedPaths[i] != p {
			t.Fatalf("MutatedPaths = %v, want %v", r.MutatedPaths, want)
		}
	}
}

func TestAgent_Report_PopulatedEvenWhenMaxStepsExceeded(t *testing.T) {
	writeArgs, _ := json.Marshal(map[string]string{"path": "half-done.txt"})
	client := &repeatingToolClient{args: writeArgs}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(mutStub{"write_file"}),
		System:     "sys",
		SkipVerify: true,
		MaxSteps:   2,
	})

	if _, err := a.Run(context.Background(), "task"); err == nil {
		t.Fatal("expected max-steps error")
	}

	r := a.Report()
	if r.Steps != 2 {
		t.Fatalf("Steps = %d, want 2", r.Steps)
	}
	if len(r.MutatedPaths) != 1 || r.MutatedPaths[0] != "half-done.txt" {
		t.Fatalf("MutatedPaths = %v, want [half-done.txt] — a run that hit "+
			"MaxSteps still wrote files, and the harness must know", r.MutatedPaths)
	}
	if a.LastRunMutations != 2 {
		t.Fatalf("LastRunMutations = %d, want 2 (one successful write per step)", a.LastRunMutations)
	}
	if r.Final != "" {
		t.Fatalf("Final = %q, want empty for an unfinished run", r.Final)
	}
}

func TestAgent_Report_ResetBetweenRuns(t *testing.T) {
	writeArgs, _ := json.Marshal(map[string]string{"path": "first.txt"})
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "1", Name: "write_file", Arguments: writeArgs},
		}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "first done"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "second done"}},
	}}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(mutStub{"write_file"}),
		System:     "sys",
		SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task one"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, err := a.Run(context.Background(), "task two"); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	r := a.Report()
	if len(r.MutatedPaths) != 0 {
		t.Fatalf("MutatedPaths = %v, want empty — report must not leak across runs", r.MutatedPaths)
	}
	if r.Final != "second done" {
		t.Fatalf("Final = %q, want %q", r.Final, "second done")
	}
}

func TestMutatedPathsFromCall(t *testing.T) {
	got := mutatedPathsFromCall(json.RawMessage(`{"from":"a","to":"b"}`))
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("move args = %v, want [a b]", got)
	}
	if got := mutatedPathsFromCall(json.RawMessage(`{"other":"x"}`)); got != nil {
		t.Fatalf("pathless args = %v, want nil", got)
	}
	if got := mutatedPathsFromCall(json.RawMessage(`not-json`)); got != nil {
		t.Fatalf("invalid args = %v, want nil", got)
	}
}

// repeatingToolClient issues the same single write_file call forever.
type repeatingToolClient struct {
	args  json.RawMessage
	calls int
}

func (c *repeatingToolClient) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	c.calls++
	return llm.ChatResponse{Message: llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{
			{ID: "r", Name: "write_file", Arguments: c.args},
		},
	}}, nil
}

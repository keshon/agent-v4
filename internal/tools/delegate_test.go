package tools

import (
	"context"
	"encoding/json"
	"testing"

	"agent-v4/internal/agent"
	"agent-v4/internal/llm"
)

// stubSubClient returns a fixed answer with no tool calls, so spawned
// subagents finish immediately without needing real tools.
type stubSubClient struct{ answer string }

func (c stubSubClient) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: c.answer}}, nil
}

func TestDelegate_PassesRoleToSpawn(t *testing.T) {
	var gotRole string
	d := Delegate{
		Spawn: func(role string) *agent.Agent {
			gotRole = role
			return agent.New(agent.Config{
				Client:     stubSubClient{answer: "done"},
				Tools:      agent.NewRegistry(),
				System:     "base",
				SkipVerify: true,
			})
		},
	}

	args, _ := json.Marshal(map[string]string{
		"task": "write a greeting",
		"role": "Alice, a software engineer from London who loves tea",
	})
	out, err := d.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("result = %q, want %q", out, "done")
	}
	if gotRole != "Alice, a software engineer from London who loves tea" {
		t.Fatalf("role passed to Spawn = %q, want the role text", gotRole)
	}
}

func TestDelegate_EmptyRoleIsFine(t *testing.T) {
	var gotRole string
	called := false
	d := Delegate{
		Spawn: func(role string) *agent.Agent {
			called = true
			gotRole = role
			return agent.New(agent.Config{
				Client:     stubSubClient{answer: "done"},
				Tools:      agent.NewRegistry(),
				System:     "base",
				SkipVerify: true,
			})
		},
	}

	args, _ := json.Marshal(map[string]string{"task": "write a greeting"})
	if _, err := d.Run(context.Background(), args); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !called {
		t.Fatal("Spawn was never called")
	}
	if gotRole != "" {
		t.Fatalf("role = %q, want empty when not provided", gotRole)
	}
}

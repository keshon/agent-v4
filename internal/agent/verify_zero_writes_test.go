package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agent-v4/internal/llm"
)

// The announce-without-write shape: a worker reads everything, says "let
// me write the file", and finishes with zero mutating calls. With
// VerifyOnZeroWrites the loop injects the verify round (carrying the
// zero-writes hard fact) even though SkipVerify is set — one in-context
// nudge instead of a fresh fix worker.
func TestAgent_VerifyOnZeroWrites_FiresOnlyWhenNothingWritten(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "I am done (wrote nothing)"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "actually writing now"}},
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(), System: "sys",
		SkipVerify: true, VerifyOnZeroWrites: true,
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2 — zero-writes finish must trigger the verify round", client.calls)
	}
	if out != "actually writing now" {
		t.Fatalf("out = %q", out)
	}
	var factSeen bool
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "0 file-writing") {
			factSeen = true
		}
	}
	if !factSeen {
		t.Fatal("zero-writes hard fact missing from the verify message")
	}
}

func TestAgent_VerifyOnZeroWrites_SkippedAfterRealWrites(t *testing.T) {
	writeArgs, _ := json.Marshal(map[string]string{"path": "a.txt", "content": "x"})
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "1", Name: "write_file", Arguments: writeArgs}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(mutStub{"write_file"}), System: "sys",
		SkipVerify: true, VerifyOnZeroWrites: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2 — a worker that wrote files finishes without the extra round", client.calls)
	}
}

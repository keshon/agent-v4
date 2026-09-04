package agent

import (
	"context"
	"encoding/json"
	"testing"

	"tars/internal/llm"
	"tars/internal/prompts"
)

func readStep(id, path string) llm.ChatResponse {
	args, _ := json.Marshal(map[string]string{"path": path})
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: id, Name: "read_file", Arguments: args}}}}
}

// Live failure (2026-07-14, FPS mission): a worker wrote package.json,
// vite.config.ts and index.html — three correct consecutive writes, and
// exactly what its files_hint asked for — then got told it was looping
// and should "switch to a genuinely different tool". src/main.ts, the
// third acceptance criterion, was never written. A step that changed the
// workspace is progress and can never be evidence of a loop.
func TestAgent_ToolLoop_NotTriggeredByWritingDifferentFiles(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		writeStep("1", "package.json"),
		writeStep("2", "vite.config.ts"),
		writeStep("3", "index.html"),
		say("scaffolded the project"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(mutStub{"write_file"}), System: "sys",
		SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if countInjected(client.lastHistory, prompts.ToolLoop) != 0 {
		t.Fatal("writing three different files tripped the tool-loop nudge")
	}
}

// The case the nudge was actually written for: grinding a read-only tool
// with slightly different arguments, converging on nothing.
func TestAgent_ToolLoop_StillTriggeredByRepeatedReads(t *testing.T) {
	var runs int32
	client := &stubClient{responses: []llm.ChatResponse{
		readStep("1", "a.go"),
		readStep("2", "b.go"),
		readStep("3", "c.go"),
		say("still looking"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(readStub{name: "read_file", runs: &runs}), System: "sys",
		SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if countInjected(client.lastHistory, prompts.ToolLoop) != 1 {
		t.Fatal("three consecutive read-only calls did not trip the tool-loop nudge")
	}
}

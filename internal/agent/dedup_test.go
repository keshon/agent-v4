package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"agent-v4/internal/llm"
)

// readStub is an idempotent (pure-read) tool that counts executions.
type readStub struct {
	name string
	runs *int32
}

func (r readStub) Name() string          { return r.name }
func (r readStub) Description() string   { return "stub" }
func (readStub) Mode() ToolMode          { return Concurrent }
func (readStub) Idempotent() bool        { return true }
func (readStub) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (r readStub) Run(context.Context, json.RawMessage) (string, error) {
	atomic.AddInt32(r.runs, 1)
	return "file contents", nil
}

// Live failure shape (Qwen run, 2026-07-11): the model issued the same
// read_file call 5-7 times in a row. Identical idempotent calls must not
// re-execute — the repeat comes back as an error result telling the model
// to do something different, and it must not count as progress.
func TestAgent_IdempotentRepeat_ShortCircuits(t *testing.T) {
	var runs int32
	readArgs := json.RawMessage(`{"path":"a.go"}`)
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "1", Name: "read_file", Arguments: readArgs}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "2", Name: "read_file", Arguments: readArgs}}}}, // exact repeat
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(readStub{name: "read_file", runs: &runs}),
		System:     "sys",
		SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("tool executed %d times, want 1 — the repeat must not re-execute", got)
	}

	// The repeat's tool-result must be an instructive error, not content.
	var repeatResult string
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleTool && m.ToolCallID == "2" {
			repeatResult = m.Content
		}
	}
	if !strings.Contains(repeatResult, "error:") || !strings.Contains(repeatResult, "already ran in step 0") {
		t.Fatalf("repeat result = %q, want an error pointing at the earlier step", repeatResult)
	}
}

func TestAgent_IdempotentRepeat_ReExecutesAfterMutation(t *testing.T) {
	var runs int32
	readArgs := json.RawMessage(`{"path":"a.go"}`)
	writeArgs := json.RawMessage(`{"path":"a.go","content":"new"}`)
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "1", Name: "read_file", Arguments: readArgs}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "2", Name: "write_file", Arguments: writeArgs}}}}, // mutation
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "3", Name: "read_file", Arguments: readArgs}}}}, // same read, now legitimate
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(readStub{name: "read_file", runs: &runs}, mutStub{"write_file"}),
		System:     "sys",
		SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&runs); got != 2 {
		t.Fatalf("tool executed %d times, want 2 — a mutation invalidates the repeat cache", got)
	}
}

func TestAgent_NonIdempotentTools_NeverDeduped(t *testing.T) {
	// check_url-style tools vary over time; identical calls must always
	// re-execute. echoToolStub does not implement Idempotent.
	var runs int32
	tool := countingConcurrentStub{runs: &runs}
	args := json.RawMessage(`{"url":"http://x"}`)
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "1", Name: "check_url", Arguments: args}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "2", Name: "check_url", Arguments: args}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(tool), System: "sys", SkipVerify: true})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&runs); got != 2 {
		t.Fatalf("tool executed %d times, want 2 — non-idempotent tools always re-run", got)
	}
}

type countingConcurrentStub struct{ runs *int32 }

func (countingConcurrentStub) Name() string          { return "check_url" }
func (countingConcurrentStub) Description() string   { return "stub" }
func (countingConcurrentStub) Mode() ToolMode        { return Concurrent }
func (countingConcurrentStub) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (c countingConcurrentStub) Run(context.Context, json.RawMessage) (string, error) {
	atomic.AddInt32(c.runs, 1)
	return "status 200", nil
}

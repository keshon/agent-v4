package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"tars/internal/llm"
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

func (countingConcurrentStub) Name() string            { return "check_url" }
func (countingConcurrentStub) Description() string     { return "stub" }
func (countingConcurrentStub) Mode() ToolMode          { return Concurrent }
func (countingConcurrentStub) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (c countingConcurrentStub) Run(context.Context, json.RawMessage) (string, error) {
	atomic.AddInt32(c.runs, 1)
	return "status 200", nil
}

// Live failure (2026-07-14): write_file package.json succeeded, then was
// reissued identically for many steps. Soft nudges failed because each
// "ok" counted as progress. A second write_file to the same path must
// short-circuit as an error and must not re-execute.
func TestAgent_WriteFile_SamePathRefused(t *testing.T) {
	var runs int32
	writeArgs := json.RawMessage(`{"path":"package.json","content":"{\"name\":\"x\"}"}`)
	otherArgs := json.RawMessage(`{"path":"tsconfig.json","content":"{}"}`)
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "1", Name: "write_file", Arguments: writeArgs}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "2", Name: "write_file", Arguments: writeArgs}}}}, // same path — refuse
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "3", Name: "write_file", Arguments: otherArgs}}}}, // new path — allow
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(countingWriteStub{runs: &runs}),
		System:     "sys",
		SkipVerify: true,
	})
	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&runs); got != 2 {
		t.Fatalf("write_file ran %d times, want 2 (first package.json + tsconfig.json)", got)
	}
	var refuse string
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleTool && m.ToolCallID == "2" {
			refuse = m.Content
		}
	}
	if !strings.Contains(refuse, "error:") || !strings.Contains(refuse, "already wrote package.json") {
		t.Fatalf("refuse result = %q, want already-wrote error", refuse)
	}
}

type countingWriteStub struct{ runs *int32 }

func (countingWriteStub) Name() string            { return "write_file" }
func (countingWriteStub) Description() string     { return "stub" }
func (countingWriteStub) Mode() ToolMode          { return Exclusive }
func (countingWriteStub) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (c countingWriteStub) Run(context.Context, json.RawMessage) (string, error) {
	atomic.AddInt32(c.runs, 1)
	return "wrote ok", nil
}

// A model re-reading one file rarely spells the arguments the same way
// twice: {"path":"x"} one step, {"path": "x", "metadata_only": false} the
// next. On raw bytes those are different keys, so the repeat guard never
// fires and the same file comes back again — observed live on 2026-09-05
// as two consecutive reads of a file that had just been confirmed.
func TestCallKey_IgnoresArgumentSpelling(t *testing.T) {
	same := []string{
		`{"path":"a.go"}`,
		`{"path": "a.go"}`,
		`{ "path" : "a.go" }`,
	}
	want := callKey("read_file", json.RawMessage(same[0]))
	for _, args := range same[1:] {
		if got := callKey("read_file", json.RawMessage(args)); got != want {
			t.Errorf("callKey(%s) = %q, want %q — same call, different spelling", args, got, want)
		}
	}
}

func TestCallKey_IgnoresKeyOrder(t *testing.T) {
	a := callKey("read_file", json.RawMessage(`{"path":"a.go","max_bytes":10}`))
	b := callKey("read_file", json.RawMessage(`{"max_bytes":10,"path":"a.go"}`))
	if a != b {
		t.Errorf("key order changed the signature:\n %q\n %q", a, b)
	}
}

// Genuinely different calls must stay different, including a field that
// is present in one and absent in the other — metadata_only:true really
// is a different request.
func TestCallKey_KeepsRealDifferences(t *testing.T) {
	base := callKey("read_file", json.RawMessage(`{"path":"a.go"}`))
	for _, other := range []string{
		`{"path":"b.go"}`,
		`{"path":"a.go","metadata_only":true}`,
	} {
		if got := callKey("read_file", json.RawMessage(other)); got == base {
			t.Errorf("callKey(%s) collapsed into the base key %q", other, base)
		}
	}
	if callKey("read_file", json.RawMessage(`{"path":"a.go"}`)) ==
		callKey("list_files", json.RawMessage(`{"path":"a.go"}`)) {
		t.Error("different tools with identical arguments share a key")
	}
}

// Malformed arguments fall back to raw bytes rather than collapsing.
func TestCallKey_MalformedArgumentsDoNotCollapse(t *testing.T) {
	a := callKey("run_shell", json.RawMessage(`{"command": "go test`))
	b := callKey("run_shell", json.RawMessage(`not json at all`))
	if a == b {
		t.Error("two different malformed argument blobs share a key")
	}
}

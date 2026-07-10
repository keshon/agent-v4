package mission

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-v4/internal/llm"
	"agent-v4/internal/tools"
)

// runnerClient scripts full ChatResponses (tool calls included) for
// end-to-end Runner tests — the model is fake, but the tools, the
// workspace writes, and the checks are all real.
type runnerClient struct {
	responses []llm.ChatResponse
	requests  []llm.ChatRequest
}

func (c *runnerClient) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.requests) > len(c.responses) {
		return llm.ChatResponse{}, fmt.Errorf("unexpected model call #%d", len(c.requests))
	}
	return c.responses[len(c.requests)-1], nil
}

func text(content string) llm.ChatResponse {
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: content}}
}

func toolCall(name string, args map[string]string) llm.ChatResponse {
	raw, _ := json.Marshal(args)
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "c1", Name: name, Arguments: raw},
	}}}
}

func newRunner(t *testing.T, client llm.Client) (*Runner, string) {
	t.Helper()
	ws := testWS(t)
	dir := t.TempDir()
	return &Runner{
		Client: client,
		WS:     ws,
		Dir:    dir,
		Procs:  tools.NewBackgroundProcesses(),
	}, dir
}

func TestRunner_FullMission_PlanExecuteVerifyDone(t *testing.T) {
	client := &runnerClient{responses: []llm.ChatResponse{
		text(validPlanJSON), // plan call
		toolCall("write_file", map[string]string{"path": "index.html", "content": "<html>hello</html>"}),
		text("created index.html with a hello message"),
	}}
	r, dir := newRunner(t, client)

	m := &Mission{ID: "t1", Task: "make a hello page", Phase: PhasePlan}
	report, err := r.Run(context.Background(), m)
	if err != nil {
		t.Fatalf("Run: %v\nreport:\n%s", err, report)
	}

	if m.Phase != PhaseDone {
		t.Fatalf("Phase = %s, want done", m.Phase)
	}
	if !strings.Contains(report, "s1 [done]") {
		t.Fatalf("report missing done subtask:\n%s", report)
	}
	if !strings.Contains(report, "wrote: index.html") || !strings.Contains(report, "check: PASSED") {
		t.Fatalf("report missing measured facts:\n%s", report)
	}
	if len(m.Mutated) != 1 || m.Mutated[0] != "index.html" {
		t.Fatalf("Mutated = %v, want [index.html]", m.Mutated)
	}

	// The ledger survived on disk at every stage; worker transcript kept.
	if !Exists(dir) {
		t.Fatal("mission.json not persisted")
	}
	if _, err := os.Stat(filepath.Join(dir, "workers", "s1-a1.json")); err != nil {
		t.Fatalf("worker transcript not saved: %v", err)
	}

	// The written file is really there — the check verified reality.
	if _, err := os.Stat(filepath.Join(r.WS.Root(), "index.html")); err != nil {
		t.Fatalf("index.html not actually written: %v", err)
	}

	// Worker calls carry tools; the plan call must not.
	if len(client.requests[0].Tools) != 0 {
		t.Fatal("plan call leaked tools")
	}
	if len(client.requests[1].Tools) == 0 {
		t.Fatal("worker call has no tools")
	}
	// Worker seed is compiled from the ledger, not a shared transcript.
	seed := client.requests[1].Messages[1].Content
	if !strings.Contains(seed, "make a hello page") || !strings.Contains(seed, "s1 [NOW]") {
		t.Fatalf("worker seed not compiled from ledger:\n%s", seed)
	}
}

func TestRunner_CheckFailure_FailsLoudlyWithFacts(t *testing.T) {
	client := &runnerClient{responses: []llm.ChatResponse{
		text(validPlanJSON),
		text("I have created index.html and it works great!"), // classic fake-save: no tool calls
	}}
	r, _ := newRunner(t, client)

	m := &Mission{ID: "t2", Task: "make a hello page", Phase: PhasePlan}
	report, err := r.Run(context.Background(), m)
	if err == nil {
		t.Fatal("expected mission failure when the check fails")
	}
	if m.Phase != PhaseFailed {
		t.Fatalf("Phase = %s, want failed", m.Phase)
	}
	sub := m.Subtasks[0]
	if sub.Status != StatusFailed {
		t.Fatalf("subtask status = %s, want failed", sub.Status)
	}
	joined := strings.Join(sub.Facts, "\n")
	if !strings.Contains(joined, "wrote: nothing") {
		t.Fatalf("facts must record the zero-write truth, got: %s", joined)
	}
	if !strings.Contains(joined, "check: FAILED") {
		t.Fatalf("facts must record the check verdict, got: %s", joined)
	}
	if !strings.Contains(report, "failed") {
		t.Fatalf("report should show the failure:\n%s", report)
	}
}

func TestRunner_ApprovalGate_RejectAborts(t *testing.T) {
	client := &runnerClient{responses: []llm.ChatResponse{text(validPlanJSON)}}
	r, _ := newRunner(t, client)
	r.ApprovePlan = func(string) (bool, string) { return false, "" }

	m := &Mission{ID: "t3", Task: "task", Phase: PhasePlan}
	if _, err := r.Run(context.Background(), m); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err = %v, want plan-rejected failure", err)
	}
	if m.Phase != PhaseFailed {
		t.Fatalf("Phase = %s, want failed", m.Phase)
	}
}

func TestRunner_ApprovalGate_EditNoteTriggersOneRevision(t *testing.T) {
	noWritePlan := `{"subtasks":[{"id":"s1","milestone":"m","title":"just say hi","goal":"Reply with a greeting.","acceptance":["a greeting was produced"],"files_hint":[],"check":{"type":"none"}}]}`
	client := &runnerClient{responses: []llm.ChatResponse{
		text(validPlanJSON), // first plan
		text(noWritePlan),   // revised plan after the note
		text("hi there"),    // the (trivial) worker
	}}
	r, _ := newRunner(t, client)

	gateCalls := 0
	r.ApprovePlan = func(rendered string) (bool, string) {
		gateCalls++
		if gateCalls == 1 {
			return false, "make it a no-file subtask"
		}
		return true, ""
	}

	m := &Mission{ID: "t4", Task: "task", Phase: PhasePlan}
	if _, err := r.Run(context.Background(), m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gateCalls != 2 {
		t.Fatalf("gate calls = %d, want 2", gateCalls)
	}
	// The revision call carried the human's note.
	var noteSeen bool
	for _, msg := range client.requests[1].Messages {
		if strings.Contains(msg.Content, "make it a no-file subtask") {
			noteSeen = true
		}
	}
	if !noteSeen {
		t.Fatal("edit note never reached the second plan call")
	}
	if m.Phase != PhaseDone {
		t.Fatalf("Phase = %s, want done", m.Phase)
	}
}

func TestRunner_TerminalPhases_NoModelCalls(t *testing.T) {
	client := &runnerClient{} // any call would error
	r, _ := newRunner(t, client)

	done := &Mission{ID: "t5", Task: "x", Phase: PhaseDone,
		Subtasks: []Subtask{{ID: "s1", Title: "t", Status: StatusDone}}}
	if _, err := r.Run(context.Background(), done); err != nil {
		t.Fatalf("done mission should return cleanly: %v", err)
	}

	failed := &Mission{ID: "t6", Task: "x", Phase: PhaseFailed}
	if _, err := r.Run(context.Background(), failed); err == nil {
		t.Fatal("failed mission should keep reporting failure")
	}
	if len(client.requests) != 0 {
		t.Fatalf("terminal phases made %d model calls, want 0", len(client.requests))
	}
}

func TestRunner_Resume_SkipsDoneSubtasks(t *testing.T) {
	// A mission interrupted between s1 and s2: resume must not re-run s1.
	client := &runnerClient{responses: []llm.ChatResponse{
		toolCall("write_file", map[string]string{"path": "game.js", "content": "loop()"}),
		text("wrote game.js"),
	}}
	r, _ := newRunner(t, client)

	// index.html must really exist for s1's re-check in verify.
	if err := os.WriteFile(filepath.Join(r.WS.Root(), "index.html"), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := sampleMission() // s1 done, s2 pending, cursor 1, phase execute
	if _, err := r.Run(context.Background(), m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.Phase != PhaseDone {
		t.Fatalf("Phase = %s, want done", m.Phase)
	}
	if len(client.requests) != 2 {
		t.Fatalf("model calls = %d, want 2 (worker only, no re-plan, no s1 re-run)", len(client.requests))
	}
}

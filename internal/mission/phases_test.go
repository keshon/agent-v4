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
		// Most tests script an exact call sequence; the review stage has
		// its own dedicated tests below.
		SkipReview: true,
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
		text("checked my work, all good"),                     // zero-writes verify round; still no writes
	}}
	r, _ := newRunner(t, client)
	r.MaxFixAttempts = -1 // isolate the check-failure path from the fix loop
	r.MaxReplans = -1

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
		text(validPlanJSON),      // first plan
		text(noWritePlan),        // revised plan after the note
		text("hi there"),         // the (trivial) worker
		text("greeting produced"), // its zero-writes verify round
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

func TestRunner_FixLoop_ConvergesOnSecondAttempt(t *testing.T) {
	client := &runnerClient{responses: []llm.ChatResponse{
		text(validPlanJSON),
		text("I created index.html and it works!"), // attempt 1: fake-save, check will fail
		text("double-checked, looks complete"),     // attempt 1's zero-writes verify round — still lying
		toolCall("write_file", map[string]string{"path": "index.html", "content": "<html>hi</html>"}),
		text("wrote index.html for real this time"), // fix worker finishes
	}}
	r, _ := newRunner(t, client)

	m := &Mission{ID: "f1", Task: "make a hello page", Phase: PhasePlan}
	if _, err := r.Run(context.Background(), m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.Phase != PhaseDone {
		t.Fatalf("Phase = %s, want done", m.Phase)
	}
	sub := m.Subtasks[0]
	if sub.Attempts != 2 {
		t.Fatalf("Attempts = %d, want 2 (original + one fix)", sub.Attempts)
	}
	joined := strings.Join(sub.Facts, "\n")
	for _, want := range []string{
		"attempt 1 wrote: nothing", "check: FAILED",
		"attempt 2 wrote: index.html", "check: PASSED",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("facts missing %q:\n%s", want, joined)
		}
	}
	// The fix worker's seed must carry the evidence, not a paraphrase.
	// (requests: 0 plan, 1 worker, 2 zero-writes verify, 3 fix worker)
	fixSeed := client.requests[3].Messages[1].Content
	if !strings.Contains(fixSeed, "FAILED its mechanical check") {
		t.Fatalf("fix seed missing failure framing:\n%s", fixSeed)
	}
	if !strings.Contains(fixSeed, "does not exist") {
		t.Fatalf("fix seed missing the check's actual output:\n%s", fixSeed)
	}
	if m.Replans != 0 {
		t.Fatalf("Replans = %d, want 0 — the fix loop converged", m.Replans)
	}
}

func TestRunner_Replan_AfterFixExhaustion(t *testing.T) {
	noFilePlan := `{"subtasks":[{"id":"x","milestone":"wrap","title":"summarize","goal":"State that the work is complete.","acceptance":["a summary was produced"],"files_hint":[],"check":{"type":"none"}}]}`
	client := &runnerClient{responses: []llm.ChatResponse{
		text(validPlanJSON),
		text("done (not really)"),  // s1 worker, no writes → check fails
		text("yes, really done"),   // s1's zero-writes verify round
		text(noFilePlan),           // replan for the remaining work
		text("all wrapped up"),     // s2 worker
		text("nothing left to do"), // s2's zero-writes verify round (its check is none)
	}}
	r, _ := newRunner(t, client)
	r.MaxFixAttempts = -1 // isolate the replan path from the fix loop

	m := &Mission{ID: "r1", Task: "make a hello page", Phase: PhasePlan}
	if _, err := r.Run(context.Background(), m); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if m.Replans != 1 {
		t.Fatalf("Replans = %d, want 1", m.Replans)
	}
	if len(m.Subtasks) != 2 {
		t.Fatalf("subtasks = %d, want 2 (failed s1 kept + new s2)", len(m.Subtasks))
	}
	if m.Subtasks[0].Status != StatusFailed {
		t.Fatalf("s1 status = %s, want failed — replacement, not erasure", m.Subtasks[0].Status)
	}
	if m.Subtasks[1].ID != "s2" || m.Subtasks[1].Status != StatusDone {
		t.Fatalf("new subtask = %+v, want s2 done", m.Subtasks[1])
	}
	if m.Phase != PhaseDone {
		t.Fatalf("Phase = %s, want done", m.Phase)
	}
	// The replan call saw the execution record and the failure reason.
	// (requests: 0 plan, 1 s1 worker, 2 s1 verify, 3 replan)
	replanMsg := client.requests[3].Messages[1].Content
	if !strings.Contains(replanMsg, "check: FAILED") {
		t.Fatalf("replan message missing execution record:\n%s", replanMsg)
	}
	if !strings.Contains(replanMsg, "subtask failed its check") {
		t.Fatalf("replan message missing the reason:\n%s", replanMsg)
	}
}

func TestRunner_Replan_BudgetExhaustedFailsLoudly(t *testing.T) {
	client := &runnerClient{responses: []llm.ChatResponse{
		text(validPlanJSON),
		text("done (not really)"), // no writes → check fails
		text("confirmed done"),    // zero-writes verify round
	}}
	r, _ := newRunner(t, client)
	r.MaxFixAttempts = -1
	r.MaxReplans = -1 // no replans allowed

	m := &Mission{ID: "r2", Task: "make a hello page", Phase: PhasePlan}
	_, err := r.Run(context.Background(), m)
	if err == nil || !strings.Contains(err.Error(), "replan budget exhausted") {
		t.Fatalf("err = %v, want replan-budget failure", err)
	}
	if m.Phase != PhaseFailed {
		t.Fatalf("Phase = %s, want failed", m.Phase)
	}
}

func TestRunner_VerifyRegression_TriggersReplan(t *testing.T) {
	// A mission that reaches verify with a done subtask whose artifact has
	// since gone missing — the replan must produce the remaining work and
	// the second verify pass must confirm it.
	fixPlan := `{"subtasks":[{"id":"x","milestone":"repair","title":"restore page","goal":"Recreate index.html with a hello message.","acceptance":["index.html exists"],"files_hint":["index.html"],"check":{"type":"file_exists","path":"index.html"}}]}`
	client := &runnerClient{responses: []llm.ChatResponse{
		text(fixPlan),
		toolCall("write_file", map[string]string{"path": "index.html", "content": "<html>restored</html>"}),
		text("restored index.html"),
	}}
	r, _ := newRunner(t, client)

	m := &Mission{ID: "v1", Task: "make a hello page", Phase: PhaseVerify,
		Subtasks: []Subtask{{
			ID: "s1", Title: "create page", Goal: "make page", Status: StatusDone,
			Acceptance: []string{"index.html exists"},
			Check:      Check{Type: "file_exists", Path: "index.html"}, // file was never written
		}},
		Cursor: 1,
	}
	if _, err := r.Run(context.Background(), m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.Replans != 1 {
		t.Fatalf("Replans = %d, want 1", m.Replans)
	}
	if m.Phase != PhaseDone {
		t.Fatalf("Phase = %s, want done", m.Phase)
	}
	joined := strings.Join(m.Subtasks[0].Facts, "\n")
	if !strings.Contains(joined, "final verify: FAILED") {
		t.Fatalf("regression not recorded on s1:\n%s", joined)
	}
}

func TestRunner_Explore_GreenFieldSkipsStraightToPlan(t *testing.T) {
	client := &runnerClient{responses: []llm.ChatResponse{
		text(validPlanJSON),
		toolCall("write_file", map[string]string{"path": "index.html", "content": "<html>hi</html>"}),
		text("created"),
	}}
	r, _ := newRunner(t, client)

	m := &Mission{ID: "e1", Task: "make a hello page", Phase: PhaseExplore}
	if _, err := r.Run(context.Background(), m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.Map != "" {
		t.Fatalf("green-field workspace should produce no map, got:\n%s", m.Map)
	}
	// The very first model call must be the plan — no annotation worker.
	if client.requests[0].Grammar != PlanGrammar {
		t.Fatal("first call should be the grammar-constrained plan call")
	}
}

func TestRunner_Explore_BuildsMapAndFeedsPlanner(t *testing.T) {
	client := &runnerClient{responses: []llm.ChatResponse{
		text("- entry point is index.html\n- no build step needed"), // annotation worker
		text(validPlanJSON),
		toolCall("write_file", map[string]string{"path": "index.html", "content": "<html>hi</html>"}),
		text("created"),
	}}
	r, dir := newRunner(t, client)

	// Enough real files to clear the green-field threshold.
	for _, p := range []string{"README.md", "old.js", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(r.WS.Root(), p), []byte("content of "+p), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := &Mission{ID: "e2", Task: "make a hello page", Phase: PhaseExplore}
	if _, err := r.Run(context.Background(), m); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(m.Map, "## Directory tree") {
		t.Fatalf("mechanical map missing:\n%s", m.Map)
	}
	if !strings.Contains(m.Map, "entry point is index.html") {
		t.Fatalf("annotation notes missing from map:\n%s", m.Map)
	}
	// The annotation worker is read-only.
	for _, def := range client.requests[0].Tools {
		if def.Name == "write_file" || def.Name == "run_shell" {
			t.Fatalf("annotation worker got mutating tool %s", def.Name)
		}
	}
	// The planner saw the map, not just a bare listing.
	planMsg := client.requests[1].Messages[1].Content
	if !strings.Contains(planMsg, "## Directory tree") {
		t.Fatalf("plan call did not receive the map:\n%s", planMsg)
	}
	// map.md persisted for the human.
	if _, err := os.Stat(filepath.Join(dir, "map.md")); err != nil {
		t.Fatalf("map.md not written: %v", err)
	}
}

func TestRunner_Explore_AnnotationFailureIsNotFatal(t *testing.T) {
	// The annotation worker errors out (unexpected extra call) — the
	// mission must proceed on the mechanical map alone.
	client := &runnerClient{responses: []llm.ChatResponse{
		// annotation worker gets an empty answer, then its retry also empty
		// → worker returns "", which is fine; simplest failure shape here is
		// just letting it produce nothing useful.
		text(""),
		text(""),
		text(validPlanJSON),
		toolCall("write_file", map[string]string{"path": "index.html", "content": "<html>hi</html>"}),
		text("created"),
	}}
	r, _ := newRunner(t, client)
	for _, p := range []string{"README.md", "old.js", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(r.WS.Root(), p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := &Mission{ID: "e3", Task: "make a hello page", Phase: PhaseExplore}
	if _, err := r.Run(context.Background(), m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.Phase != PhaseDone {
		t.Fatalf("Phase = %s, want done despite useless annotation", m.Phase)
	}
	if strings.Contains(m.Map, "Notes (model-generated") {
		t.Fatalf("empty notes should not be appended:\n%s", m.Map)
	}
}

func TestRunner_Review_OKVerdictRecordsNote(t *testing.T) {
	client := &runnerClient{responses: []llm.ChatResponse{
		text(validPlanJSON),
		toolCall("write_file", map[string]string{"path": "index.html", "content": "<html>hi</html>"}),
		text("created"),
		text("OK"), // review worker
		text(`{"verdict":"ok","notes":""}`), // verdict call
	}}
	r, _ := newRunner(t, client)
	r.SkipReview = false

	m := &Mission{ID: "rv1", Task: "make a hello page", Phase: PhasePlan}
	report, err := r.Run(context.Background(), m)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.Notes) != 1 || m.Notes[0] != "review: ok" {
		t.Fatalf("Notes = %v, want [review: ok]", m.Notes)
	}
	if !strings.Contains(report, "review: ok") {
		t.Fatalf("report missing review note:\n%s", report)
	}
	// The verdict call is decision-narrowed and tool-free.
	verdictReq := client.requests[4]
	if verdictReq.Grammar != DecisionGrammar {
		t.Fatal("verdict call must carry the decision grammar")
	}
	if len(verdictReq.Tools) != 0 {
		t.Fatal("verdict call must be tool-free")
	}
	// The review worker is read-only.
	for _, def := range client.requests[3].Tools {
		if def.Name == "write_file" || def.Name == "run_shell" {
			t.Fatalf("review worker got mutating tool %s", def.Name)
		}
	}
}

func TestRunner_Review_GapsSpendReplanBudget(t *testing.T) {
	gapPlan := `{"subtasks":[{"id":"x","milestone":"repair","title":"add stylesheet","goal":"Create style.css and link it from index.html.","acceptance":["style.css exists"],"files_hint":["style.css"],"check":{"type":"file_exists","path":"style.css"}}]}`
	client := &runnerClient{responses: []llm.ChatResponse{
		text(validPlanJSON),
		toolCall("write_file", map[string]string{"path": "index.html", "content": "<html><link href=style.css>hi</html>"}),
		text("created"),
		text("- style.css is referenced by index.html but does not exist"), // review 1
		text(`{"verdict":"gaps","notes":"style.css referenced but missing"}`),
		text(gapPlan), // replan from review gaps
		toolCall("write_file", map[string]string{"path": "style.css", "content": "body{margin:0}"}),
		text("added style.css"),
		text("OK"), // review 2 (second verify cycle)
		text(`{"verdict":"ok","notes":""}`),
	}}
	r, _ := newRunner(t, client)
	r.SkipReview = false

	m := &Mission{ID: "rv2", Task: "make a styled hello page", Phase: PhasePlan}
	if _, err := r.Run(context.Background(), m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.Replans != 1 {
		t.Fatalf("Replans = %d, want 1 (spent on review gaps)", m.Replans)
	}
	if m.Phase != PhaseDone {
		t.Fatalf("Phase = %s, want done", m.Phase)
	}
	if len(m.Subtasks) != 2 || m.Subtasks[1].Status != StatusDone {
		t.Fatalf("gap subtask not executed: %+v", m.Subtasks)
	}
	// The replan call carried the review's gaps as the reason.
	replanMsg := client.requests[5].Messages[1].Content
	if !strings.Contains(replanMsg, "review found gaps") || !strings.Contains(replanMsg, "style.css") {
		t.Fatalf("replan reason missing review gaps:\n%s", replanMsg)
	}
}

func TestRunner_Review_GapsWithoutBudgetStillFinishes(t *testing.T) {
	client := &runnerClient{responses: []llm.ChatResponse{
		text(validPlanJSON),
		toolCall("write_file", map[string]string{"path": "index.html", "content": "<html>hi</html>"}),
		text("created"),
		text("- missing style.css"), // review
		text(`{"verdict":"gaps","notes":"missing style.css"}`),
	}}
	r, _ := newRunner(t, client)
	r.SkipReview = false
	r.MaxReplans = -1 // no budget for the gaps

	m := &Mission{ID: "rv3", Task: "make a hello page", Phase: PhasePlan}
	report, err := r.Run(context.Background(), m)
	if err != nil {
		t.Fatalf("Run: %v — passing checks must not fail on review alone", err)
	}
	if m.Phase != PhaseDone {
		t.Fatalf("Phase = %s, want done", m.Phase)
	}
	if !strings.Contains(report, "review flagged unresolved gaps") {
		t.Fatalf("report must record the unresolved gaps honestly:\n%s", report)
	}
}

// Live failure shape (2026-07-11): koboldcpp died mid-mission and the
// resulting connection errors burned 2 fix attempts and the replan budget
// in seconds, each recorded as a "failed attempt". Infra failures must
// stop the mission resumably instead.
func TestRunner_BackendDeath_StopsResumablyWithoutBurningBudgets(t *testing.T) {
	client := &runnerClient{responses: []llm.ChatResponse{
		text(validPlanJSON),
		// worker's first call: no scripted response → runnerClient errors,
		// exactly like a dead backend
	}}
	r, dir := newRunner(t, client)

	m := &Mission{ID: "i1", Task: "make a hello page", Phase: PhasePlan}
	_, err := r.Run(context.Background(), m)
	if err == nil || !strings.Contains(err.Error(), "backend failure") {
		t.Fatalf("err = %v, want backend-failure error", err)
	}

	if m.Phase != PhaseExecute {
		t.Fatalf("Phase = %s, want execute — infra death must stay resumable, not become failed", m.Phase)
	}
	sub := m.Subtasks[0]
	if sub.Attempts != 1 {
		t.Fatalf("Attempts = %d, want 1 — connection errors must not consume fix attempts", sub.Attempts)
	}
	if m.Replans != 0 {
		t.Fatalf("Replans = %d, want 0", m.Replans)
	}
	if sub.Status != StatusRunning {
		t.Fatalf("Status = %s, want running (resume continues this worker)", sub.Status)
	}

	// The persisted ledger reflects the same resumable state.
	saved, lerr := Load(dir)
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	if saved.Phase != PhaseExecute || saved.Subtasks[0].Status != StatusRunning {
		t.Fatalf("persisted state not resumable: phase=%s status=%s", saved.Phase, saved.Subtasks[0].Status)
	}
}

func TestRunner_BackendDeathDuringPlanning_LeavesPhasePlan(t *testing.T) {
	client := &runnerClient{} // every call fails
	r, _ := newRunner(t, client)

	m := &Mission{ID: "i2", Task: "task", Phase: PhasePlan}
	_, err := r.Run(context.Background(), m)
	if err == nil || !strings.Contains(err.Error(), "backend failure") {
		t.Fatalf("err = %v, want backend-failure error", err)
	}
	if m.Phase != PhasePlan {
		t.Fatalf("Phase = %s, want plan — resume should retry planning", m.Phase)
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

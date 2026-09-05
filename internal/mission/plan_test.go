package mission

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/llm"
)

type scriptClient struct {
	responses []string // plain content, no tool calls
	requests  []llm.ChatRequest
}

func (c *scriptClient) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.requests) > len(c.responses) {
		return llm.ChatResponse{}, fmt.Errorf("unexpected model call #%d", len(c.requests))
	}
	return llm.ChatResponse{Message: llm.Message{
		Role:    llm.RoleAssistant,
		Content: c.responses[len(c.requests)-1],
	}}, nil
}

const validPlanJSON = `{"subtasks":[{"id":"whatever","milestone":"scaffold","title":"create page","goal":"Create index.html with a hello message.","acceptance":["index.html exists and contains hello"],"files_hint":["index.html"],"check":{"type":"file_exists","path":"index.html"}}]}`

func TestGeneratePlan_ValidFirstTry(t *testing.T) {
	client := &scriptClient{responses: []string{validPlanJSON}}
	subtasks, warnings, err := GeneratePlan(context.Background(), client, PlanRequest{
		Task:          "make a hello page",
		FileListing:   "(workspace is empty)",
		ExistingFiles: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("GeneratePlan: %v", err)
	}
	if len(subtasks) != 1 {
		t.Fatalf("subtasks = %d, want 1", len(subtasks))
	}
	if subtasks[0].ID != "s1" {
		t.Fatalf("ID = %q, want harness-assigned s1 regardless of model output", subtasks[0].ID)
	}
	if subtasks[0].Status != StatusPending {
		t.Fatalf("Status = %q, want pending", subtasks[0].Status)
	}
	// index.html doesn't exist → new-file intent surfaced as a warning.
	if len(warnings) != 1 || !strings.Contains(warnings[0], "index.html") {
		t.Fatalf("warnings = %v, want one new-file warning for index.html", warnings)
	}

	req := client.requests[0]
	if req.Grammar != PlanGrammar {
		t.Fatal("plan call must carry the plan grammar")
	}
	if len(req.Tools) != 0 {
		t.Fatal("plan call must be tool-free — grammar+tools interaction is deliberately avoided")
	}
	if req.Temperature != planTemperature {
		t.Fatalf("Temperature = %v, want %v", req.Temperature, planTemperature)
	}
}

func TestGeneratePlan_RetriesOnceWithValidationErrors(t *testing.T) {
	bad := `{"subtasks":[{"id":"s1","milestone":"m","title":"t","goal":"do it","acceptance":["done"],"files_hint":["a.txt"],"check":{"type":"shell","cmd":"echo ok"}}]}`
	client := &scriptClient{responses: []string{bad, validPlanJSON}}

	subtasks, _, err := GeneratePlan(context.Background(), client, PlanRequest{
		Task: "task", ExistingFiles: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("GeneratePlan: %v", err)
	}
	if len(subtasks) != 1 || subtasks[0].Check.Type != "file_exists" {
		t.Fatalf("expected the corrected plan, got %+v", subtasks)
	}
	if len(client.requests) != 2 {
		t.Fatalf("calls = %d, want 2 (one retry)", len(client.requests))
	}
	// The retry must carry the specific validator complaint.
	last := client.requests[1].Messages[len(client.requests[1].Messages)-1]
	if !strings.Contains(last.Content, "always succeeds") {
		t.Fatalf("retry message should quote the echo-check error, got: %s", last.Content)
	}
}

func TestGeneratePlan_FailsLoudlyAfterTwoBadAttempts(t *testing.T) {
	bad := `{"subtasks":[]}`
	client := &scriptClient{responses: []string{bad, bad}}
	_, _, err := GeneratePlan(context.Background(), client, PlanRequest{
		Task: "task", ExistingFiles: map[string]bool{},
	})
	if err == nil {
		t.Fatal("expected loud failure after two invalid plans — never loop on plan generation")
	}
	if len(client.requests) != 2 {
		t.Fatalf("calls = %d, want exactly 2", len(client.requests))
	}
}

func TestGeneratePlan_EditNoteReachesTheModel(t *testing.T) {
	client := &scriptClient{responses: []string{validPlanJSON}}
	_, _, err := GeneratePlan(context.Background(), client, PlanRequest{
		Task: "task", ExistingFiles: map[string]bool{}, EditNote: "split it into two files",
	})
	if err != nil {
		t.Fatalf("GeneratePlan: %v", err)
	}
	var found bool
	for _, msg := range client.requests[0].Messages {
		if strings.Contains(msg.Content, "split it into two files") {
			found = true
		}
	}
	if !found {
		t.Fatal("the human's edit note never reached the planner")
	}
}

func TestParseAndValidate_StructuralRules(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string // substring of the expected error
	}{
		{"no subtasks", `{"subtasks":[]}`, "no subtasks"},
		{"not json", `nope`, "not valid JSON"},
		{"empty goal", `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":" ","acceptance":["x"],"files_hint":[],"check":{"type":"none"}}]}`, "goal is empty"},
		{"no acceptance", `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":[],"files_hint":[],"check":{"type":"none"}}]}`, "no acceptance"},
		{"files without check", `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["a.txt"],"check":{"type":"none"}}]}`, "no check"},
		{"echo check", `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["a.txt"],"check":{"type":"shell","cmd":"echo done"}}]}`, "always succeeds"},
		{"ls check (live failure shape)", `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["a.txt"],"check":{"type":"shell","cmd":"ls -l"}}]}`, "always succeeds"},
		{"dir check", `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["a.txt"],"check":{"type":"shell","cmd":"dir"}}]}`, "always succeeds"},
		{"true check", `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["a.txt"],"check":{"type":"shell","cmd":"true"}}]}`, "always succeeds"},
		{"pathless file check", `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["a.txt"],"check":{"type":"file_exists"}}]}`, "no path"},
		{"bad http url", `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["a.txt"],"check":{"type":"http","url":"localhost:3000"}}]}`, "http(s) URL"},
		{"unknown check", `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["a.txt"],"check":{"type":"vibes"}}]}`, "unknown check type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errs := parseAndValidate(tc.json, "", map[string]bool{})
			if len(errs) == 0 {
				t.Fatalf("expected a validation error containing %q", tc.want)
			}
			joined := strings.Join(errs, "; ")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("errors = %s, want substring %q", joined, tc.want)
			}
		})
	}
}

// Both shapes below are live failures from the 2026-07-11 Qwen run: a
// check against a directory burned three workers on an unsatisfiable
// condition, and a check against a pre-existing file auto-passed having
// verified nothing.
func TestParseAndValidate_FileExistsOnDirectory_Rejected(t *testing.T) {
	plan := `{"subtasks":[{"id":"a","milestone":"m","title":"analyze","goal":"g","acceptance":["x"],"files_hint":[],"check":{"type":"file_exists","path":"internal/tools"}}]}`
	existing := map[string]bool{"internal/tools/registry.go": true, "internal/tools/fs.go": true}
	_, _, errs := parseAndValidate(plan, "", existing)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, ";"), "is a directory") {
		t.Fatalf("errs = %v, want directory rejection", errs)
	}
}

func TestParseAndValidate_FileExistsOnPreExistingFile_Rejected(t *testing.T) {
	plan := `{"subtasks":[{"id":"a","milestone":"m","title":"read the plan","goal":"g","acceptance":["x"],"files_hint":["plan.md"],"check":{"type":"file_exists","path":"plan.md"}}]}`
	_, _, errs := parseAndValidate(plan, "", map[string]bool{"plan.md": true})
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, ";"), "verifies nothing") {
		t.Fatalf("errs = %v, want vacuous-check rejection", errs)
	}
}

// Live shape (Qwen3.6 run): four subtasks all checked file_exists plan.md
// — s2-s4 became unverifiable the moment s1 created the file.
func TestParseAndValidate_DuplicateChecks_Rejected(t *testing.T) {
	plan := `{"subtasks":[
		{"id":"a","milestone":"m","title":"t1","goal":"g1","acceptance":["x"],"files_hint":["plan.md"],"check":{"type":"file_exists","path":"plan.md"}},
		{"id":"b","milestone":"m","title":"t2","goal":"g2","acceptance":["y"],"files_hint":["plan.md"],"check":{"type":"file_exists","path":"plan.md"}}]}`
	_, _, errs := parseAndValidate(plan, "", map[string]bool{})
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, ";"), "same check as s1") {
		t.Fatalf("errs = %v, want duplicate-check rejection naming s1", errs)
	}
}

func TestParseAndValidate_DistinctChecks_Accepted(t *testing.T) {
	plan := `{"subtasks":[
		{"id":"a","milestone":"m","title":"t1","goal":"g1","acceptance":["x"],"files_hint":["index.html"],"check":{"type":"file_exists","path":"index.html"}},
		{"id":"b","milestone":"m","title":"t2","goal":"g2","acceptance":["y"],"files_hint":["style.css"],"check":{"type":"file_exists","path":"style.css"}}]}`
	_, _, errs := parseAndValidate(plan, "", map[string]bool{})
	if len(errs) != 0 {
		t.Fatalf("distinct checks should pass, got: %v", errs)
	}
}

func TestParseAndValidate_FileExistsOnNewFile_Accepted(t *testing.T) {
	plan := `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["style.css"],"check":{"type":"file_exists","path":"style.css"}}]}`
	_, _, errs := parseAndValidate(plan, "", map[string]bool{"index.html": true})
	if len(errs) != 0 {
		t.Fatalf("new-file check should be accepted, got: %v", errs)
	}
}

func TestParseAndValidate_ExistingFileNoWarning(t *testing.T) {
	plan := `{"subtasks":[{"id":"a","milestone":"m","title":"t","goal":"g","acceptance":["x"],"files_hint":["main.go"],"check":{"type":"shell","cmd":"go build ./..."}}]}`
	_, warnings, errs := parseAndValidate(plan, "", map[string]bool{"main.go": true})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(warnings) != 0 {
		t.Fatalf("existing file flagged as new: %v", warnings)
	}
}

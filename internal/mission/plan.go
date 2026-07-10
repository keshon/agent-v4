package mission

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-v4/internal/llm"
	"agent-v4/internal/prompts"
)

// planTemperature is deliberately low: structured output wants stability,
// but not 0.0 — greedy sampling under a grammar makes weak models loop on
// repeated tokens.
const planTemperature = 0.3

// PlanRequest carries everything one plan-generation call needs.
type PlanRequest struct {
	Task          string
	FileListing   string          // rendered workspace listing, budget-capped
	ExistingFiles map[string]bool // real relative paths, for hallucination checks
	EditNote      string          // human rejection note from the approval gate, if any
	MaxTokens     int
}

// GeneratePlan makes one tool-free, grammar-constrained model call and
// validates the result structurally in Go. Grammar guarantees syntax; the
// validator guarantees structure; the human approval gate (in the Runner)
// handles semantics. One retry carrying the specific validation errors,
// then a loud failure with the raw output attached — never a loop.
func GeneratePlan(ctx context.Context, client llm.Client, req PlanRequest) (subtasks []Subtask, warnings []string, err error) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompts.MissionPlan},
		{Role: llm.RoleUser, Content: fmt.Sprintf(prompts.MissionPlanTask, req.Task, req.FileListing)},
	}
	if req.EditNote != "" {
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf(prompts.MissionPlanEdit, req.EditNote),
		})
	}

	const attempts = 2
	var lastRaw string
	var lastErrs []string
	for i := 0; i < attempts; i++ {
		if i > 0 {
			messages = append(messages,
				llm.Message{Role: llm.RoleAssistant, Content: lastRaw},
				llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf(prompts.MissionPlanRetry, strings.Join(lastErrs, "\n"))},
			)
		}
		resp, err := client.Chat(ctx, llm.ChatRequest{
			Messages:    messages,
			Grammar:     PlanGrammar,
			Temperature: planTemperature,
			MaxTokens:   req.MaxTokens,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("plan call: %w", err)
		}
		lastRaw = resp.Message.Content

		subtasks, warnings, lastErrs = parseAndValidate(lastRaw, req.ExistingFiles)
		if len(lastErrs) == 0 {
			return subtasks, warnings, nil
		}
	}
	return nil, nil, fmt.Errorf("plan rejected by validation after %d attempts: %s\nraw output:\n%s",
		attempts, strings.Join(lastErrs, "; "), lastRaw)
}

func parseAndValidate(raw string, existing map[string]bool) (subtasks []Subtask, warnings, errs []string) {
	var doc struct {
		Subtasks []Subtask `json:"subtasks"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &doc); err != nil {
		return nil, nil, []string{fmt.Sprintf("output is not valid JSON: %v", err)}
	}
	subtasks = doc.Subtasks

	if len(subtasks) == 0 {
		return nil, nil, []string{"plan has no subtasks"}
	}
	if len(subtasks) > 8 {
		return nil, nil, []string{fmt.Sprintf("plan has %d subtasks, maximum is 8", len(subtasks))}
	}

	for i := range subtasks {
		s := &subtasks[i]
		// IDs and statuses are harness-owned — overwrite instead of
		// validating; a weak model shouldn't be able to break sequencing.
		s.ID = fmt.Sprintf("s%d", i+1)
		s.Status = StatusPending
		s.Attempts = 0
		s.Facts = nil
		s.Summary = ""

		if strings.TrimSpace(s.Goal) == "" {
			errs = append(errs, fmt.Sprintf("%s: goal is empty", s.ID))
		}
		if strings.TrimSpace(s.Title) == "" {
			errs = append(errs, fmt.Sprintf("%s: title is empty", s.ID))
		}
		if len(s.Acceptance) == 0 {
			errs = append(errs, fmt.Sprintf("%s: no acceptance criteria", s.ID))
		}

		errs = append(errs, validateCheck(s)...)

		// files_hint paths that don't exist yet are fine — most plans
		// create files — but they must be surfaced to the human as
		// new-file intents, never silently trusted as existing.
		for _, p := range s.FilesHint {
			if !existing[strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "./")] {
				warnings = append(warnings, fmt.Sprintf("%s: %s does not exist yet (will be created)", s.ID, p))
			}
		}
	}
	return subtasks, warnings, errs
}

func validateCheck(s *Subtask) (errs []string) {
	c := s.Check
	switch c.Type {
	case "none":
		if len(s.FilesHint) > 0 {
			errs = append(errs, fmt.Sprintf("%s: touches files (%s) but has no check — pick file_exists or shell",
				s.ID, strings.Join(s.FilesHint, ", ")))
		}
	case "file_exists":
		if strings.TrimSpace(c.Path) == "" {
			errs = append(errs, fmt.Sprintf("%s: file_exists check has no path", s.ID))
		}
	case "shell":
		cmd := strings.TrimSpace(strings.ToLower(c.Cmd))
		if cmd == "" {
			errs = append(errs, fmt.Sprintf("%s: shell check has no cmd", s.ID))
		} else if strings.HasPrefix(cmd, "echo") || cmd == "true" || strings.HasPrefix(cmd, "exit 0") {
			errs = append(errs, fmt.Sprintf("%s: shell check %q always succeeds and verifies nothing", s.ID, c.Cmd))
		}
	case "http":
		if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
			errs = append(errs, fmt.Sprintf("%s: http check needs an http(s) URL, got %q", s.ID, c.URL))
		}
	default:
		errs = append(errs, fmt.Sprintf("%s: unknown check type %q", s.ID, c.Type))
	}
	return errs
}

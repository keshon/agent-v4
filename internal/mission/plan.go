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
	return generateSubtasks(ctx, client, messages, req)
}

// ReplanRequest carries everything a replan call needs beyond PlanRequest:
// the execution record so far (statuses + measured facts) and the reason
// the old plan stopped.
type ReplanRequest struct {
	PlanRequest
	Record string // Mission.RenderReport() — statuses and facts, verbatim
	Reason string // why replanning: the failing check output, review gaps
}

// GenerateReplan asks for a plan covering only the remaining work. The
// caller keeps the already-executed subtasks frozen (with their measured
// history) and appends the result — the model never gets to rewrite what
// already happened.
func GenerateReplan(ctx context.Context, client llm.Client, req ReplanRequest) (subtasks []Subtask, warnings []string, err error) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompts.MissionPlan},
		{Role: llm.RoleUser, Content: fmt.Sprintf(prompts.MissionReplan,
			req.Task, req.Record, req.Reason, req.FileListing)},
	}
	return generateSubtasks(ctx, client, messages, req.PlanRequest)
}

// generateSubtasks is the shared call-validate-retry core for plan and
// replan generation.
func generateSubtasks(ctx context.Context, client llm.Client, messages []llm.Message, req PlanRequest) (subtasks []Subtask, warnings []string, err error) {
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
			// A chat failure here is the backend, not the plan — mark it
			// infra so the Runner stops resumably instead of failing the
			// mission (or burning the replan budget it was called with).
			return nil, nil, fmt.Errorf("%w: plan call: %v", errInfra, err)
		}
		lastRaw = resp.Message.Content

		subtasks, warnings, lastErrs = parseAndValidate(lastRaw, req.Task, req.ExistingFiles)
		if len(lastErrs) == 0 {
			return subtasks, warnings, nil
		}
	}
	return nil, nil, fmt.Errorf("plan rejected by validation after %d attempts: %s\nraw output:\n%s",
		attempts, strings.Join(lastErrs, "; "), lastRaw)
}

func parseAndValidate(raw, task string, existing map[string]bool) (subtasks []Subtask, warnings, errs []string) {
	raw = stripThink(raw)
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

	// A check identical to an earlier subtask's check cannot distinguish
	// its own subtask's work — live shape: four subtasks all checked
	// "file_exists: plan.md", so once s1 created the file, s2-s4 were
	// unverifiable.
	checkOwner := make(map[string]string)

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

		if msg := readOnlySubtaskErr(s, existing); msg != "" {
			errs = append(errs, msg)
		}

		errs = append(errs, validateCheck(s, existing)...)

		if s.Check.Type != "" && s.Check.Type != "none" {
			fp := s.Check.fingerprint()
			if owner, dup := checkOwner[fp]; dup {
				errs = append(errs, fmt.Sprintf("%s: has the same check as %s — a check must verify its "+
					"OWN subtask's work. Merge the two subtasks into one, or give this one a check that "+
					"detects its specific contribution (e.g. content_contains for a symbol it adds)",
					s.ID, owner))
			} else {
				checkOwner[fp] = s.ID
			}
		}

		// files_hint paths that don't exist yet are fine — most plans
		// create files — but they must be surfaced to the human as
		// new-file intents, never silently trusted as existing.
		for _, p := range s.FilesHint {
			if !existing[normalizePlanPath(p)] {
				warnings = append(warnings, fmt.Sprintf("%s: %s does not exist yet (will be created)", s.ID, p))
			}
		}
	}

	// One blob for a multi-file task is the reactive loop with ceremony.
	if msg := planTooCoarse(task, subtasks); msg != "" {
		errs = append(errs, msg)
	}
	return subtasks, warnings, errs
}

// stripThink drops Qwen-style <think>…</think> wrappers. When the plan
// grammar is ignored by the backend, thinking models dump thousands of
// tokens of reasoning before the JSON — Unmarshal then fails or the
// validator never sees the real plan.
func stripThink(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			return strings.TrimSpace(s)
		}
		rest := s[start+len("<think>"):]
		end := strings.Index(rest, "</think>")
		if end < 0 {
			return strings.TrimSpace(s[:start])
		}
		s = s[:start] + rest[end+len("</think>"):]
	}
}

// readOnlySubtaskErr rejects "read/understand the plan" units. Workers
// already have read tools; anything "understood" dies with the worker.
// Live shape 2026-07-14: s1 read PLAN.md with check none → retry "fixed"
// it to file_exists on the already-existing file → still rejected.
func readOnlySubtaskErr(s *Subtask, existing map[string]bool) string {
	if !allHintsExist(s.FilesHint, existing) {
		return ""
	}
	if len(s.FilesHint) == 0 {
		return ""
	}
	low := strings.ToLower(s.Title + " " + s.Goal)
	readish := strings.Contains(low, "read ") || strings.Contains(low, "understand") ||
		strings.Contains(low, "analyz") || strings.Contains(low, "review") ||
		strings.Contains(low, "inspect") || strings.Contains(low, "study ") ||
		strings.Contains(low, "look at")
	noneOrVacuous := s.Check.Type == "" || s.Check.Type == "none" ||
		(s.Check.Type == "file_exists" && existing[normalizePlanPath(s.Check.Path)])
	if readish || noneOrVacuous {
		return fmt.Sprintf("%s: read-only subtask — REMOVE it. Workers read existing files themselves; "+
			"only plan subtasks that CREATE or MODIFY files. Put any \"read the plan\" work into the "+
			"first implementation subtask's goal", s.ID)
	}
	return ""
}

func allHintsExist(hints []string, existing map[string]bool) bool {
	if len(hints) == 0 {
		return false
	}
	for _, p := range hints {
		if !existing[normalizePlanPath(p)] {
			return false
		}
	}
	return true
}

func validateCheck(s *Subtask, existing map[string]bool) (errs []string) {
	c := s.Check
	switch c.Type {
	case "none":
		if len(s.FilesHint) > 0 && !allHintsExist(s.FilesHint, existing) {
			errs = append(errs, fmt.Sprintf("%s: touches files (%s) but has no check — pick file_exists, content_contains, or shell",
				s.ID, strings.Join(s.FilesHint, ", ")))
		}
	case "file_exists":
		path := normalizePlanPath(c.Path)
		switch {
		case strings.TrimSpace(path) == "":
			errs = append(errs, fmt.Sprintf("%s: file_exists check has no path", s.ID))
		case existing[path]:
			// Live failure shape: a plan checked file_exists on plan.md,
			// which already existed — the subtask "passed" having done
			// nothing. A check that is already true before any work
			// verifies nothing.
			errs = append(errs, fmt.Sprintf("%s: file_exists check on %q verifies nothing — the file "+
				"already exists. Point it at a file this subtask CREATES, use content_contains for a "+
				"symbol you add, or use a shell check that proves the change", s.ID, c.Path))
		case isDirOf(existing, path):
			// Live failure shape: a plan checked file_exists on
			// internal/tools (a directory) — unsatisfiable, and the fix
			// loop burned three workers trying to satisfy it.
			errs = append(errs, fmt.Sprintf("%s: file_exists check path %q is a directory — "+
				"the check can never pass. Name a specific file", s.ID, c.Path))
		}
	case "content_contains":
		path := normalizePlanPath(c.Path)
		needle := strings.TrimSpace(strings.Trim(c.Contains, `"'`))
		s.Check.Contains = needle // normalize planner-escaped quotes like "\"vite\""
		switch {
		case strings.TrimSpace(path) == "":
			errs = append(errs, fmt.Sprintf("%s: content_contains check has no path", s.ID))
		case isDirOf(existing, path):
			errs = append(errs, fmt.Sprintf("%s: content_contains path %q is a directory — name a file", s.ID, c.Path))
		case needle == "":
			errs = append(errs, fmt.Sprintf("%s: content_contains check has empty contains", s.ID))
		case len(needle) < 3:
			errs = append(errs, fmt.Sprintf("%s: content_contains %q is too short — use a distinctive symbol (function name, export, tag)", s.ID, c.Contains))
		}
	case "shell":
		cmd := strings.TrimSpace(strings.ToLower(c.Cmd))
		if cmd == "" {
			errs = append(errs, fmt.Sprintf("%s: shell check has no cmd", s.ID))
		} else if vacuousShellCheck(cmd) {
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

// planTooCoarse rejects "one subtask does everything" when the task
// already names multiple deliverable files — that shape is the reactive
// loop with extra steps, not a real decomposition.
func planTooCoarse(task string, subtasks []Subtask) string {
	if len(subtasks) != 1 {
		return ""
	}
	files := FileMentions(task)
	if len(files) < 2 {
		return ""
	}
	return fmt.Sprintf("task names %d files (%s) but plan has only 1 subtask — split into one subtask per file/module (merge only tightly-coupled edits to the SAME file)",
		len(files), strings.Join(files, ", "))
}

// normalizePlanPath makes a model-written path comparable against the
// walk-produced existing-files set: forward slashes, no ./ prefix.
func normalizePlanPath(p string) string {
	return strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "./")
}

// isDirOf reports whether path is a directory, judged purely from the
// existing-files set: it's a directory iff some real file lives under it.
func isDirOf(existing map[string]bool, path string) bool {
	prefix := strings.TrimRight(path, "/") + "/"
	for f := range existing {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	return false
}

// vacuousShellCheck flags commands that succeed regardless of whether any
// work was done — a check that can't fail verifies nothing. Matched on
// the first word (lowercased): a real weak-model plan produced `ls -l` as
// a "check" and it passed vacuously, so this is a live failure shape, not
// paranoia.
func vacuousShellCheck(cmd string) bool {
	first := cmd
	if i := strings.IndexAny(first, " \t"); i >= 0 {
		first = first[:i]
	}
	switch first {
	case "echo", "ls", "dir", "pwd", "cd", "true", "cat", "type", "whoami", "date", "exit":
		return true
	}
	return false
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"agent-v4/internal/llm"
	"agent-v4/internal/prompts"
)

// Config wires together everything an Agent needs. There is exactly one
// loop implementation here — no separate "policy" or "runtime" layer
// sitting next to it making the same decisions twice.
type Config struct {
	Client llm.Client
	Tools  *Registry
	System string

	// MaxSteps bounds how many model round-trips a single Run performs.
	MaxSteps int

	// MaxTokens caps generation length per response. Left at zero, New
	// defaults this to 8192 — large single-shot generations (a full
	// HTML+CSS+JS file in one write_file call) silently truncate mid-JSON
	// without enough budget, which looks like a model failure but is
	// actually a missing request parameter.
	MaxTokens int

	// ContextLimit is the backend's real context window size, e.g. from
	// KoboldClient.MaxContextLength. Zero disables budget tracking
	// entirely — there's no model-agnostic default that means anything,
	// so this has to come from the backend, not a guess.
	ContextLimit int

	// MaxStuckSteps is how many consecutive unproductive steps are
	// tolerated before the agent gets nudged to try a different approach.
	// A step counts as unproductive if every tool call in it errored, OR
	// if it's an exact repeat of the previous step's calls — a tool that
	// "succeeds" by returning the same listing for the third time in a
	// row is just as stuck as one that keeps erroring.
	MaxStuckSteps int

	// MaxExploratorySteps is how many consecutive steps with zero
	// Exclusive (mutating) tool calls are tolerated before a one-time
	// nudge suggests broadening the search instead of indefinitely
	// narrowing the same dead-end query. Distinct from MaxStuckSteps:
	// the model can be making "progress" by this loop's definition (new
	// list_files/grep_files calls, no errors, no exact repeats) while
	// still going nowhere — burning steps refining a search instead of
	// converging on an answer. Zero means "use the default of 8"; to
	// disable this nudge entirely, set it higher than MaxSteps.
	MaxExploratorySteps int

	// SkipVerify disables the self-check pass that normally runs once
	// before Run returns: the model is asked to re-examine its own work
	// against the original task before declaring victory. Leave this
	// false unless the extra round-trip genuinely isn't worth it for your
	// use case — a local model "succeeding" by writing an empty file is
	// exactly the kind of mistake this catches some of the time.
	SkipVerify bool

	// OnStep, if set, is called after every model response — for
	// logging/debugging without baking observability into the loop.
	OnStep func(step int, msg llm.Message)

	// StateFile, if set, gets the full message history written to it
	// (as JSON) after every step. If the process dies or the run hits
	// MaxSteps, the file on disk reflects the last completed step —
	// load it with LoadState and continue with Resume instead of losing
	// everything and starting over.
	StateFile string

	// MutatingTools names the tools that actually change the filesystem.
	// Used only so the self-check round can state a hard fact ("0 of
	// these succeeded this run") instead of trusting the model's own
	// claim that it saved something — a model can describe writing a
	// file in its response text without ever calling the tool that
	// actually does it. Defaults to this project's own file tools.
	MutatingTools []string

	// Verify, if set, runs once at the self-check checkpoint (a real
	// build/lint/test command, e.g. "go build ./... && go test ./...")
	// and its real output gets folded into the verify message as ground
	// truth — the same "don't trust the self-report" pattern as
	// MutatingTools, extended from "did you save the file" to "does the
	// code actually work." Nil means skip this (most projects don't have
	// one canned command that makes sense).
	Verify func(ctx context.Context) (output string, ok bool)
}

type Agent struct {
	cfg Config
}

func New(cfg Config) *Agent {
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 25
	}
	if cfg.MaxStuckSteps == 0 {
		cfg.MaxStuckSteps = 2
	}
	if cfg.MaxExploratorySteps == 0 {
		cfg.MaxExploratorySteps = 8
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 8192
	}
	if len(cfg.MutatingTools) == 0 {
		cfg.MutatingTools = []string{"write_file", "patch_file", "patch_lines", "move_file"}
	}
	return &Agent{cfg: cfg}
}

// Run executes the agent loop on a single task and returns the model's
// final answer once it stops requesting tools.
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
	return a.run(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: a.cfg.System},
		{Role: llm.RoleUser, Content: task},
	})
}

// Resume continues from a previously saved history (see LoadState) after
// a run was interrupted — crashed, killed, or hit MaxSteps. note is
// appended as a fresh user message before continuing; pass something like
// "Your previous attempt was interrupted before finishing. Check the
// current state of the workspace before assuming anything, then finish
// the task." A plain summary-of-what-happened isn't required — the full
// history is already there, the model can re-read it.
func (a *Agent) Resume(ctx context.Context, history []llm.Message, note string) (string, error) {
	history = append(history, llm.Message{Role: llm.RoleUser, Content: note})
	return a.run(ctx, history)
}

// PausedOnQuestion reports whether the last message in history is an
// assistant message containing an unanswered ask_user tool call. This is
// the correct way to distinguish "interrupted by a question" from
// "interrupted by a crash/timeout" when deciding which resume path to
// take.
func PausedOnQuestion(history []llm.Message) (callID string, question string, ok bool) {
	if len(history) == 0 {
		return "", "", false
	}
	last := history[len(history)-1]
	if last.Role != llm.RoleAssistant {
		return "", "", false
	}
	for _, tc := range last.ToolCalls {
		if tc.Name == "ask_user" {
			var args struct {
				Question string `json:"question"`
			}
			json.Unmarshal(tc.Arguments, &args)
			return tc.ID, args.Question, true
		}
	}
	return "", "", false
}

// ResumeWithAnswer continues from a history that ended with an unanswered
// ask_user tool call. answer is synthesized as the proper role:tool,
// tool_call_id message before the loop resumes — NOT as a plain user
// message, which would produce a wire-invalid conversation (a backend
// expects every tool_call to get its matching tool-result before anything
// else, not a naked user message after an unanswered call).
func (a *Agent) ResumeWithAnswer(ctx context.Context, history []llm.Message, callID, answer string) (string, error) {
	history = append(history, llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: callID,
		Content:    answer,
	})
	return a.run(ctx, history)
}

// LoadState reads a history previously written via Config.StateFile.
func LoadState(path string) ([]llm.Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}
	var history []llm.Message
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, fmt.Errorf("decode state file: %w", err)
	}
	return history, nil
}

// saveState is best-effort: a failed snapshot write should never abort a
// run that's otherwise working fine.
func (a *Agent) saveState(history []llm.Message) {
	if a.cfg.StateFile == "" {
		return
	}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(a.cfg.StateFile); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	_ = os.WriteFile(a.cfg.StateFile, data, 0o644)
}

func (a *Agent) run(ctx context.Context, history []llm.Message) (string, error) {
	stuckSteps := 0
	verifiedOnce := false
	lastSignature := ""
	warnedThreshold := 0
	lastPromptTokens := 0
	mutatingSucceeded := 0
	exploratorySteps := 0
	searchFatigueWarned := false

	for step := 0; step < a.cfg.MaxSteps; step++ {
		resp, err := a.cfg.Client.Chat(ctx, llm.ChatRequest{
			Messages:  history,
			Tools:     a.cfg.Tools.Defs(),
			MaxTokens: a.effectiveMaxTokens(lastPromptTokens),
		})
		if err != nil {
			return "", fmt.Errorf("step %d: chat: %w", step, err)
		}
		if a.cfg.OnStep != nil {
			a.cfg.OnStep(step, resp.Message)
		}
		history = append(history, resp.Message)
		budgetNudge := a.budgetWarning(resp.Usage, &warnedThreshold)
		if resp.Usage.PromptTokens > 0 {
			lastPromptTokens = resp.Usage.PromptTokens
		}
		a.saveState(history)

		if len(resp.Message.ToolCalls) == 0 {
			// A model can write text that *looks* like a tool call
			// ("<|tool_call>call:write_file{...}") instead of making a
			// real structured one. Nothing executes, but the model often
			// then believes — and later claims — that it did. Catch this
			// before it's mistaken for a genuine finish.
			if looksLikeLeakedToolCall(resp.Message.Content) {
				history = append(history, llm.Message{
					Role:    llm.RoleUser,
					Content: prompts.LeakDetected,
				})
				stuckSteps++
				if stuckSteps >= a.cfg.MaxStuckSteps {
					history = append(history, llm.Message{
						Role:    llm.RoleUser,
						Content: prompts.LeakRepeated,
					})
					stuckSteps = 0
				}
				continue
			}

			if !a.cfg.SkipVerify && !verifiedOnce {
				verifiedOnce = true
				verifyMsg := prompts.Verify
				if mutatingSucceeded == 0 {
					verifyMsg += fmt.Sprintf(prompts.VerifyZeroWrites,
						strings.Join(a.cfg.MutatingTools, "/"))
				}
				if a.cfg.Verify != nil {
					if out, ok := a.cfg.Verify(ctx); ok {
						if out == "" {
							out = "(no output)"
						}
						verifyMsg += fmt.Sprintf(prompts.VerifyCheckResult, out)
					}
				}
				history = append(history, llm.Message{Role: llm.RoleUser, Content: verifyMsg})
				if budgetNudge != "" {
					history = append(history, llm.Message{Role: llm.RoleUser, Content: budgetNudge})
				}
				continue
			}
			return resp.Message.Content, nil
		}

		signature := callSignature(resp.Message.ToolCalls)
		repeat := signature == lastSignature
		lastSignature = signature

		// Tool calls within one step are scheduled by Tool.Mode(), not run
		// uniformly. Concurrent calls (reads, independent delegate_task
		// calls, read-only checks) run together via goroutines — this is
		// what makes multiple delegate_task calls in one step actually
		// run in parallel. Exclusive calls (anything that mutates the
		// workspace, or run_shell's unanalyzable arbitrary command) run
		// one at a time and never overlap with the Concurrent batch or
		// each other — a model issuing write_file then patch_file on the
		// same file in one step can rely on that order; two reads can't
		// race a write of the same path either way, in any order.
		type callResult struct {
			content string
			err     error
		}
		results := make([]callResult, len(resp.Message.ToolCalls))

		var concurrentIdx, exclusiveIdx []int
		for i, call := range resp.Message.ToolCalls {
			if a.cfg.Tools.ModeOf(call.Name) == Concurrent {
				concurrentIdx = append(concurrentIdx, i)
			} else {
				exclusiveIdx = append(exclusiveIdx, i)
			}
		}

		runOne := func(i int) {
			call := resp.Message.ToolCalls[i]
			content, err := a.cfg.Tools.Run(ctx, call.Name, call.Arguments)
			results[i] = callResult{content: content, err: err}
		}

		if len(concurrentIdx) > 0 {
			var wg sync.WaitGroup
			for _, i := range concurrentIdx {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					runOne(i)
				}(i)
			}
			wg.Wait()
		}
		for _, i := range exclusiveIdx {
			runOne(i)
		}

		mutatingBefore := mutatingSucceeded
		progressed := false
		for i, call := range resp.Message.ToolCalls {
			content := results[i].content
			if results[i].err != nil {
				content = "error: " + results[i].err.Error()
			} else {
				progressed = true
				if containsStr(a.cfg.MutatingTools, call.Name) {
					mutatingSucceeded++
				}
			}
			history = append(history, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Content:    content,
			})
		}

		if mutatingSucceeded > mutatingBefore {
			exploratorySteps = 0
		} else {
			exploratorySteps++
		}
		if exploratorySteps >= a.cfg.MaxExploratorySteps && !searchFatigueWarned {
			searchFatigueWarned = true
			history = append(history, llm.Message{Role: llm.RoleUser, Content: prompts.SearchFatigue})
		}

		if budgetNudge != "" {
			history = append(history, llm.Message{Role: llm.RoleUser, Content: budgetNudge})
		}
		a.saveState(history)

		if progressed && !repeat {
			stuckSteps = 0
			continue
		}

		stuckSteps++
		if stuckSteps >= a.cfg.MaxStuckSteps {
			nudge := prompts.StuckFailing
			if repeat {
				nudge = prompts.StuckRepeating
			}
			history = append(history, llm.Message{Role: llm.RoleUser, Content: nudge})
			stuckSteps = 0
			lastSignature = "" // the nudge itself breaks the repeat chain
		}
	}

	return "", fmt.Errorf("reached max steps (%d) without finishing", a.cfg.MaxSteps)
}

// budgetWarning returns a one-time nudge when usage crosses a new context
// threshold, or "" if there's nothing new to report. *warned tracks the
// highest percentage already warned about, so crossing 75% doesn't nag
// every single step afterward — only the next, higher threshold matters.
// effectiveMaxTokens caps Config.MaxTokens against the room actually left
// in the context window, using the prompt size from the previous call as
// a stand-in for "about how big the next prompt will be" (history only
// grows a little per step, so this is a safe approximation, not the exact
// next value). Without this, asking for e.g. 32768 tokens of generation
// when the prompt itself is already using real context space causes
// exactly what koboldcpp warns about: "most of the context will be
// removed" — the backend starts evicting the prompt mid-generation and
// produces incoherent, runaway output instead of erroring cleanly.
func (a *Agent) effectiveMaxTokens(lastPromptTokens int) int {
	if a.cfg.ContextLimit <= 0 {
		return a.cfg.MaxTokens
	}
	promptEstimate := lastPromptTokens
	if promptEstimate <= 0 {
		// First call, nothing measured yet. System prompt + tool schemas
		// alone are routinely 1000+ tokens (we've seen 1135 in practice)
		// — don't assume zero just because we haven't measured it.
		promptEstimate = 1536
	}
	const safetyMargin = 256 // chat template / role overhead, not exact
	room := a.cfg.ContextLimit - promptEstimate - safetyMargin
	if room < 256 {
		room = 256 // always ask for *something* rather than zero/negative
	}
	if room > a.cfg.MaxTokens {
		return a.cfg.MaxTokens
	}
	return room
}

func (a *Agent) budgetWarning(usage llm.Usage, warned *int) string {
	if a.cfg.ContextLimit <= 0 || usage.PromptTokens <= 0 {
		return ""
	}
	pct := usage.PromptTokens * 100 / a.cfg.ContextLimit
	switch {
	case pct >= 90 && *warned < 90:
		*warned = 90
		return fmt.Sprintf(prompts.BudgetWarning, usage.PromptTokens, a.cfg.ContextLimit, pct)
	case pct >= 75 && *warned < 75:
		*warned = 75
		return fmt.Sprintf(prompts.BudgetNotice, usage.PromptTokens, a.cfg.ContextLimit, pct)
	default:
		return ""
	}
}

// callSignature identifies a set of tool calls by name+arguments, order
// independent, so the loop can tell "the model issued the exact same
// call(s) again" apart from "the model made progress" — a tool call that
// succeeds without error is not the same thing as the model moving
// forward if it's the same call as last time.
// looksLikeLeakedToolCall catches a model writing its own native
// tool-call template as plain text instead of making a real structured
// call — observed twice now as "<|tool_call>call:NAME{...}" and
// "<tool_call>...</tool_call>" variants, both at the start of a message
// and buried in the middle of one after some normal prose. A
// start-of-message-only grammar constraint can't catch the second case;
// this check runs on the full content regardless of position.
func looksLikeLeakedToolCall(content string) bool {
	lower := strings.ToLower(content)
	return strings.Contains(lower, "<tool_call") || strings.Contains(lower, "<|tool_call")
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func callSignature(calls []llm.ToolCall) string {
	parts := make([]string, len(calls))
	for i, c := range calls {
		parts[i] = c.Name + ":" + string(c.Arguments)
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

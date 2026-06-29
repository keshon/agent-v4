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
					Role: llm.RoleUser,
					Content: "Your last response contains text that looks like an attempted tool " +
						"call (e.g. \"<|tool_call...\" or \"<tool_call>...\") instead of a real one " +
						"— nothing was executed, nothing was saved. Make the actual tool call " +
						"properly using function-calling, not text that merely looks like one.",
				})
				stuckSteps++
				if stuckSteps >= a.cfg.MaxStuckSteps {
					history = append(history, llm.Message{
						Role: llm.RoleUser,
						Content: "This keeps happening. Respond with either a real function call " +
							"or plain text — nothing that imitates a function call as text.",
					})
					stuckSteps = 0
				}
				continue
			}

			if !a.cfg.SkipVerify && !verifiedOnce {
				verifiedOnce = true
				verifyMsg := "Before you finish: double-check that what you just did actually " +
					"satisfies the original task. Re-read or re-list anything you're not " +
					"certain about. If something is wrong, incomplete, or a placeholder, " +
					"fix it now using your tools. If it's genuinely correct, just confirm."
				if mutatingSucceeded == 0 {
					verifyMsg += fmt.Sprintf(" Concrete fact: 0 file-writing tool calls (%s) have "+
						"succeeded so far in this run. If the task required creating or changing "+
						"a file, that file does not exist yet — describing or generating content "+
						"in your response text does not save it, only an actual tool call does.",
						strings.Join(a.cfg.MutatingTools, "/"))
				}
				if a.cfg.Verify != nil {
					if out, ok := a.cfg.Verify(ctx); ok {
						if out == "" {
							out = "(no output)"
						}
						verifyMsg += fmt.Sprintf(" Build/test check result: %s", out)
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

		// Tool calls within one step run concurrently — this is what
		// makes multiple delegate_task calls in a single step actually
		// run in parallel instead of one after another. Trade-off: a
		// model that issues a write then a read of the *same* file in one
		// step can no longer rely on them executing in that order. In
		// practice multi-call steps are almost always independent reads
		// or independent delegations, so this is worth it.
		type callResult struct {
			content string
			err     error
		}
		results := make([]callResult, len(resp.Message.ToolCalls))
		var wg sync.WaitGroup
		for i, call := range resp.Message.ToolCalls {
			wg.Add(1)
			go func(i int, call llm.ToolCall) {
				defer wg.Done()
				content, err := a.cfg.Tools.Run(ctx, call.Name, call.Arguments)
				results[i] = callResult{content: content, err: err}
			}(i, call)
		}
		wg.Wait()

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
			nudge := "Every tool call has failed for several steps in a row. " +
				"Stop repeating the same approach: re-read the error messages above, " +
				"reconsider your plan, and try a different tool or a different strategy."
			if repeat {
				nudge = "You've called the exact same tool with the exact same arguments " +
					"several times in a row. Repeating it again will not produce a different " +
					"result. State explicitly what you learned from the last call, and take a " +
					"genuinely different next step."
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
		return fmt.Sprintf("Context budget warning: %d/%d tokens used (%d%%). Wrap up now — "+
			"finish this step and stop, or delegate any remaining self-contained work to "+
			"delegate_task so it runs in a fresh context instead of growing this one.",
			usage.PromptTokens, a.cfg.ContextLimit, pct)
	case pct >= 75 && *warned < 75:
		*warned = 75
		return fmt.Sprintf("Context budget notice: %d/%d tokens used (%d%%). Consider wrapping "+
			"up soon, or delegating self-contained remaining work to a subagent via "+
			"delegate_task to keep this conversation's context smaller.",
			usage.PromptTokens, a.cfg.ContextLimit, pct)
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

package agent

import (
	"context"
	"encoding/json"
	"errors"
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

	// VerifyOnZeroWrites, when set alongside SkipVerify, still runs the
	// self-check round if the run is about to finish with zero successful
	// MutatingTools calls. Mission workers use this: their correctness is
	// checked mechanically afterwards (so the general verify round is
	// redundant), but a worker that announces "let me write the file" and
	// finishes without writing is best corrected HERE, while its analysis
	// is still in context — one nudge now beats a fresh fix worker that
	// has to rediscover everything.
	VerifyOnZeroWrites bool

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

	// CompactKeepSteps is how many recent assistant-led step groups to
	// retain when history is mechanically compacted at 90% context usage.
	// Defaults to 8 in New. Set to -1 to disable compaction.
	CompactKeepSteps int
}

type Agent struct {
	cfg Config

	// LastRunMutations counts successful MutatingTools calls from the
	// most recent completed run. Set when Run/Resume returns; readable by
	// delegate_task to report subagent filesystem changes to the parent.
	// (Unlike RunReport.MutatedPaths, this also counts writes made by
	// delegated subagents, via the DELEGATE result header.)
	LastRunMutations int

	report RunReport
}

// RunReport is what a harness can learn about a finished run without
// trusting anything the model said about itself: which files its own
// mutating tool calls actually touched, how many steps it took, what the
// final message was. Populated by run() as measured fact — a mission
// runner records these into its ledger instead of asking a weak model to
// summarize its own work, which is exactly where fake "I saved the file"
// claims come from.
type RunReport struct {
	// Steps is how many model round-trips the run performed.
	Steps int

	// MutatedPaths lists the workspace paths of successful MutatingTools
	// calls made directly by this agent (path/from/to arguments), in
	// first-touch order, deduplicated. Subagent writes are not included —
	// a parent that needs those reads the DELEGATE result header.
	MutatedPaths []string

	// Final is the model's final answer text ("" if the run errored out).
	Final string

	// LastPromptTokens is the backend-reported prompt size of the last
	// completed call — how full the context actually got.
	LastPromptTokens int
}

// Report returns measured facts about the most recent Run/Resume,
// including a partially filled report for a run that errored mid-way.
func (a *Agent) Report() RunReport {
	return a.report
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
	if cfg.CompactKeepSteps == 0 {
		cfg.CompactKeepSteps = 8
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

type runState struct {
	stuckSteps, exploratorySteps, mutatingSucceeded, warnedThreshold, lastPromptTokens int
	consecutiveSameToolCount                                                          int
	verifiedOnce, searchFatigueWarned, compactedOnce, blockFinishDueToVerify, toolLoopWarned bool
	emptyFinishRetried                                                                 bool
	lastSignature, verifyFailedOutput, lastSingleTool string
	mutatedPaths                                      []string

	// idempotentSeen maps signature (name+args) of successful idempotent
	// calls to the step that ran them; cleared whenever anything mutates
	// the workspace. Exact repeats short-circuit to an error result — a
	// weak model that re-issues the same read five times gets told it's
	// repeating instead of getting five copies of the same bytes.
	idempotentSeen map[string]int
}

func (a *Agent) run(ctx context.Context, history []llm.Message) (string, error) {
	var st runState
	a.LastRunMutations = 0
	a.report = RunReport{}

	for step := 0; step < a.cfg.MaxSteps; step++ {
		resp, err := a.cfg.Client.Chat(ctx, llm.ChatRequest{
			Messages:  history,
			Tools:     a.cfg.Tools.Defs(),
			MaxTokens: a.effectiveMaxTokens(st.lastPromptTokens),
		})
		if err != nil {
			return "", fmt.Errorf("step %d: chat: %w", step, err)
		}
		if a.cfg.OnStep != nil {
			a.cfg.OnStep(step, resp.Message)
		}
		history = append(history, resp.Message)
		budgetNudge := a.budgetWarning(resp.Usage, &st.warnedThreshold)
		if resp.Usage.PromptTokens > 0 {
			st.lastPromptTokens = resp.Usage.PromptTokens
		}
		a.report.Steps = step + 1
		a.report.LastPromptTokens = st.lastPromptTokens
		a.maybeCompact(&history, resp.Usage, &st)
		a.saveState(history)

		if len(resp.Message.ToolCalls) == 0 {
			// finish_reason "length" means the backend cut generation off
			// and discarded whatever the model was building — usually the
			// tool call it had just announced. That's a truncation, never a
			// finish: treating the stump as an answer is how a live run
			// "finished" a subtask with "Let me write the plan.md file…"
			// and zero writes. Nudge and continue; escalate via the stuck
			// counter so a backend that truncates every response is still
			// bounded by MaxStuckSteps → MaxSteps.
			if resp.FinishReason == "length" {
				history = append(history, llm.Message{
					Role:    llm.RoleUser,
					Content: prompts.Truncated,
				})
				st.stuckSteps++
				if st.stuckSteps >= a.cfg.MaxStuckSteps {
					history = append(history, llm.Message{
						Role:    llm.RoleUser,
						Content: prompts.StuckFailing,
					})
					st.stuckSteps = 0
				}
				continue
			}

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
				st.stuckSteps++
				if st.stuckSteps >= a.cfg.MaxStuckSteps {
					history = append(history, llm.Message{
						Role:    llm.RoleUser,
						Content: prompts.LeakRepeated,
					})
					st.stuckSteps = 0
				}
				continue
			}

			if st.blockFinishDueToVerify {
				st.blockFinishDueToVerify = false
				st.verifiedOnce = false
				history = append(history, llm.Message{
					Role:    llm.RoleUser,
					Content: fmt.Sprintf(prompts.VerifyFailedContinue, st.verifyFailedOutput),
				})
				st.verifyFailedOutput = ""
				continue
			}

			verifyWanted := !a.cfg.SkipVerify ||
				(a.cfg.VerifyOnZeroWrites && st.mutatingSucceeded == 0)
			if verifyWanted && !st.verifiedOnce {
				st.verifiedOnce = true
				verifyMsg := prompts.Verify
				if st.mutatingSucceeded == 0 {
					verifyMsg += fmt.Sprintf(prompts.VerifyZeroWrites,
						strings.Join(a.cfg.MutatingTools, "/"))
				}
				if a.cfg.Verify != nil {
					if out, ok := a.cfg.Verify(ctx); ok {
						if out == "" {
							out = "(no output)"
						}
						verifyMsg += fmt.Sprintf(prompts.VerifyCheckResult, out)
						if strings.HasPrefix(out, "FAILED") {
							st.blockFinishDueToVerify = true
							st.verifyFailedOutput = out
						}
					}
				}
				history = append(history, llm.Message{Role: llm.RoleUser, Content: verifyMsg})
				if budgetNudge != "" {
					history = append(history, llm.Message{Role: llm.RoleUser, Content: budgetNudge})
				}
				continue
			}
			if strings.TrimSpace(resp.Message.Content) == "" {
				if !st.emptyFinishRetried {
					st.emptyFinishRetried = true
					continue
				}
			}
			a.LastRunMutations = st.mutatingSucceeded
			a.report.MutatedPaths = st.mutatedPaths
			a.report.Final = resp.Message.Content
			return resp.Message.Content, nil
		}

		signature := callSignature(resp.Message.ToolCalls)
		repeat := signature == st.lastSignature
		st.lastSignature = signature

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

		// Exact repeats of idempotent (pure-read) calls don't re-execute:
		// nothing has changed, so the result would be byte-identical — and
		// re-delivering it teaches a weak model nothing while filling the
		// context with duplicates. The repeat comes back as an *error*
		// result on purpose: errors don't count as progress, so the stuck
		// detector keeps escalating if the model won't change course.
		skipped := make([]bool, len(resp.Message.ToolCalls))
		for i, call := range resp.Message.ToolCalls {
			if !a.cfg.Tools.IdempotentOf(call.Name) {
				continue
			}
			sig := call.Name + ":" + string(call.Arguments)
			if prev, seen := st.idempotentSeen[sig]; seen {
				skipped[i] = true
				results[i] = callResult{err: fmt.Errorf(
					"this exact %s call (same arguments) already ran in step %d and nothing has "+
						"changed since — its result is still valid, re-read it from the conversation. "+
						"Do not repeat the call; do something different (different path, different "+
						"arguments, or move on to acting on what you already know)",
					call.Name, prev)}
			}
		}

		var concurrentIdx, exclusiveIdx []int
		for i, call := range resp.Message.ToolCalls {
			if skipped[i] {
				continue
			}
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

		mutatingBefore := st.mutatingSucceeded
		progressed := false
		exclusiveSucceeded := false
		for i, call := range resp.Message.ToolCalls {
			content := results[i].content
			if results[i].err != nil {
				content = "error: " + results[i].err.Error()
			} else {
				progressed = true
				if a.cfg.Tools.ModeOf(call.Name) == Exclusive {
					exclusiveSucceeded = true
				}
				if a.cfg.Tools.IdempotentOf(call.Name) {
					if st.idempotentSeen == nil {
						st.idempotentSeen = make(map[string]int)
					}
					st.idempotentSeen[call.Name+":"+string(call.Arguments)] = step
				}
				if containsStr(a.cfg.MutatingTools, call.Name) {
					st.mutatingSucceeded++
					for _, p := range mutatedPathsFromCall(call.Arguments) {
						if !containsStr(st.mutatedPaths, p) {
							st.mutatedPaths = append(st.mutatedPaths, p)
						}
					}
				}
				if call.Name == "delegate_task" {
					if m := parseDelegateMutations(content); m > 0 {
						st.mutatingSucceeded += m
					}
				}
			}
			history = append(history, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Content:    content,
			})
		}

		// Anything that mutated (an Exclusive call — write/patch/shell — or
		// a subagent reporting writes) invalidates the repeat cache: the
		// same read can now legitimately return something new.
		if exclusiveSucceeded || st.mutatingSucceeded > mutatingBefore {
			st.idempotentSeen = nil
		}

		if st.mutatingSucceeded > mutatingBefore {
			st.exploratorySteps = 0
		} else {
			st.exploratorySteps++
		}
		if st.exploratorySteps >= a.cfg.MaxExploratorySteps && !st.searchFatigueWarned {
			st.searchFatigueWarned = true
			history = append(history, llm.Message{Role: llm.RoleUser, Content: prompts.SearchFatigue})
		}

		a.trackSingleToolLoop(resp.Message.ToolCalls, &st)
		if st.consecutiveSameToolCount >= 3 && !st.toolLoopWarned {
			st.toolLoopWarned = true
			history = append(history, llm.Message{Role: llm.RoleUser, Content: prompts.ToolLoop})
		}

		if budgetNudge != "" {
			history = append(history, llm.Message{Role: llm.RoleUser, Content: budgetNudge})
		}
		a.saveState(history)

		if progressed && !repeat {
			st.stuckSteps = 0
			continue
		}

		st.stuckSteps++
		if st.stuckSteps >= a.cfg.MaxStuckSteps {
			nudge := prompts.StuckFailing
			if repeat {
				nudge = prompts.StuckRepeating
			}
			history = append(history, llm.Message{Role: llm.RoleUser, Content: nudge})
			st.stuckSteps = 0
			st.lastSignature = "" // the nudge itself breaks the repeat chain
		}
	}

	a.LastRunMutations = st.mutatingSucceeded
	a.report.MutatedPaths = st.mutatedPaths
	return "", fmt.Errorf("%w (%d) without finishing", ErrMaxSteps, a.cfg.MaxSteps)
}

// ErrMaxSteps marks a run that did real work but ran out of step budget —
// as opposed to infrastructure failures (a dead backend, a network error)
// where no work happened at all. Callers that retry on failure (the
// mission fix loop) must distinguish the two: retrying a step-budget
// death can converge; retrying against a dead backend just burns retry
// budget on connection errors.
var ErrMaxSteps = errors.New("reached max steps")

// mutatedPathsFromCall pulls workspace paths out of a mutating tool
// call's arguments. The project's file tools name them path (write_file,
// patch_file, patch_lines) or from/to (move_file); a mutating tool with
// none of these contributes nothing rather than guessing.
func mutatedPathsFromCall(args json.RawMessage) []string {
	var fields struct {
		Path string `json:"path"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	if json.Unmarshal(args, &fields) != nil {
		return nil
	}
	var out []string
	for _, p := range []string{fields.Path, fields.From, fields.To} {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseDelegateMutations reads the structured DELEGATE header from a
// delegate_task result so the parent run can count subagent writes.
func parseDelegateMutations(content string) int {
	if !strings.HasPrefix(content, "DELEGATE\n") {
		return 0
	}
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "mutations: ") {
			var n int
			if _, err := fmt.Sscanf(line, "mutations: %d", &n); err == nil {
				return n
			}
		}
		if line == "----" {
			break
		}
	}
	return 0
}

func (a *Agent) maybeCompact(history *[]llm.Message, usage llm.Usage, st *runState) bool {
	if st.compactedOnce || a.cfg.ContextLimit <= 0 || a.cfg.CompactKeepSteps <= 0 {
		return false
	}
	if usage.PromptTokens <= 0 {
		return false
	}
	pct := usage.PromptTokens * 100 / a.cfg.ContextLimit
	if pct < 90 {
		return false
	}
	*history = compactHistory(*history, a.cfg.CompactKeepSteps)
	st.compactedOnce = true
	return true
}

// trackSingleToolLoop counts consecutive steps where the model issued
// exactly one tool call and it's the same tool name as the previous
// such step — catches run_shell/git-log tweak loops that exact-repeat
// detection misses because the arguments differ slightly each time.
func (a *Agent) trackSingleToolLoop(calls []llm.ToolCall, st *runState) {
	if len(calls) != 1 {
		st.lastSingleTool = ""
		st.consecutiveSameToolCount = 0
		return
	}
	name := calls[0].Name
	if name == st.lastSingleTool {
		st.consecutiveSameToolCount++
		return
	}
	st.lastSingleTool = name
	st.consecutiveSameToolCount = 1
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
	if strings.Contains(lower, "<tool_call") || strings.Contains(lower, "<|tool_call") {
		return true
	}
	// Gemma-style narrative leaks: "(Made a function call call_92023 to read_file...)"
	if strings.Contains(lower, "made a function call") {
		return true
	}
	return strings.Contains(content, "call_") && strings.Contains(lower, "arguments=")
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

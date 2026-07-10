package mission

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-v4/internal/agent"
	"agent-v4/internal/llm"
	"agent-v4/internal/prompts"
	"agent-v4/internal/tools"
	"agent-v4/internal/workspace"
)

// Runner drives a mission through its phases deterministically. The
// model never decides what phase comes next — it fills in a plan when
// asked and executes one subtask at a time in a fresh, compiled context.
// Every transition and every status change persists mission.json first,
// so resume is always "load, switch on phase, continue".
type Runner struct {
	Client llm.Client
	WS     *workspace.Workspace
	Dir    string // .agent/tasks/<id> — the mission's state directory
	Procs  *tools.BackgroundProcesses

	ContextLimit int
	MaxTokens    int

	// MaxWorkerSteps bounds each subtask worker. Deliberately small: a
	// subtask that needs 30 steps is a planning failure, not a worker
	// failure. Zero means 15.
	MaxWorkerSteps int

	// MaxPlanRevisions bounds how many times the human approval gate can
	// send the plan back with an edit note. Zero means 2.
	MaxPlanRevisions int

	// ApprovePlan, if set, gates execution on a human reading the
	// rendered plan. Return (true, "") to run it, (false, "note") to
	// regenerate with the note, (false, "") to abort the mission. Nil
	// auto-approves — the -yes path.
	ApprovePlan func(rendered string) (approved bool, editNote string)

	// VerifyCmd, if non-empty, runs as a mission-level shell check
	// during the verify phase, independent of whatever checks the
	// planner declared — the harness's own ground truth.
	VerifyCmd string

	// OnStep mirrors agent.Config.OnStep for worker steps, tagged with
	// the subtask id. OnEvent narrates phase-level progress. Both
	// optional.
	OnStep  func(subID string, step int, msg llm.Message)
	OnEvent func(format string, args ...any)
}

func (r *Runner) event(format string, args ...any) {
	if r.OnEvent != nil {
		r.OnEvent(format, args...)
	}
}

func (r *Runner) maxWorkerSteps() int {
	if r.MaxWorkerSteps > 0 {
		return r.MaxWorkerSteps
	}
	return 15
}

func (r *Runner) maxPlanRevisions() int {
	if r.MaxPlanRevisions > 0 {
		return r.MaxPlanRevisions
	}
	return 2
}

// Run drives m from its current phase to a terminal one and returns the
// final report. The error is non-nil when the mission FAILED — the
// report still describes everything that was measured, because failing
// loudly with facts is a feature, not an afterthought.
func (r *Runner) Run(ctx context.Context, m *Mission) (string, error) {
	for {
		switch m.Phase {
		case PhaseExplore:
			// Mechanical map generation lands in a later stage; the
			// listing computed per-phase below covers Stage 2.
			m.Phase = PhasePlan
			if err := m.Save(r.Dir); err != nil {
				return "", err
			}

		case PhasePlan:
			if err := r.runPlan(ctx, m); err != nil {
				return r.fail(m, "planning: %v", err)
			}

		case PhaseExecute:
			sub := m.Current()
			if sub == nil {
				m.Phase = PhaseVerify
				if err := m.Save(r.Dir); err != nil {
					return "", err
				}
				continue
			}
			if sub.Status == StatusDone || sub.Status == StatusSkipped {
				m.Cursor++
				if err := m.Save(r.Dir); err != nil {
					return "", err
				}
				continue
			}
			if err := r.runSubtask(ctx, m, sub); err != nil {
				return r.fail(m, "subtask %s: %v", sub.ID, err)
			}

		case PhaseVerify:
			if err := r.runVerify(ctx, m); err != nil {
				return r.fail(m, "verify: %v", err)
			}

		case PhaseDone:
			return m.RenderReport(), nil

		case PhaseFailed:
			return m.RenderReport(), fmt.Errorf("mission failed")

		default:
			return r.fail(m, "unknown phase %q", m.Phase)
		}
	}
}

func (r *Runner) fail(m *Mission, format string, args ...any) (string, error) {
	reason := fmt.Sprintf(format, args...)
	r.event("mission FAILED: %s", reason)
	m.Phase = PhaseFailed
	_ = m.Save(r.Dir)
	return m.RenderReport(), fmt.Errorf("mission failed: %s", reason)
}

// --- plan phase ---

func (r *Runner) runPlan(ctx context.Context, m *Mission) error {
	listing, existing := WorkspaceListing(r.WS)
	editNote := ""

	for revision := 0; ; revision++ {
		r.event("planning (%s)…", planAttemptLabel(revision))
		subtasks, warnings, err := GeneratePlan(ctx, r.Client, PlanRequest{
			Task:          m.Task,
			FileListing:   listing,
			ExistingFiles: existing,
			EditNote:      editNote,
			MaxTokens:     r.MaxTokens,
		})
		if err != nil {
			return err
		}
		m.Subtasks = subtasks

		if r.ApprovePlan == nil {
			break
		}
		rendered := m.RenderPlan()
		if len(warnings) > 0 {
			rendered += "\n\nNew files the plan intends to create:\n  " + strings.Join(warnings, "\n  ")
		}
		approved, note := r.ApprovePlan(rendered)
		if approved {
			break
		}
		if note == "" {
			return fmt.Errorf("plan rejected by user")
		}
		if revision+1 >= r.maxPlanRevisions() {
			return fmt.Errorf("plan still rejected after %d revisions", r.maxPlanRevisions())
		}
		editNote = note
	}

	m.Phase = PhaseExecute
	m.Cursor = 0
	return m.Save(r.Dir)
}

func planAttemptLabel(revision int) string {
	if revision == 0 {
		return "initial"
	}
	return fmt.Sprintf("revision %d", revision)
}

// --- execute phase ---

func (r *Runner) runSubtask(ctx context.Context, m *Mission, sub *Subtask) error {
	sub.Status = StatusRunning
	sub.Attempts++
	if err := m.Save(r.Dir); err != nil {
		return err
	}
	r.event("subtask %s (%s): starting worker (attempt %d)", sub.ID, sub.Title, sub.Attempts)

	worker := r.newWorker(sub)
	listing, _ := WorkspaceListing(r.WS)
	seed := CompileSeed(m, sub, listing)

	result, runErr := r.runOrResumeWorker(ctx, worker, sub, seed)
	report := worker.Report()

	// Facts are measured, not claimed. Record them even when the worker
	// errored out — a max-steps death after three successful writes still
	// changed the workspace, and the check (and any human reading the
	// ledger) must see the whole truth.
	if len(report.MutatedPaths) > 0 {
		sub.AddFact("wrote: %s", strings.Join(report.MutatedPaths, ", "))
		m.Mutated = unionPaths(m.Mutated, report.MutatedPaths)
	} else {
		sub.AddFact("wrote: nothing")
	}
	if runErr != nil {
		sub.AddFact("worker stopped early: %v", runErr)
	}
	sub.Summary = agent.TruncateMiddle(strings.TrimSpace(result), maxSummaryChars)

	// The check is the verdict — not the worker's exit status. A worker
	// that hit MaxSteps but finished the actual work still passes; a
	// worker that returned a confident report over an empty file fails.
	checkOut, ok := RunCheck(ctx, sub.Check, r.WS)
	if ok {
		sub.AddFact("check: PASSED — %s", agent.TruncateMiddle(checkOut, 200))
		sub.Status = StatusDone
		m.Cursor++
		r.event("subtask %s: check PASSED", sub.ID)
		return m.Save(r.Dir)
	}

	sub.AddFact("check: FAILED — %s", agent.TruncateMiddle(checkOut, 2000))
	sub.Status = StatusFailed
	_ = m.Save(r.Dir)
	r.event("subtask %s: check FAILED", sub.ID)
	// Stage 2 fails the mission on the first failed subtask; the bounded
	// fix loop and replanning arrive in the next stage and slot in here.
	return fmt.Errorf("check failed: %s", agent.TruncateMiddle(checkOut, 500))
}

func (r *Runner) newWorker(sub *Subtask) *agent.Agent {
	var onStep func(step int, msg llm.Message)
	if r.OnStep != nil {
		id := sub.ID
		onStep = func(step int, msg llm.Message) { r.OnStep(id, step, msg) }
	}
	return agent.New(agent.Config{
		Client:       r.Client,
		Tools:        tools.Base(r.WS, r.Procs),
		System:       prompts.MissionWorker,
		MaxSteps:     r.maxWorkerSteps(),
		MaxTokens:    r.MaxTokens,
		ContextLimit: r.ContextLimit,
		// The mission's checks are mechanical; the model self-check round
		// would be a redundant extra call per subtask.
		SkipVerify: true,
		StateFile:  r.workerStateFile(sub),
		OnStep:     onStep,
	})
}

func (r *Runner) workerStateFile(sub *Subtask) string {
	return filepath.Join(r.Dir, "workers", fmt.Sprintf("%s-a%d.json", sub.ID, sub.Attempts))
}

// runOrResumeWorker continues an interrupted worker transcript when one
// exists for this subtask (the process died mid-subtask and the mission
// was resumed); otherwise it starts fresh from the compiled seed.
func (r *Runner) runOrResumeWorker(ctx context.Context, worker *agent.Agent, sub *Subtask, seed string) (string, error) {
	if state := r.latestWorkerState(sub.ID); state != "" {
		if history, err := agent.LoadState(state); err == nil && len(history) > 1 {
			r.event("subtask %s: resuming interrupted worker (%s)", sub.ID, filepath.Base(state))
			return worker.Resume(ctx, history, prompts.Resume)
		}
	}
	return worker.Run(ctx, seed)
}

// latestWorkerState returns the newest saved transcript for a subtask,
// or "" when this is a fresh start. Attempt files sort lexically within
// one subtask because attempts share the "sN-a" prefix.
func (r *Runner) latestWorkerState(subID string) string {
	pattern := filepath.Join(r.Dir, "workers", subID+"-a*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	newest := matches[len(matches)-1]
	if info, err := os.Stat(newest); err != nil || info.Size() == 0 {
		return ""
	}
	return newest
}

// --- verify phase ---

// runVerify re-runs every done subtask's check — later subtasks can break
// earlier ones — plus the mission-level VerifyCmd. Any regression fails
// the mission with the specifics on record.
func (r *Runner) runVerify(ctx context.Context, m *Mission) error {
	var regressions []string
	for i := range m.Subtasks {
		sub := &m.Subtasks[i]
		if sub.Status != StatusDone || sub.Check.Type == "" || sub.Check.Type == "none" {
			continue
		}
		out, ok := RunCheck(ctx, sub.Check, r.WS)
		if !ok {
			sub.AddFact("final verify: FAILED — %s", agent.TruncateMiddle(out, 500))
			regressions = append(regressions, fmt.Sprintf("%s: %s", sub.ID, agent.TruncateMiddle(out, 200)))
		}
	}

	if r.VerifyCmd != "" {
		r.event("running mission verify command: %s", r.VerifyCmd)
		out, ok := RunCheck(ctx, Check{Type: "shell", Cmd: r.VerifyCmd}, r.WS)
		if !ok {
			regressions = append(regressions, "verify-cmd: "+agent.TruncateMiddle(out, 500))
		}
	}

	if len(regressions) > 0 {
		_ = m.Save(r.Dir)
		return fmt.Errorf("final checks failed:\n%s", strings.Join(regressions, "\n"))
	}

	m.Phase = PhaseDone
	r.event("all checks passed")
	return m.Save(r.Dir)
}

// Command agent runs a single task against a local LLM backend.
//
//	agent "create a snake game in plain JS under ./game"
//	agent -debug "..."   # also writes agent-debug.log with raw request/response JSON
package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/mission"
	"github.com/keshon/tars/internal/prompts"
	"github.com/keshon/tars/internal/roles"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/workspace"
)

func main() {
	backend := flag.String("backend", "http://localhost:5001", "koboldcpp/llama.cpp base URL")
	model := flag.String("model", "local", "model name (often ignored by local servers)")
	root := flag.String("workspace", ".", "workspace root the agent may read/write")
	debug := flag.Bool("debug", false, "log raw request/response JSON to agent-debug.log")
	grammar := flag.Bool("grammar", true, "constrain responses with a GBNF grammar that blocks "+
		"leaked native tool-call template tags from appearing as plain text (disable if it "+
		"breaks real tool calls on your backend)")
	maxTokens := flag.Int("max-tokens", 8192, "generation budget per response — too low truncates "+
		"large outputs (e.g. a full HTML+CSS+JS file) mid-JSON")
	resume := flag.String("resume", "", "path to a .agent/tasks/.../state.json snapshot to resume "+
		"an interrupted run from, instead of starting a new task")
	answer := flag.String("answer", "", "answer to supply when resuming a run paused on ask_user "+
		"(use with -resume when the agent stopped to ask a clarifying question)")
	verifyCmd := flag.String("verify-cmd", "", "command to run at the self-check checkpoint "+
		"(e.g. \"go build ./... && go vet ./... && go test ./...\") — real output gets fed back "+
		"as fact instead of trusting the model's own claim that the code works. Empty disables this.")
	logMax := flag.Int("log-max", 300, "max characters per line in step console output; "+
		"long text is truncated in the middle (head ... tail). 0 = no limit")
	missionMode := flag.Bool("mission", false, "run the task as a mission: an upfront model-generated "+
		"plan (approved by you), then one fresh-context worker per subtask, each verified mechanically. "+
		"For complex multi-file tasks a weak model can't hold in its head; simple tasks are better off "+
		"without it. Also auto-enabled when the task names ≥2 deliverable files (see -direct)")
	direct := flag.Bool("direct", false, "force the reactive agent loop even when the task looks multi-file")
	yes := flag.Bool("yes", false, "skip the mission plan approval gate and run the plan as generated")
	backendKind := flag.String("backend-kind", "kobold", "which local server: kobold or llama "+
		"(llama-server, worth running with --jinja for per-model tool-call formats)")
	flag.Parse()

	task := strings.Join(flag.Args(), " ")
	if task == "" && *resume == "" {
		log.Fatal(`usage: agent [flags] "task description"  (or  agent -resume <state.json> [-answer "..."])`)
	}

	// Auto-mission for multi-file tasks — the cheap alternative to hoping
	// the reactive loop (or spontaneous delegate_task) holds a plan.
	if !*missionMode && !*direct && task != "" && mission.SuggestMission(task) {
		*missionMode = true
		fmt.Println("auto-mission: task names multiple deliverable files (use -direct to skip)")
	}

	// A -resume target whose directory holds mission.json is a mission
	// resume, whatever the path points at (the dir itself, mission.json,
	// or a worker state file) — direct-mode resume stays the fallback.
	missionDir := ""
	if *resume != "" {
		cand := *resume
		if info, err := os.Stat(cand); err != nil || !info.IsDir() {
			cand = filepath.Dir(cand)
		}
		if mission.Exists(cand) {
			missionDir = cand
		}
	}

	// Every run snapshots its history to disk after each step, so a crash
	// or hitting MaxSteps doesn't lose everything — see -resume.
	var stateFile string
	if *resume != "" {
		stateFile = *resume // keep appending to the same snapshot we resumed from
	} else {
		sum := sha1.Sum([]byte(task + time.Now().String()))
		taskID := hex.EncodeToString(sum[:])[:8]
		taskDir := filepath.Join(".agent", "tasks", taskID)
		stateFile = filepath.Join(taskDir, "state.json")
		if *missionMode {
			missionDir = taskDir
			fmt.Printf("mission id: %s (resume with -resume %s)\n", taskID, taskDir)
		} else {
			fmt.Printf("task id: %s (resume with -resume %s)\n", taskID, stateFile)
		}
	}

	ws, err := workspace.New(*root)
	if err != nil {
		log.Fatalf("workspace: %v", err)
	}

	var client *llm.Server
	switch *backendKind {
	case "kobold":
		client = llm.NewKoboldClient(*backend, *model)
	case "llama":
		client = llm.NewLlamaClient(*backend, *model)
	default:
		log.Fatalf("unknown -backend-kind %q, want kobold or llama", *backendKind)
	}
	if *grammar {
		client.Grammar = llm.DefaultGrammar
	}
	if *debug {
		f, err := os.Create("agent-debug.log")
		if err != nil {
			log.Fatalf("open debug log: %v", err)
		}
		defer f.Close()
		client.Debug = f
		fmt.Println("debug: logging raw request/response JSON to agent-debug.log")
	}

	// Ask the backend for its real context window instead of guessing.
	// If it doesn't support this (older koboldcpp, different server),
	// budget tracking just stays off — not a fatal error.
	ctx := context.Background()
	contextLimit, err := client.MaxContextLength(ctx)
	if err != nil {
		fmt.Printf("context budget tracking unavailable (%v) — continuing without it\n", err)
		contextLimit = 0
	} else {
		fmt.Printf("context window: %d tokens\n", contextLimit)
	}

	// Reuses mission.RunShellCommand for the same OS-aware shell choice as
	// tools.RunShell and mission shell checks — one exec shape everywhere.
	var verify func(ctx context.Context) (string, bool)
	if *verifyCmd != "" {
		verify = func(ctx context.Context) (string, bool) {
			out, err := mission.RunShellCommand(ctx, *verifyCmd, ws.Root())
			status := "PASSED"
			if err != nil {
				status = "FAILED"
			}
			return fmt.Sprintf("%s\n%s", status, out), true
		}
	}

	// Shared across both tool sets so a subagent's start_background and
	// the parent's check_background/stop_background see the same
	// processes — a process started by one half of the conversation
	// should be checkable/stoppable from the other.
	bgProcs := tools.NewBackgroundProcesses()

	if missionDir != "" {
		runMission(ctx, missionParams{
			client:       client,
			ws:           ws,
			procs:        bgProcs,
			dir:          missionDir,
			task:         task,
			resuming:     *resume != "",
			contextLimit: contextLimit,
			maxTokens:    *maxTokens,
			logMax:       *logMax,
			verifyCmd:    *verifyCmd,
			autoApprove:  *yes,
		})
		return
	}

	// ask_user blocks on stdin inside the tool. State is snapshotted by
	// the agent loop after the assistant message (with the unanswered tool
	// call) and before tool execution — so Ctrl+C while waiting leaves a
	// resumable state.json. Use -resume with -answer to continue.
	const maxClarifyingQuestions = 3
	questionCount := 0
	askFn := func(question string) (string, error) {
		questionCount++
		if questionCount > maxClarifyingQuestions {
			return "", fmt.Errorf("ask_user: clarifying question limit (%d) reached — "+
				"make a decision and proceed with a stated assumption", maxClarifyingQuestions)
		}
		fmt.Printf("\n[paused — state saved at %s]\n", stateFile)
		fmt.Printf("[resume: agent -resume %s -answer \"your answer\"]\n", stateFile)
		fmt.Printf("\n[agent asks] %s\n> ", question)
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("reading answer: %w", err)
		}
		return strings.TrimSpace(line), nil
	}

	env := roles.Env{
		Client:       client,
		WS:           ws,
		Procs:        bgProcs,
		MaxTokens:    *maxTokens,
		ContextLimit: contextLimit,
		OnStep:       func(l string, s int, m llm.Message) { printStep(l, s, m, *logMax) },
	}

	// The subagent this spawns previously also carried Verify. That was
	// dead configuration: a subagent sets SkipVerify with no
	// VerifyOnZeroWrites, so verifyWanted is never true and the hook could
	// not fire. Dropping it changes nothing at runtime.
	a := roles.Interactive(env, "", stateFile, askFn, verify)

	var result string
	if *resume != "" {
		history, err := agent.LoadState(*resume)
		if err != nil {
			log.Fatalf("resume: %v", err)
		}
		fmt.Printf("resuming from %s (%d messages)\n", *resume, len(history))

		if callID, question, paused := agent.PausedOnQuestion(history); paused {
			ans := *answer
			if ans == "" {
				// No -answer flag: the human is here now, just ask them
				// interactively using the same askFn path as the live run.
				fmt.Printf("\n[agent asked] %s\n> ", question)
				reader := bufio.NewReader(os.Stdin)
				line, err := reader.ReadString('\n')
				if err != nil {
					log.Fatalf("reading answer: %v", err)
				}
				ans = strings.TrimSpace(line)
			}
			result, err = a.ResumeWithAnswer(ctx, history, callID, ans)
		} else {
			result, err = a.Resume(ctx, history, prompts.Resume)
		}
		if err != nil {
			log.Fatalf("agent failed: %v", err)
		}
	} else {
		var err error
		result, err = a.Run(ctx, task)
		if err != nil {
			log.Fatalf("agent failed: %v", err)
		}
	}
	fmt.Println("\n=== result ===")
	fmt.Println(agent.TruncateMiddle(result, *logMax))
}

type missionParams struct {
	client       llm.Client
	ws           *workspace.Workspace
	procs        *tools.BackgroundProcesses
	dir          string
	task         string
	resuming     bool
	contextLimit int
	maxTokens    int
	logMax       int
	verifyCmd    string
	autoApprove  bool
}

// runMission is the -mission entry point: harness-owned plan → execute →
// verify instead of one long reactive conversation. The interaction
// point with the human is the plan approval gate; workers themselves
// never ask questions.
func runMission(ctx context.Context, p missionParams) {
	var m *mission.Mission
	if p.resuming {
		var err error
		m, err = mission.Load(p.dir)
		if err != nil {
			log.Fatalf("resume mission: %v", err)
		}
		progress := ""
		if len(m.Subtasks) > 0 {
			progress = fmt.Sprintf(", subtask %d/%d", m.Cursor+1, len(m.Subtasks))
		}
		fmt.Printf("resuming mission %s (phase: %s%s)\n", m.ID, m.Phase, progress)
	} else {
		m = &mission.Mission{ID: filepath.Base(p.dir), Task: p.task, Phase: mission.PhaseExplore}
		if err := m.Save(p.dir); err != nil {
			log.Fatalf("create mission: %v", err)
		}
	}

	// The approval gate is the cheapest, strongest defense against a
	// weak model's garbage plans: a human reads it before anything runs.
	var approve func(string) (bool, string)
	if !p.autoApprove {
		reader := bufio.NewReader(os.Stdin)
		approve = func(rendered string) (bool, string) {
			fmt.Println("\n=== proposed plan ===")
			fmt.Println(rendered)
			fmt.Print("\napprove? [y]es / [n]o / or type a revision note\n> ")
			line, err := reader.ReadString('\n')
			if err != nil {
				return false, ""
			}
			line = strings.TrimSpace(line)
			switch strings.ToLower(line) {
			case "y", "yes":
				return true, ""
			case "n", "no":
				return false, ""
			default:
				return false, line
			}
		}
	}

	runner := &mission.Runner{
		Client:       p.client,
		WS:           p.ws,
		Dir:          p.dir,
		Procs:        p.procs,
		ContextLimit: p.contextLimit,
		MaxTokens:    p.maxTokens,
		ApprovePlan:  approve,
		VerifyCmd:    p.verifyCmd,
		OnStep: func(subID string, step int, msg llm.Message) {
			if msg.Content != "" {
				fmt.Printf("[%s step %d] %s\n", subID, step, agent.TruncateMiddle(msg.Content, p.logMax))
			}
			for _, tc := range msg.ToolCalls {
				fmt.Printf("[%s step %d] -> %s(%s)\n", subID, step, tc.Name,
					agent.TruncateMiddle(string(tc.Arguments), p.logMax))
			}
		},
		OnEvent: func(format string, args ...any) {
			fmt.Printf("[mission] "+format+"\n", args...)
		},
	}

	report, err := runner.Run(ctx, m)
	fmt.Println("\n=== mission report ===")
	fmt.Println(report)
	if err != nil {
		log.Fatalf("%v", err)
	}
}

// printStep renders one model step to the console. label is a subtask id
// or worker name in mission mode and empty for the top-level agent, which
// is the only difference between what the two modes used to print from
// two separate copies of this loop.
func printStep(label string, step int, msg llm.Message, logMax int) {
	prefix := fmt.Sprintf("[step %d]", step)
	if label != "" {
		prefix = fmt.Sprintf("[%s step %d]", label, step)
	}
	if msg.Content != "" {
		fmt.Printf("%s %s\n", prefix, agent.TruncateMiddle(msg.Content, logMax))
	}
	for _, tc := range msg.ToolCalls {
		fmt.Printf("%s -> %s(%s)\n", prefix, tc.Name,
			agent.TruncateMiddle(string(tc.Arguments), logMax))
	}
}

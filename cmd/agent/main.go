// Command agent runs a single task against a local LLM backend.
//
//	agent "create a snake game in plain JS under ./game"
//	agent -debug "..."   # also writes agent-debug.log with raw request/response JSON
package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agent-v4/internal/agent"
	"agent-v4/internal/llm"
	"agent-v4/internal/tools"
	"agent-v4/internal/workspace"
)

const systemPrompt = `You are a careful local coding agent.

CORE RULES (highest priority):
- You operate ONLY through tools. Writing code in chat has no effect unless a tool is used.
- Never simulate file changes. Always use tools to modify the workspace.
- Always prefer the safest atomic tool over manual composition.

FILESYSTEM RULES:
- To rename or move files, you MUST use move_file. Never use shell commands or read+write combinations for renaming.
- To edit existing files:
  - Use patch_file or patch_lines for small or targeted changes.
  - Use write_file only when creating a new file or fully replacing content intentionally.
- Before accessing a file, if its existence is uncertain, verify using list_files or grep_files first.
- Do not assume file paths exist.

DELEGATION RULES:
- If a task can be split into independent subtasks, create one delegate_task per subtask in the SAME step.
- Multiple delegate_task calls in one step execute in parallel.
- Do not wait for one subtask to finish before issuing another if they are independent.
- Use delegate_task role to define behavior or expertise when required. This is a real behavioral modifier, not a description.

SKILLS / GUIDANCE:
- If a skills/ directory exists, inspect it with list_files before starting complex work.
- Read relevant SKILL.md files before implementing unfamiliar patterns or workflows.

EXECUTION DISCIPLINE:
- Think step by step.
- Prefer simple solutions over complex ones.
- Every change to the workspace must be performed via a tool call.
- Any code shown in the response is non-functional unless applied through tools.

!If a rule conflicts with another instruction, follow the rule in the highest section first!`

const resumeNote = "Your previous attempt at this task was interrupted before finishing. " +
	"Don't assume anything about what's already done — call list_files (and read_file where " +
	"needed) to check the actual current state of the workspace first, then continue and finish " +
	"the task."

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
	verifyCmd := flag.String("verify-cmd", "", "command to run at the self-check checkpoint "+
		"(e.g. \"go build ./... && go vet ./... && go test ./...\") — real output gets fed back "+
		"as fact instead of trusting the model's own claim that the code works. Empty disables this.")
	flag.Parse()

	task := strings.Join(flag.Args(), " ")
	if task == "" && *resume == "" {
		log.Fatal(`usage: agent [flags] "task description"  (or  agent -resume <state.json>)`)
	}

	// Every run snapshots its history to disk after each step, so a crash
	// or hitting MaxSteps doesn't lose everything — see -resume.
	var stateFile string
	if *resume != "" {
		stateFile = *resume // keep appending to the same snapshot we resumed from
	} else {
		sum := sha1.Sum([]byte(task + time.Now().String()))
		taskID := hex.EncodeToString(sum[:])[:8]
		stateFile = filepath.Join(".agent", "tasks", taskID, "state.json")
		fmt.Printf("task id: %s (resume with -resume %s)\n", taskID, stateFile)
	}

	ws, err := workspace.New(*root)
	if err != nil {
		log.Fatalf("workspace: %v", err)
	}

	client := llm.NewKoboldClient(*backend, *model)
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

	// Subagents get the same client and workspace but no Delegate tool,
	// so a task can't recurse into itself forever.
	// Reuses the same OS-aware shell choice as tools.RunShell, but lives
	// here (not in internal/agent) since agent must not depend on tools —
	// tools already depends on agent for Delegate's Spawn type.
	var verify func(ctx context.Context) (string, bool)
	if *verifyCmd != "" {
		verify = func(ctx context.Context) (string, bool) {
			var cmd *exec.Cmd
			if runtime.GOOS == "windows" {
				cmd = exec.CommandContext(ctx, "cmd", "/C", *verifyCmd)
			} else {
				cmd = exec.CommandContext(ctx, "sh", "-c", *verifyCmd)
			}
			cmd.Dir = ws.Root()
			out, err := cmd.CombinedOutput()
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

	subTools := agent.NewRegistry(
		tools.ReadFile{WS: ws},
		tools.WriteFile{WS: ws},
		tools.PatchFile{WS: ws},
		tools.PatchLines{WS: ws},
		tools.ListFiles{WS: ws},
		tools.MoveFile{WS: ws},
		tools.RunShell{WS: ws},
		tools.GrepFiles{WS: ws},
		tools.StartBackground{WS: ws, Procs: bgProcs},
		tools.CheckBackground{Procs: bgProcs},
		tools.StopBackground{Procs: bgProcs},
		tools.CheckURL{},
	)
	spawnSub := func(role string) *agent.Agent {
		sys := systemPrompt
		if role != "" {
			sys += "\n\nFor this specific task, adopt this identity and let it consistently shape " +
				"your tone, voice, and choices in anything you write or decide: " + role
		}
		return agent.New(agent.Config{
			Client:       client,
			Tools:        subTools,
			System:       sys,
			MaxTokens:    *maxTokens,
			ContextLimit: contextLimit,
			Verify:       verify,
		})
	}

	mainTools := agent.NewRegistry(
		tools.ReadFile{WS: ws},
		tools.WriteFile{WS: ws},
		tools.PatchFile{WS: ws},
		tools.PatchLines{WS: ws},
		tools.ListFiles{WS: ws},
		tools.MoveFile{WS: ws},
		tools.RunShell{WS: ws},
		tools.GrepFiles{WS: ws},
		tools.StartBackground{WS: ws, Procs: bgProcs},
		tools.CheckBackground{Procs: bgProcs},
		tools.StopBackground{Procs: bgProcs},
		tools.CheckURL{},
		tools.Delegate{Spawn: spawnSub},
	)

	a := agent.New(agent.Config{
		Client:       client,
		Tools:        mainTools,
		System:       systemPrompt,
		MaxTokens:    *maxTokens,
		ContextLimit: contextLimit,
		StateFile:    stateFile,
		Verify:       verify,
		OnStep: func(step int, msg llm.Message) {
			if msg.Content != "" {
				fmt.Printf("[step %d] %s\n", step, msg.Content)
			}
			for _, tc := range msg.ToolCalls {
				fmt.Printf("[step %d] -> %s(%s)\n", step, tc.Name, string(tc.Arguments))
			}
		},
	})

	var result string
	if *resume != "" {
		history, err := agent.LoadState(*resume)
		if err != nil {
			log.Fatalf("resume: %v", err)
		}
		fmt.Printf("resuming from %s (%d messages)\n", *resume, len(history))
		result, err = a.Resume(ctx, history, resumeNote)
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
	fmt.Println(result)
}

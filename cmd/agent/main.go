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

const systemPrompt = `You are a careful local coding agent. You have tools to read, write, patch, ` +
	`list, and move/rename files, plus a shell command and the ability to delegate self-contained ` +
	`subtasks. Always use move_file to rename or move a file — never a shell command or ` +
	`read_file+write_file — since move_file is the only way to guarantee the content is ` +
	`preserved exactly. For small edits to an existing file, use patch_file instead of rewriting ` +
	`the whole file with write_file. If you're not sure a file or path exists, call list_files to ` +
	`check before trying to read, write, or move it. ` +
	`When a task naturally splits into multiple independent, non-overlapping pieces of work — ` +
	`several similar files to create, several unrelated things to check or build — issue one ` +
	`delegate_task call per piece in the SAME step rather than doing them one at a time yourself; ` +
	`independent delegate_task calls in one step run in parallel. If a subtask calls for a ` +
	`specific expertise, behavior, or identity (a strict reviewer mindset, a language-only ` +
	`specialist, a named character), pass it via delegate_task's role field — that actually ` +
	`shapes how the subagent behaves, not just text inside the task description. If a skills/ ` +
	`directory exists in the workspace, check it with list_files before starting an unfamiliar or ` +
	`complex task — read_file any relevant SKILL.md for guidance before proceeding. Writing code ` +
	`or content in your own response text does NOT save it anywhere — a task that asks you to ` +
	`create or modify a file is only done once you have actually called write_file, patch_file, ` +
	`patch_lines, or move_file; this applies whether you're doing the work yourself or you are a ` +
	`subagent given a task by delegate_task. Work step by step and prefer simple solutions.`

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

	subTools := agent.NewRegistry(
		tools.ReadFile{WS: ws},
		tools.WriteFile{WS: ws},
		tools.PatchFile{WS: ws},
		tools.PatchLines{WS: ws},
		tools.ListFiles{WS: ws},
		tools.MoveFile{WS: ws},
		tools.RunShell{WS: ws},
		tools.GrepFiles{WS: ws},
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

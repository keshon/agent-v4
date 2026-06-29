package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"agent-v4/internal/workspace"
)

type RunShell struct {
	WS      *workspace.Workspace
	Timeout time.Duration // defaults to 30s if zero
}

func (RunShell) Name() string { return "run_shell" }
func (RunShell) Description() string {
	if runtime.GOOS == "windows" {
		return "Run a command inside the workspace via cmd.exe (use Windows commands: " +
			"dir, ren, copy, move, del) and return its combined output."
	}
	return "Run a command inside the workspace via sh (use POSIX commands: " +
		"ls, mv, cp, rm) and return its combined output."
}
func (RunShell) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"command": {"type": "string"}},
		"required": ["command"]
	}`)
}

func (t RunShell) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}

	timeout := t.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := shellCommand(ctx, in.Command)
	cmd.Dir = t.WS.Root()

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	result := out.String()
	if err != nil {
		return result, fmt.Errorf("command failed: %w", err)
	}
	return result, nil
}

// shellCommand picks a shell that actually understands the commands a
// model writes for this host OS. On Windows that's cmd.exe — ren, dir,
// copy and friends are cmd.exe builtins with no standalone executable, so
// running them through a POSIX sh (even one present via Git Bash) just
// fails with "command not found".
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

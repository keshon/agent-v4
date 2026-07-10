package mission

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"agent-v4/internal/workspace"
)

// checkShellTimeout bounds a shell check the same way tools.RunShell
// bounds the model's own commands — a check that hangs is a failed
// check, not a stuck mission.
const checkShellTimeout = 120 * time.Second

// RunCheck executes a subtask's declared check mechanically and returns
// what it measured. ok is the verdict; output is the evidence (fed to
// fix workers verbatim, so it should carry real command output, not a
// paraphrase). A check the harness can't even attempt (bad type, path
// escaping the workspace) is a failure with the reason as output — never
// a silent pass.
func RunCheck(ctx context.Context, c Check, ws *workspace.Workspace) (output string, ok bool) {
	switch c.Type {
	case "", "none":
		return "(no check declared)", true

	case "file_exists":
		full, err := ws.Resolve(c.Path)
		if err != nil {
			return fmt.Sprintf("check path rejected: %v", err), false
		}
		info, err := os.Stat(full)
		if err != nil {
			return fmt.Sprintf("file %s does not exist", c.Path), false
		}
		if info.IsDir() {
			return fmt.Sprintf("%s exists but is a directory, not a file", c.Path), false
		}
		if info.Size() == 0 {
			// A weak model "completing" a subtask by writing an empty file
			// is a known failure shape — existence alone proves nothing.
			return fmt.Sprintf("file %s exists but is empty (0 bytes)", c.Path), false
		}
		return fmt.Sprintf("file %s exists (%d bytes)", c.Path, info.Size()), true

	case "shell":
		out, err := RunShellCommand(ctx, c.Cmd, ws.Root())
		if err != nil {
			return fmt.Sprintf("command failed: %v\n%s", err, out), false
		}
		if out == "" {
			out = "(no output)"
		}
		return out, true

	case "http":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
		if err != nil {
			return fmt.Sprintf("bad check URL: %v", err), false
		}
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Sprintf("GET %s failed: %v", c.URL, err), false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Sprintf("GET %s returned %s, want 200", c.URL, resp.Status), false
		}
		return fmt.Sprintf("GET %s returned 200", c.URL), true

	default:
		return fmt.Sprintf("unknown check type %q", c.Type), false
	}
}

// RunShellCommand runs one command line in dir with the same OS-aware
// shell choice as tools.RunShell (cmd.exe on Windows, sh elsewhere) and
// returns combined output. Shared by shell checks and main.go's
// -verify-cmd hook so the two can never drift apart.
func RunShellCommand(ctx context.Context, command, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, checkShellTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

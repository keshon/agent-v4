//go:build windows

package tools

import (
	"fmt"
	"os/exec"
)

// setNewProcessGroup is a no-op on Windows: taskkill /T below walks the
// process tree by PID and doesn't need the child pre-grouped the way the
// Unix setpgid approach does.
func setNewProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills cmd's whole process tree. /T (tree) is what
// makes this different from cmd.Process.Kill() — without it, killing a
// "cmd /C npm run dev" wrapper can leave the actual node process running
// as an orphan, still holding the port.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
}

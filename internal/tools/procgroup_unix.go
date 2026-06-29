//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setNewProcessGroup puts cmd in its own process group before it starts,
// so killProcessGroup can take down the whole subtree (e.g. sh -> npm ->
// node) instead of just the immediate child.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup signals the entire process group, not just cmd's
// direct PID. Without this, killing a shell wrapper can leave its actual
// child (the real dev server) running as an orphan — and that orphan
// holding the output pipe open is exactly what makes cmd.Wait() hang
// forever waiting for EOF that will never come.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

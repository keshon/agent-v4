//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// processGroup puts a background command in its own process group so the
// whole subtree (sh -> npm -> node) can be signalled at once. Killing only
// the direct child leaves the real server running as an orphan, and that
// orphan holding the output pipe is what makes cmd.Wait block.
type processGroup struct{}

func newProcessGroup() *processGroup { return &processGroup{} }

func (g *processGroup) beforeStart(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func (g *processGroup) afterStart(cmd *exec.Cmd) error { return nil }

func (g *processGroup) kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func (g *processGroup) release() {}

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// staleLockAfter is how long a lock file may sit before it is assumed to
// belong to a run that died rather than one still going. Long enough to
// cover doom-lite, short enough that a crash does not block the next day.
const staleLockAfter = 3 * time.Hour

// takeLock stops two evals sharing one backend.
//
// Live, and the reason this exists: a sweep was running when three more
// eval runs were fired at the same server. Every timing in the overlap
// became a measurement of contention — one probe read 197.9s against 49s
// for its own next run — while the pass/fail data stayed clean, so
// nothing looked wrong. That is the same defect as an unstated model or
// unstated sampling: the number is real, the thing it measures is not
// what the reader assumes.
//
// Correctness survives contention; wall time does not. The lock is
// advisory and -force overrides it, because sometimes two runs against
// different backends genuinely are fine.
// holderAlive reports whether the process named in a lock file is still
// running. An unreadable or malformed lock is treated as held, since
// guessing wrong in that direction only costs a wait.
func holderAlive(stamp string) bool {
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(stamp), "pid %d", &pid); err != nil || pid <= 0 {
		return true
	}
	if pid == os.Getpid() {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false // Windows: FindProcess opens the process, so this means gone
	}
	if runtime.GOOS == "windows" {
		return true
	}
	// Unix: FindProcess always succeeds, so probe with signal 0.
	return p.Signal(syscall.Signal(0)) == nil
}

func takeLock(outDir string, force bool) (release func(), err error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(outDir, ".eval-lock")

	if info, statErr := os.Stat(path); statErr == nil {
		age := time.Since(info.ModTime())
		owner, _ := os.ReadFile(path)
		// A killed run never releases its lock, and waiting out the
		// staleness window for a process that is already gone is a worse
		// failure than the contention this guards against. The lock
		// records a pid; ask whether it is still there.
		if !holderAlive(string(owner)) {
			age = staleLockAfter
		}
		if age < staleLockAfter && !force {
			return nil, fmt.Errorf(
				"another eval has been running for %s and shares this backend:\n  %s\n"+
					"Timings measured across two runs record contention, not the agent. "+
					"Wait for it, or pass -force if they use different backends",
				age.Round(time.Second), strings.TrimSpace(string(owner)))
		}
	}

	stamp := fmt.Sprintf("pid %d started %s", os.Getpid(), time.Now().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(stamp), 0o644); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

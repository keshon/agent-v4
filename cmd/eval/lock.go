package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
func takeLock(outDir string, force bool) (release func(), err error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(outDir, ".eval-lock")

	if info, statErr := os.Stat(path); statErr == nil {
		age := time.Since(info.ModTime())
		if age < staleLockAfter && !force {
			owner, _ := os.ReadFile(path)
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

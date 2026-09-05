package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTakeLock_RefusesWhileAnotherRunHoldsIt(t *testing.T) {
	dir := t.TempDir()
	release, err := takeLock(dir, false)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer release()

	if _, err := takeLock(dir, false); err == nil {
		t.Fatal("a second eval started while the first held the lock")
	} else if !strings.Contains(err.Error(), "another eval") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestTakeLock_ForceOverrides(t *testing.T) {
	dir := t.TempDir()
	release, err := takeLock(dir, false)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer release()

	if _, err := takeLock(dir, true); err != nil {
		t.Fatalf("-force did not override: %v", err)
	}
}

// A crashed run must not block the machine indefinitely.
func TestTakeLock_IgnoresAStaleLock(t *testing.T) {
	dir := t.TempDir()
	release, err := takeLock(dir, false)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	release()

	path := filepath.Join(dir, ".eval-lock")
	if err := os.WriteFile(path, []byte("pid 1 started long ago"), 0o644); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}
	old := time.Now().Add(-staleLockAfter - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("age the lock: %v", err)
	}
	if _, err := takeLock(dir, false); err != nil {
		t.Fatalf("a stale lock blocked a new run: %v", err)
	}
}

func TestTakeLock_ReleasesSoTheNextRunCanStart(t *testing.T) {
	dir := t.TempDir()
	release, err := takeLock(dir, false)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	release()
	if _, err := takeLock(dir, false); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
}

// A killed run never releases its lock. Waiting out the staleness window
// for a process that is already gone is a worse failure than the
// contention the lock guards against — and it happened the first time
// this guard was used in anger.
func TestTakeLock_IgnoresALockWhoseProcessIsGone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".eval-lock")
	// A pid that cannot be running: pid 0 is never a user process, and
	// the stamp is otherwise well formed and fresh.
	if err := os.WriteFile(path,
		[]byte("pid 999999 started "+time.Now().Format(time.RFC3339)), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	release, err := takeLock(dir, false)
	if err != nil {
		t.Fatalf("a dead run's lock blocked a new one: %v", err)
	}
	release()
}

func TestHolderAlive_TreatsThisProcessAsRunning(t *testing.T) {
	if !holderAlive("pid " + strconv.Itoa(os.Getpid()) + " started now") {
		t.Error("this process reported as gone")
	}
}

// A lock we cannot parse is treated as held: guessing wrong that way only
// costs a wait, the other way corrupts a measurement.
func TestHolderAlive_UnparseableLockIsTreatedAsHeld(t *testing.T) {
	for _, stamp := range []string{"", "garbage", "pid notanumber"} {
		if !holderAlive(stamp) {
			t.Errorf("unparseable lock %q treated as free", stamp)
		}
	}
}

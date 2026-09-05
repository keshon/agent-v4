package main

import (
	"os"
	"path/filepath"
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

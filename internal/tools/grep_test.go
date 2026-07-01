package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-v4/internal/workspace"
)

func TestGrepFiles_FindsMatchesAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nfunc Foo() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\nfunc Bar() { Foo() }\n"), 0o644)

	tool := GrepFiles{WS: ws}
	args, _ := json.Marshal(map[string]string{"path": ".", "pattern": "Foo"})
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "a.go:2:") || !strings.Contains(out, "b.go:2:") {
		t.Fatalf("expected matches in both files, got: %q", out)
	}
}

func TestGrepFiles_GlobFilter(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("token\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("token\n"), 0o644)

	tool := GrepFiles{WS: ws}
	args, _ := json.Marshal(map[string]string{"path": ".", "pattern": "token", "glob": "*.go"})
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "a.go") || strings.Contains(out, "a.txt") {
		t.Fatalf("glob filter not applied correctly: %q", out)
	}
}

func TestGrepFiles_NoMatches(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("nothing here\n"), 0o644)

	tool := GrepFiles{WS: ws}
	args, _ := json.Marshal(map[string]string{"path": ".", "pattern": "zzz_not_present"})
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "no matches" {
		t.Fatalf("got %q, want %q", out, "no matches")
	}
}

func TestGrepFiles_BadPatternErrors(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	tool := GrepFiles{WS: ws}
	args, _ := json.Marshal(map[string]string{"path": ".", "pattern": "("})
	if _, err := tool.Run(context.Background(), args); err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestGrepFiles_EmptyPatternErrors(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	tool := GrepFiles{WS: ws}
	args, _ := json.Marshal(map[string]string{"path": ".", "pattern": ""})
	if _, err := tool.Run(context.Background(), args); err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestGrepFiles_UnknownFieldErrors(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	tool := GrepFiles{WS: ws}
	args, _ := json.Marshal(map[string]string{"path": ".", "command": "foo"})
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown field mention", err)
	}
}

func TestGrepFiles_SkipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)

	// Reproduces the real failure: a binary file with long stretches of
	// non-newline bytes that a wildcard pattern would happily "match",
	// dumping raw control-character garbage into the result.
	binData := make([]byte, 5000)
	for i := range binData {
		binData[i] = byte(i % 251) // varied bytes, deliberately avoiding newlines mostly
	}
	binData[100] = 0 // the actual signal looksBinary checks for
	os.WriteFile(filepath.Join(dir, "app.exe"), binData, 0o644)
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package main\nfunc mask() {}\n"), 0o644)

	tool := GrepFiles{WS: ws}
	args, _ := json.Marshal(map[string]string{"path": ".", "pattern": ".*"})
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out, "app.exe") {
		preview := out
		if len(preview) > 200 {
			preview = preview[:200]
		}
		t.Fatalf("binary file should have been skipped entirely, got: %q", preview)
	}
	if !strings.Contains(out, "app.go") {
		t.Fatalf("expected the real text file to still match, got: %q", out)
	}
}

func TestGrepFiles_TruncatesLongLines(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)

	longLine := "needle" + strings.Repeat("x", 2000)
	os.WriteFile(filepath.Join(dir, "minified.js"), []byte(longLine+"\n"), 0o644)

	tool := GrepFiles{WS: ws}
	args, _ := json.Marshal(map[string]string{"path": ".", "pattern": "needle"})
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out) > 1000 {
		t.Fatalf("expected the long line to be truncated, result was %d bytes", len(out))
	}
	if !strings.Contains(out, "...(truncated)") {
		t.Fatalf("expected a truncation marker, got: %q", out)
	}
}

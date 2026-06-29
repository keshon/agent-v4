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

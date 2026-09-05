package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keshon/tars/internal/workspace"
)

func TestPatchFile_ReplacesUniqueOccurrence(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	path := filepath.Join(dir, "game.js")
	if err := os.WriteFile(path, []byte("let score = 0;\nfunction draw() {}\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	tool := PatchFile{WS: ws}
	args, _ := json.Marshal(map[string]string{
		"path":        "game.js",
		"old_content": "let score = 0;",
		"new_content": "let score = 0;\nlet lives = 3;",
	})
	if _, err := tool.Run(context.Background(), args); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	want := "let score = 0;\nlet lives = 3;\nfunction draw() {}\n"
	if string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestPatchFile_MissingContentErrors(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "game.js"), []byte("let score = 0;\n"), 0o644)

	tool := PatchFile{WS: ws}
	args, _ := json.Marshal(map[string]string{
		"path":        "game.js",
		"old_content": "let lives = 3;",
		"new_content": "let lives = 5;",
	})
	if _, err := tool.Run(context.Background(), args); err == nil {
		t.Fatal("expected an error when old_content isn't found, got nil")
	}
}

func TestPatchFile_AmbiguousMatchErrors(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "game.js"), []byte("x = 1;\nx = 1;\n"), 0o644)

	tool := PatchFile{WS: ws}
	args, _ := json.Marshal(map[string]string{
		"path":        "game.js",
		"old_content": "x = 1;",
		"new_content": "x = 2;",
	})
	if _, err := tool.Run(context.Background(), args); err == nil {
		t.Fatal("expected an error when old_content matches more than once, got nil")
	}
}

func TestPatchLines_ReplacesRange(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0o644)

	tool := PatchLines{WS: ws}
	args, _ := json.Marshal(map[string]any{
		"path": "f.txt", "start_line": 2, "end_line": 3, "new_content": "X\nY",
	})
	if _, err := tool.Run(context.Background(), args); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "a\nX\nY\nd\ne\n"
	if string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestPatchLines_InvalidRangeErrors(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\nb\n"), 0o644)

	tool := PatchLines{WS: ws}
	args, _ := json.Marshal(map[string]any{
		"path": "f.txt", "start_line": 5, "end_line": 1, "new_content": "x",
	})
	if _, err := tool.Run(context.Background(), args); err == nil {
		t.Fatal("expected error for end_line < start_line")
	}
}

func TestPatchFile_UnknownFieldErrors(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "game.js"), []byte("x\n"), 0o644)

	tool := PatchFile{WS: ws}
	args, _ := json.Marshal(map[string]string{"path": "game.js", "command": "sed"})
	if _, err := tool.Run(context.Background(), args); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestPatchLines_UnknownFieldErrors(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\n"), 0o644)

	tool := PatchLines{WS: ws}
	args, _ := json.Marshal(map[string]string{"path": "f.txt", "command": "sed"})
	if _, err := tool.Run(context.Background(), args); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

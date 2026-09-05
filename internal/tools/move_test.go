package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keshon/tars/internal/workspace"
)

func TestMoveFile_PreservesContent(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}

	const want = "soft light upon the hill\n"
	if err := os.WriteFile(filepath.Join(dir, "poem.txt"), []byte(want), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	tool := MoveFile{WS: ws}
	args, _ := json.Marshal(map[string]string{"from": "poem.txt", "to": "стихи.txt"})
	if _, err := tool.Run(context.Background(), args); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "poem.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still exists after move (err=%v)", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "стихи.txt"))
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestMoveFile_MissingSourceErrors(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	tool := MoveFile{WS: ws}

	args, _ := json.Marshal(map[string]string{"from": "ghost.txt", "to": "renamed.txt"})
	if _, err := tool.Run(context.Background(), args); err == nil {
		t.Fatal("expected an error for a nonexistent source file, got nil")
	}
}

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

func TestReadFile_TruncatesLargeFiles(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	large := strings.Repeat("x", 200*1024)
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(large), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	args, _ := json.Marshal(map[string]string{"path": "big.txt"})
	out, err := ReadFile{WS: ws}.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "...(content truncated at ") {
		t.Fatalf("expected truncation marker, got len %d", len(out))
	}
	if !strings.Contains(out, "FILE\n") || !strings.Contains(out, "truncated: true") || !strings.Contains(out, "size: ") {
		t.Fatalf("expected structured FILE header, got: %q", out[:minLen(200, len(out))])
	}
	if len(out) >= len(large) {
		t.Fatalf("output not truncated: len %d", len(out))
	}
}

func TestReadFile_SmallFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	content := "hello world"
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	args, _ := json.Marshal(map[string]string{"path": "small.txt"})
	out, err := ReadFile{WS: ws}.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "FILE\n") || !strings.Contains(out, "truncated: false") {
		t.Fatalf("expected structured FILE header, got: %q", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Fatalf("expected file content in output, got: %q", out)
	}
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

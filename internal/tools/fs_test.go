package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/workspace"
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

func TestReadFile_MetadataOnly_NoBody(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	large := strings.Repeat("x", 200*1024)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(large), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"path": "big.txt", "metadata_only": true})
	out, err := ReadFile{WS: ws}.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out, "----") {
		t.Fatalf("metadata_only should not include body separator, got len %d", len(out))
	}
	if !strings.Contains(out, "size: 204800") || !strings.Contains(out, "truncated: true") {
		t.Fatalf("expected size/truncated facts in header, got: %q", out)
	}
	if len(out) > 200 {
		t.Fatalf("metadata_only output too large: %d bytes", len(out))
	}
}

// max_bytes=0 must mean "no explicit cap" — every model reads 0 as the
// universal "unlimited" convention. The original 0-means-header-only
// semantics sent a live Qwen run into an 18-repeat read loop: it kept
// asking for content with max_bytes=0 and kept getting a contentless
// header it couldn't understand.
func TestReadFile_MaxBytesZero_ReturnsFullContent(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello"), 0o644)

	zero := 0
	args, _ := json.Marshal(map[string]any{"path": "f.txt", "max_bytes": zero})
	out, err := ReadFile{WS: ws}.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("max_bytes=0 must return the full content, got: %q", out)
	}
}

func TestReadFile_MaxBytesPositive_CapsContent(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello world"), 0o644)

	args, _ := json.Marshal(map[string]any{"path": "f.txt", "max_bytes": 5})
	out, err := ReadFile{WS: ws}.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "hello") || strings.Contains(out, "world") {
		t.Fatalf("max_bytes=5 should cap at 5 bytes, got: %q", out)
	}
}

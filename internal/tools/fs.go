// Package tools holds concrete agent.Tool implementations: filesystem
// access, shell execution, and delegation to subagents. Each tool is a
// small, self-contained type — no shared "dispatcher" indirection.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-v4/internal/agent"
	"agent-v4/internal/workspace"
)

const readMaxBytes = 128 * 1024 // cap one read_file result — mirrors grep_files safety nets

type ReadFile struct{ WS *workspace.Workspace }

func (ReadFile) Name() string         { return "read_file" }
func (ReadFile) Description() string  { return "Read the full contents of a text file." }
func (ReadFile) Mode() agent.ToolMode { return agent.Concurrent }
func (ReadFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"path": {"type": "string"}},
		"required": ["path"]
	}`)
}

func (t ReadFile) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	full, err := t.WS.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	rel, _ := filepath.Rel(t.WS.Root(), full)
	return formatReadFileResult(rel, data), nil
}

func formatReadFileResult(path string, data []byte) string {
	totalSize := len(data)
	truncated := totalSize > readMaxBytes
	binary := looksBinaryBytes(data)

	var b strings.Builder
	b.WriteString("FILE\n")
	fmt.Fprintf(&b, "path: %s\n", path)
	fmt.Fprintf(&b, "size: %d\n", totalSize)
	if truncated {
		b.WriteString("truncated: true\n")
	} else {
		b.WriteString("truncated: false\n")
	}
	if binary {
		b.WriteString("binary: true\n")
	}
	b.WriteString("----\n")
	if truncated {
		b.Write(data[:readMaxBytes])
		fmt.Fprintf(&b, "\n...(content truncated at %d bytes — use grep_files or read a smaller section)", readMaxBytes)
	} else {
		b.Write(data)
	}
	return b.String()
}

func looksBinaryBytes(data []byte) bool {
	limit := len(data)
	if limit > grepSniffBytes {
		limit = grepSniffBytes
	}
	for _, b := range data[:limit] {
		if b == 0 {
			return true
		}
	}
	return false
}

type WriteFile struct{ WS *workspace.Workspace }

func (WriteFile) Name() string         { return "write_file" }
func (WriteFile) Mode() agent.ToolMode { return agent.Exclusive }
func (WriteFile) Description() string {
	return "Write text content to a file, creating parent directories as needed."
}
func (WriteFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"content": {"type": "string"}
		},
		"required": ["path", "content"]
	}`)
}

func (t WriteFile) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	full, err := t.WS.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(in.Content), 0o644); err != nil {
		return "", err
	}
	return "ok", nil
}

type ListFiles struct{ WS *workspace.Workspace }

func (ListFiles) Name() string         { return "list_files" }
func (ListFiles) Mode() agent.ToolMode { return agent.Concurrent }
func (ListFiles) Description() string {
	return "List files and directories under a path, non-recursively."
}
func (ListFiles) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"path": {"type": "string"}},
		"required": ["path"]
	}`)
}

func (t ListFiles) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	full, err := t.WS.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return "", err
	}
	out := ""
	for _, e := range entries {
		if e.IsDir() {
			out += e.Name() + "/\n"
		} else {
			out += e.Name() + "\n"
		}
	}
	return out, nil
}

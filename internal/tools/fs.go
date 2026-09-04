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

func (ReadFile) Name() string { return "read_file" }
func (ReadFile) Description() string {
	return "Read a text file. Returns a FILE header (path, size, truncated, binary) and the full " +
		"content. Set metadata_only=true to get only the header — use this for file size or " +
		"type checks without loading content into context."
}
func (ReadFile) Mode() agent.ToolMode { return agent.Concurrent }

// Idempotent: identical read_file calls return identical results until
// something mutates the workspace — the agent loop uses this to
// short-circuit exact-repeat calls instead of re-reading (see
// Registry.IdempotentOf).
func (ReadFile) Idempotent() bool { return true }

func (ReadFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"metadata_only": {
				"type": "boolean",
				"description": "if true, return only the FILE header (size, truncated, binary) with no body"
			},
			"max_bytes": {
				"type": "integer",
				"description": "optional cap on content bytes; omit or 0 for the full content"
			}
		},
		"required": ["path"]
	}`)
}

func (t ReadFile) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path         string `json:"path"`
		MetadataOnly bool   `json:"metadata_only"`
		MaxBytes     *int   `json:"max_bytes"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if err := rejectUnknownFields(args, "path", "metadata_only", "max_bytes"); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("path is required and must be non-empty")
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

	// max_bytes=0 (or omitted) means "no explicit cap", NOT metadata-only:
	// every model tested reads 0 as the universal "unlimited" convention.
	// The old 0-means-header-only semantics sent a weak model into a
	// repeat loop — 18 identical header-only reads in one live run,
	// because it kept asking for content with max_bytes=0 and kept
	// getting four lines of metadata. Header-only is spelled
	// metadata_only=true and nothing else.
	maxBody := readMaxBytes
	if in.MetadataOnly {
		maxBody = 0
	} else if in.MaxBytes != nil {
		if *in.MaxBytes < 0 {
			return "", fmt.Errorf("max_bytes must be >= 0")
		}
		if *in.MaxBytes > 0 {
			maxBody = *in.MaxBytes
		}
	}
	return formatReadFileResult(rel, data, maxBody), nil
}

// maxBodyBytes: 0 = header only; otherwise cap body at min(maxBodyBytes, readMaxBytes).
func formatReadFileResult(path string, data []byte, maxBodyBytes int) string {
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
	if maxBodyBytes == 0 {
		return b.String()
	}
	limit := maxBodyBytes
	if limit > readMaxBytes {
		limit = readMaxBytes
	}
	b.WriteString("----\n")
	if truncated {
		b.Write(data[:limit])
		if limit < totalSize {
			fmt.Fprintf(&b, "\n...(content truncated at %d bytes — use grep_files, metadata_only, or read a smaller section)", limit)
		}
	} else if limit < totalSize {
		b.Write(data[:limit])
		fmt.Fprintf(&b, "\n...(content truncated at %d bytes)", limit)
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
	return fmt.Sprintf("wrote %s (%d bytes)", in.Path, len(in.Content)), nil
}

type ListFiles struct{ WS *workspace.Workspace }

func (ListFiles) Name() string         { return "list_files" }
func (ListFiles) Mode() agent.ToolMode { return agent.Concurrent }
func (ListFiles) Idempotent() bool     { return true }
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

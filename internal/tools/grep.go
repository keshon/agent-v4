package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"tars/internal/agent"
	"tars/internal/workspace"
)

// GrepFiles searches file contents by regex instead of requiring read_file
// on every candidate — the same "remove the failure surface" reasoning as
// move_file: a Go-native search can't be defeated by cmd.exe vs sh syntax
// differences the way shelling out to findstr/grep would be.
type GrepFiles struct{ WS *workspace.Workspace }

func (GrepFiles) Name() string         { return "grep_files" }
func (GrepFiles) Mode() agent.ToolMode { return agent.Concurrent }
func (GrepFiles) Idempotent() bool     { return true }
func (GrepFiles) Description() string {
	return "Search file contents under a path (recursive) for a regex pattern. Returns " +
		"path:line:content for each match, capped at 200 matches; long lines are truncated. " +
		"Binary files (compiled executables, images, etc) are skipped automatically. Use this " +
		"instead of read_file-ing many files just to find where something is."
}
func (GrepFiles) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "directory to search, recursive"},
			"pattern": {"type": "string", "description": "regular expression"},
			"glob": {"type": "string", "description": "optional filename glob filter, e.g. *.go"}
		},
		"required": ["path", "pattern"]
	}`)
}

const (
	grepMaxMatches = 200
	grepMaxLineLen = 500  // truncate pathologically long matched lines (minified JS, etc)
	grepSniffBytes = 8000 // how much of a file to check for binary content before scanning
)

// looksBinary mirrors the heuristic git itself uses: if a null byte shows
// up in the first chunk of a file, treat it as binary and skip it
// entirely. Without this, grep_files will happily regex-scan a compiled
// binary (it did, in practice: a wildcard pattern matched across a long
// stretch of beacon.exe with no newlines, and raw non-UTF8 bytes —
// including control characters — went straight into the conversation
// history as a "match"). That's not just wasted context: it's exactly
// the kind of corrupted history that can derail everything after it.
func looksBinary(f *os.File) (bool, error) {
	buf := make([]byte, grepSniffBytes)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if _, seekErr := f.Seek(0, 0); seekErr != nil {
		return false, seekErr
	}
	for _, b := range buf[:n] {
		if b == 0 {
			return true, nil
		}
	}
	return false, nil
}

func (t GrepFiles) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
		Glob    string `json:"glob"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if err := rejectUnknownFields(args, "path", "pattern", "glob"); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("path is required and must be non-empty")
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return "", fmt.Errorf("pattern is required and must be non-empty")
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return "", fmt.Errorf("bad pattern: %w", err)
	}
	root, err := t.WS.Resolve(in.Path)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	matches := 0
	walkErr := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || matches >= grepMaxMatches {
			return nil
		}
		if in.Glob != "" {
			if ok, _ := filepath.Match(in.Glob, info.Name()); !ok {
				return nil
			}
		}
		f, err := os.Open(p)
		if err != nil {
			return nil // unreadable file (permissions, etc) — skip, not fatal
		}
		defer f.Close()

		if binary, err := looksBinary(f); err != nil || binary {
			return nil
		}

		rel, _ := filepath.Rel(t.WS.Root(), p)
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		lineNum := 0
		for scanner.Scan() && matches < grepMaxMatches {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				if len(line) > grepMaxLineLen {
					line = line[:grepMaxLineLen] + "...(truncated)"
				}
				fmt.Fprintf(&out, "%s:%d:%s\n", rel, lineNum, line)
				matches++
			}
		}
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	if matches == 0 {
		return "no matches", nil
	}
	if matches >= grepMaxMatches {
		out.WriteString("... (capped at 200 matches, narrow your pattern or path)\n")
	}
	return out.String(), nil
}

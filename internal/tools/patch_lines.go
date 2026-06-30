package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"agent-v4/internal/agent"
	"agent-v4/internal/workspace"
)

// PatchLines replaces lines [start_line, end_line] (1-indexed, inclusive)
// with new_content. Complements PatchFile for edits where the target text
// isn't unique enough to match by content (e.g. "fix line 42").
type PatchLines struct{ WS *workspace.Workspace }

func (PatchLines) Name() string         { return "patch_lines" }
func (PatchLines) Mode() agent.ToolMode { return agent.Exclusive }
func (PatchLines) Description() string {
	return "Replace a range of lines (1-indexed, inclusive) in a file with new content. " +
		"Use this when editing by line number is clearer than matching exact text with patch_file."
}
func (PatchLines) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"start_line": {"type": "integer"},
			"end_line": {"type": "integer"},
			"new_content": {"type": "string"}
		},
		"required": ["path", "start_line", "end_line", "new_content"]
	}`)
}

func (t PatchLines) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path       string `json:"path"`
		StartLine  int    `json:"start_line"`
		EndLine    int    `json:"end_line"`
		NewContent string `json:"new_content"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if in.StartLine < 1 || in.EndLine < in.StartLine {
		return "", fmt.Errorf("invalid range: start_line=%d end_line=%d", in.StartLine, in.EndLine)
	}

	full, err := t.WS.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(data), "\n")
	if in.StartLine > len(lines) {
		return "", fmt.Errorf("start_line %d is beyond end of file (%d lines)", in.StartLine, len(lines))
	}
	end := in.EndLine
	if end > len(lines) {
		end = len(lines)
	}

	replacement := strings.Split(in.NewContent, "\n")
	result := append([]string{}, lines[:in.StartLine-1]...)
	result = append(result, replacement...)
	result = append(result, lines[end:]...)

	if err := os.WriteFile(full, []byte(strings.Join(result, "\n")), 0o644); err != nil {
		return "", err
	}
	return "ok", nil
}

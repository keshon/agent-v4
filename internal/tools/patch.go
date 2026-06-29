package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"agent-v4/internal/workspace"
)

// PatchFile replaces one exact occurrence of old_content with new_content
// inside an existing file. It exists so a model can make a small, surgical
// edit to a large file without rewriting the whole thing through
// write_file — which is exactly the kind of single huge generation that
// risks getting cut off (see Config.MaxTokens).
type PatchFile struct{ WS *workspace.Workspace }

func (PatchFile) Name() string { return "patch_file" }
func (PatchFile) Description() string {
	return "Replace one exact occurrence of old_content with new_content in an existing file. " +
		"Use this for small edits instead of rewriting the whole file with write_file."
}
func (PatchFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"old_content": {"type": "string"},
			"new_content": {"type": "string"}
		},
		"required": ["path", "old_content", "new_content"]
	}`)
}

func (t PatchFile) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path       string `json:"path"`
		OldContent string `json:"old_content"`
		NewContent string `json:"new_content"`
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

	content := string(data)
	count := strings.Count(content, in.OldContent)
	if count == 0 {
		return "", fmt.Errorf("old_content not found in %s", in.Path)
	}
	if count > 1 {
		return "", fmt.Errorf("old_content appears %d times in %s — make it unique before patching", count, in.Path)
	}

	newContent := strings.Replace(content, in.OldContent, in.NewContent, 1)
	if err := os.WriteFile(full, []byte(newContent), 0o644); err != nil {
		return "", err
	}
	return "ok", nil
}

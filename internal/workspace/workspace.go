// Package workspace draws a hard line between "where the agent's code
// lives" and "where the agent is allowed to read and write files while
// doing a task". Every filesystem tool goes through a Workspace instead of
// touching os.* directly.
package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Workspace struct {
	root string
}

func New(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	return &Workspace{root: abs}, nil
}

func (w *Workspace) Root() string { return w.root }

// Resolve turns a path relative to the workspace into an absolute path,
// refusing anything that would escape the workspace root.
func (w *Workspace) Resolve(rel string) (string, error) {
	full := filepath.Clean(filepath.Join(w.root, rel))
	if full != w.root && !strings.HasPrefix(full, w.root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace root", rel)
	}
	return full, nil
}

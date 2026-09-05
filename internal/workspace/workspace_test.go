package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testWS(t *testing.T) (*Workspace, string) {
	t.Helper()
	// EvalSymlinks because the OS temp dir is itself a symlink on macOS
	// (/var -> /private/var); without it every containment check here
	// would compare two spellings of the same directory.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	ws, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ws, root
}

func TestResolve_AllowsPathsInsideRoot(t *testing.T) {
	ws, root := testWS(t)
	for _, rel := range []string{"a.txt", "sub/a.txt", "./a.txt", "sub/../a.txt", "."} {
		got, err := ws.Resolve(rel)
		if err != nil {
			t.Errorf("Resolve(%q) = error %v, want allowed", rel, err)
			continue
		}
		if got != root && !strings.HasPrefix(got, root+string(filepath.Separator)) {
			t.Errorf("Resolve(%q) = %q, outside root %q", rel, got, root)
		}
	}
}

// The whole point of the type: every file tool routes through Resolve, so
// anything it lets through is somewhere the agent can write.
func TestResolve_RejectsEscapes(t *testing.T) {
	ws, _ := testWS(t)
	escapes := []string{
		"..",
		"../outside.txt",
		"../../outside.txt",
		"sub/../../outside.txt",
		"a/b/../../../outside.txt",
	}
	if runtime.GOOS == "windows" {
		escapes = append(escapes, `..\outside.txt`, `sub\..\..\outside.txt`)
	}
	for _, rel := range escapes {
		if got, err := ws.Resolve(rel); err == nil {
			t.Errorf("Resolve(%q) = %q, want rejected as an escape", rel, got)
		}
	}
}

// A sibling directory whose name merely starts with the root's name is
// not inside the root: /tmp/ws must not admit /tmp/ws-evil. This is why
// the containment test needs the separator and cannot be a bare prefix.
func TestResolve_RejectsSiblingWithRootAsNamePrefix(t *testing.T) {
	ws, root := testWS(t)
	rel := ".." + string(filepath.Separator) + filepath.Base(root) + "-evil"
	if got, err := ws.Resolve(rel); err == nil {
		t.Errorf("Resolve(%q) = %q, want rejected — sibling, not child", rel, got)
	}
}

// Absolute paths are joined onto the root rather than replacing it, so
// they land inside. Documented here because it is the behaviour a caller
// depends on, not an accident.
func TestResolve_AbsolutePathIsContained(t *testing.T) {
	ws, root := testWS(t)
	got, err := ws.Resolve(filepath.Join(string(filepath.Separator), "etc", "passwd"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasPrefix(got, root+string(filepath.Separator)) {
		t.Errorf("Resolve(absolute) = %q, want contained under %q", got, root)
	}
}

// Clean() is textual: it cannot see that a symlink inside the workspace
// points out of it. This is a real hole — a tool that follows the link
// writes outside the sandbox — and this test documents it rather than
// asserting the safe behaviour, so that closing it with EvalSymlinks is
// a visible change here rather than a silent one.
func TestResolve_SymlinkEscape_IsCurrentlyAllowed(t *testing.T) {
	ws, root := testWS(t)
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlinks on this machine: %v", err)
	}

	resolved, err := ws.Resolve("link/escaped.txt")
	if err != nil {
		t.Fatalf("Resolve returned an error, so the hole is closed — "+
			"update this test to assert rejection: %v", err)
	}
	real, err := filepath.EvalSymlinks(filepath.Dir(resolved))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if strings.HasPrefix(real, root+string(filepath.Separator)) {
		t.Fatal("symlink did not actually leave the workspace; test is not measuring what it claims")
	}
	t.Logf("KNOWN GAP: %q resolves inside the root textually but really points at %q", "link/escaped.txt", real)
}

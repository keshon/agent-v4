package mission

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keshon/tars/internal/workspace"
)

// Map generation is mechanical on purpose: a walk over the workspace
// produces the directory tree, a file-type histogram, and the heads of
// manifest-shaped files — facts a weak model cannot get wrong because it
// never produces them. The optional model-written notes (see runExplore)
// are appended enrichment, never something the pipeline depends on.

const (
	mapBudgetChars    = 8 * 1024
	manifestHeadLines = 30
	manifestHeadChars = 1024
	maxManifests      = 6
	maxMapDirs        = 40
)

// manifestNames are files whose opening lines say more about a project
// than any amount of tree structure: what it is, how it builds.
var manifestNames = map[string]bool{
	"go.mod": true, "package.json": true, "readme.md": true, "readme.txt": true,
	"makefile": true, "index.html": true, "requirements.txt": true,
	"pyproject.toml": true, "cargo.toml": true, "cmakelists.txt": true,
	"composer.json": true, "build.gradle": true, "pom.xml": true,
}

// BuildMap renders the mechanical codebase map, hard-capped at
// mapBudgetChars with an honest truncation marker.
func BuildMap(ws *workspace.Workspace) string {
	type dirStat struct {
		files int
		bytes int64
	}
	dirs := map[string]*dirStat{}
	exts := map[string]int{}
	var manifests []string

	filepath.WalkDir(ws.Root(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == ws.Root() {
			return nil
		}
		rel, rerr := filepath.Rel(ws.Root(), path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dirs[dir] == nil {
			dirs[dir] = &dirStat{}
		}
		info, ierr := d.Info()
		var size int64
		if ierr == nil {
			size = info.Size()
		}
		dirs[dir].files++
		dirs[dir].bytes += size

		ext := strings.ToLower(filepath.Ext(rel))
		if ext == "" {
			ext = "(none)"
		}
		exts[ext]++

		if manifestNames[strings.ToLower(d.Name())] && len(manifests) < maxManifests {
			manifests = append(manifests, rel)
		}
		return nil
	})

	if len(dirs) == 0 {
		return ""
	}

	var b strings.Builder

	b.WriteString("## Directory tree (files, bytes)\n")
	dirNames := make([]string, 0, len(dirs))
	for d := range dirs {
		dirNames = append(dirNames, d)
	}
	sort.Strings(dirNames)
	dirsDropped := 0
	if len(dirNames) > maxMapDirs {
		dirsDropped = len(dirNames) - maxMapDirs
		dirNames = dirNames[:maxMapDirs]
	}
	for _, d := range dirNames {
		st := dirs[d]
		name := d
		if name == "." {
			name = "(root)"
		}
		fmt.Fprintf(&b, "%s — %d files, %d bytes\n", name, st.files, st.bytes)
	}
	if dirsDropped > 0 {
		fmt.Fprintf(&b, "…(%d more directories not shown)\n", dirsDropped)
	}

	b.WriteString("\n## File types\n")
	extNames := make([]string, 0, len(exts))
	for e := range exts {
		extNames = append(extNames, e)
	}
	sort.Slice(extNames, func(i, j int) bool {
		if exts[extNames[i]] != exts[extNames[j]] {
			return exts[extNames[i]] > exts[extNames[j]]
		}
		return extNames[i] < extNames[j]
	})
	var parts []string
	for _, e := range extNames {
		parts = append(parts, fmt.Sprintf("%s ×%d", e, exts[e]))
	}
	b.WriteString(strings.Join(parts, ", ") + "\n")

	sort.Strings(manifests)
	for _, mpath := range manifests {
		full, err := ws.Resolve(mpath)
		if err != nil {
			continue
		}
		head := readHead(full)
		if head == "" {
			continue
		}
		fmt.Fprintf(&b, "\n## %s (head)\n%s\n", mpath, head)
	}

	out := b.String()
	if len(out) > mapBudgetChars {
		out = out[:mapBudgetChars]
		if cut := strings.LastIndexByte(out, '\n'); cut > 0 {
			out = out[:cut]
		}
		out += "\n…(map truncated)"
	}
	return strings.TrimRight(out, "\n")
}

// readHead returns the first manifestHeadLines lines (bounded by
// manifestHeadChars) of a file, or "" for unreadable/binary-looking ones.
func readHead(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > manifestHeadChars*4 {
		data = data[:manifestHeadChars*4]
	}
	// Same null-byte sniff grep_files uses: never feed binary to a model.
	for _, c := range data {
		if c == 0 {
			return ""
		}
	}
	text := string(data)
	lines := strings.Split(text, "\n")
	if len(lines) > manifestHeadLines {
		lines = lines[:manifestHeadLines]
		text = strings.Join(lines, "\n") + "\n…"
	}
	if len(text) > manifestHeadChars {
		text = text[:manifestHeadChars] + "…"
	}
	return strings.TrimRight(text, "\n")
}

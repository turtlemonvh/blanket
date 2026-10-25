// Package docs serves the markdown pages under docs/ (keyed by short name)
// to the blanket_docs MCP tool, whole or one section at a time, and
// searches them with an in-memory BM25 index over their sections
// (index.go).
//
// go:embed can't reach files above its own package directory, so this
// package can't embed ../docs/*.md itself -- see #66. Instead main (the
// root package, which sits next to docs/) embeds the tree and calls SetFS
// at startup; tests that don't run through main call SetFS themselves,
// typically with os.DirFS pointed at the repo's docs/ directory.
package docs

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"github.com/turtlemonvh/blanket/lib"
)

// pages maps the short keys used by the blanket_docs MCP tool and the
// /ui/docs pages to filenames in docs/. "overview" points at
// docs/README.md (the docs index) rather than the top-level repo README.
// design.md is left out on purpose: it describes goals, not current
// behavior, so links to it go to GitHub.
var pages = map[string]string{
	"overview":  "README.md",
	"authoring": "authoring_task_types.md",
	"schema":    "task_type_definitions.md",
	"tags":      "tag_ontology.md",
	"usage":     "usage.md",
	"api":       "api.md",
	"flow":      "task_flow.md",
	"upgrade":   "upgrade.md",
	"install":   "install.md",
	"offline":   "offline_install.md",
	"autostart": "autostart.md",
	"mcp":       "mcp.md",
}

var (
	mu    sync.RWMutex
	files fs.FS
)

// SetFS registers the filesystem containing the markdown doc pages (the
// contents of docs/, not docs/ itself). Call it once at startup before
// Page is used; it may also be called again in tests, which discards
// any index built from the previous filesystem.
func SetFS(f fs.FS) {
	mu.Lock()
	defer mu.Unlock()
	files = f
	state = &indexState{}
}

// Keys returns the valid page keys, sorted, for building the blanket_docs
// tool's description and error messages.
func Keys() []string {
	keys := make([]string, 0, len(pages))
	for k := range pages {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// unknownPageError is the error Page and Read return for an unrecognized
// key: it names the closest real keys (turtlemonvh/blanket#229) and lists
// them all.
func unknownPageError(key string) error {
	return fmt.Errorf("unknown doc page %q; did you mean: %s? valid pages are: %s",
		key, strings.Join(lib.ClosestMatches(key, Keys(), 3), ", "), strings.Join(Keys(), ", "))
}

// Page returns the raw contents of the doc page for key, or an error
// listing valid keys if key is unrecognized.
func Page(key string) (string, error) {
	filename, ok := pages[key]
	if !ok {
		return "", unknownPageError(key)
	}

	mu.RLock()
	f := files
	mu.RUnlock()
	if f == nil {
		return "", fmt.Errorf("docs filesystem not initialized; call docs.SetFS at startup")
	}

	b, err := fs.ReadFile(f, filename)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// KeyForFile returns the page key whose file is name (e.g. "api.md" ->
// "api"), for turning links between doc pages into links between UI
// pages. ok is false for a file that isn't in the page map.
func KeyForFile(name string) (key string, ok bool) {
	for k, f := range pages {
		if f == name {
			return k, true
		}
	}
	return "", false
}

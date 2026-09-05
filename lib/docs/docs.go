// Package docs serves the markdown pages under docs/ (keyed by short name)
// to the blanket_docs MCP tool.
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
)

// pages maps the short keys used by the blanket_docs MCP tool to filenames
// in docs/. "overview" points at docs/README.md (the docs index) rather
// than the top-level repo README.
var pages = map[string]string{
	"overview":  "README.md",
	"authoring": "authoring_task_types.md",
	"schema":    "task_type_definitions.md",
	"tags":      "tag_ontology.md",
	"usage":     "usage.md",
	"api":       "api.md",
	"flow":      "task_flow.md",
}

var (
	mu    sync.RWMutex
	files fs.FS
)

// SetFS registers the filesystem containing the markdown doc pages (the
// contents of docs/, not docs/ itself). Call it once at startup before
// Page is used; it may also be called again in tests.
func SetFS(f fs.FS) {
	mu.Lock()
	defer mu.Unlock()
	files = f
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

// Page returns the raw contents of the doc page for key, or an error
// listing valid keys if key is unrecognized.
func Page(key string) (string, error) {
	filename, ok := pages[key]
	if !ok {
		return "", fmt.Errorf("unknown doc page %q; valid pages are: %s", key, strings.Join(Keys(), ", "))
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

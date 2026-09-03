// docs/embed.go
package docs

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.md
var files embed.FS

// pages maps the short keys used by the blanket_docs MCP tool to filenames
// in this directory. "overview" points at docs/README.md (the docs index)
// rather than the top-level repo README — go:embed can't reach above its
// own package directory.
var pages = map[string]string{
	"overview":  "README.md",
	"authoring": "authoring_task_types.md",
	"schema":    "task_type_definitions.md",
	"tags":      "tag_ontology.md",
	"usage":     "usage.md",
	"api":       "api.md",
	"flow":      "task_flow.md",
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
	b, err := files.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

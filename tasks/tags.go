package tasks

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SeedTags is blanket's built-in namespaced tag vocabulary. See
// docs/tag_ontology.md for the rationale behind each namespace and why
// tags are worker-matching constraints, not descriptive labels.
var SeedTags = []string{
	"os:linux", "os:darwin", "os:windows", "os:unix",
	"exec:bash", "exec:cmd", "exec:powershell",
	"runtime:python3", "runtime:node", "runtime:docker",
	"resource:gpu", "resource:bigmem", "resource:internet",
	"env:prod", "env:staging",
	"team:data-eng",
	"cost:rnd",
	"access:secrets", "access:vpn",
}

// Origins a tag can have in the resolved known-tag set.
const (
	OriginBuiltin  = "builtin"
	OriginFile     = "file"
	OriginObserved = "observed"
)

// KnownTag is one entry in the resolved known-tag set, with its origin —
// what blanket task-validate --dump-known-tags prints.
type KnownTag struct {
	Tag    string `json:"tag"`
	Origin string `json:"origin"`
}

// KnownTagsOptions controls how the known-tag set is resolved.
type KnownTagsOptions struct {
	// NoBuiltinTags excludes SeedTags from the result — user-declared and
	// observed tags only. Set via `task-validate --no-builtin-tags`.
	NoBuiltinTags bool
}

// ResolveKnownTags computes the known-tag set: the built-in seed
// vocabulary (unless disabled), tags declared in
// <typesDir>/.blanket/known-tags.conf and
// <typesDir>/.blanket/known-tags.d/*.conf beside each configured types
// directory, and tags observed across all loaded task types — one
// occurrence anywhere is enough for a tag to count as known. A tag's
// origin is whichever source is checked first (builtin, then file, then
// observed); a tag present in more than one source keeps its
// earliest-checked origin.
//
// Result is sorted by tag name for stable --dump-known-tags output.
func ResolveKnownTags(typesDirs []string, tts []TaskType, opts KnownTagsOptions) []KnownTag {
	origin := map[string]string{}
	var order []string
	add := func(tag, o string) {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			return
		}
		if _, exists := origin[tag]; exists {
			return
		}
		origin[tag] = o
		order = append(order, tag)
	}

	if !opts.NoBuiltinTags {
		for _, t := range SeedTags {
			add(t, OriginBuiltin)
		}
	}

	for _, typesDir := range typesDirs {
		for _, f := range knownTagsFiles(typesDir) {
			for _, t := range readKnownTagsFile(f) {
				add(t, OriginFile)
			}
		}
	}

	for _, tt := range tts {
		for _, t := range tt.Config.GetStringSlice("tags") {
			add(t, OriginObserved)
		}
	}

	sort.Strings(order)
	out := make([]KnownTag, 0, len(order))
	for _, t := range order {
		out = append(out, KnownTag{Tag: t, Origin: origin[t]})
	}
	return out
}

// knownTagsFiles returns the known-tags vocabulary files for one types
// directory: <typesDir>/.blanket/known-tags.conf (if present) followed by
// <typesDir>/.blanket/known-tags.d/*.conf in name order.
func knownTagsFiles(typesDir string) []string {
	var files []string
	base := filepath.Join(typesDir, ".blanket")

	single := filepath.Join(base, "known-tags.conf")
	if fi, err := os.Stat(single); err == nil && !fi.IsDir() {
		files = append(files, single)
	}

	dDir := filepath.Join(base, "known-tags.d")
	entries, err := os.ReadDir(dDir)
	if err != nil {
		return files
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		files = append(files, filepath.Join(dDir, n))
	}
	return files
}

// readKnownTagsFile parses a newline-separated tag list: one tag per line,
// blank lines and #-prefixed comments ignored. Unreadable files return nil
// rather than erroring — a missing/unreadable vocabulary file just means
// no extra tags from that source, not a validation failure.
func readKnownTagsFile(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var tags []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tags = append(tags, line)
	}
	return tags
}

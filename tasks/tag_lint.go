package tasks

import (
	"fmt"
	"sort"
	"strings"

	"github.com/turtlemonvh/blanket/lib"
)

// nearMissMaxDistance bounds how close a tag has to be (in edit distance)
// to a known tag before it's flagged as a likely typo (code 010).
const nearMissMaxDistance = 2

// TagLintOptions controls the tag-focused checks (010-014). Extension is
// meant to stay frictionless by default: a novel, well-formed namespaced
// tag is silent unless a caller opts into the stricter checks.
type TagLintOptions struct {
	KnownTagsOptions

	// WarnNewTag enables code 012: warn on a tag that appears nowhere
	// else — not in the known-tags files, not on any other loaded type.
	WarnNewTag bool

	// WarnUndeclaredTag enables code 013: warn on a tag that isn't
	// declared via the built-in vocabulary or a known-tags file, even if
	// it's already used on other task types. Stricter than WarnNewTag.
	WarnUndeclaredTag bool

	// CheckWorkers enables code 014: warn when no registered worker
	// advertises a superset of a type's tags. Requires WorkerTagSets —
	// see command/task_validate.go for how it's populated from a live
	// server; the check silently does nothing without it (the CLI layer
	// is responsible for surfacing "couldn't reach the server").
	CheckWorkers bool

	// WorkerTagSets is the tag set of every candidate worker, fetched by
	// the caller (e.g. from a running server's `GET /worker/`). Only
	// consulted when CheckWorkers is true.
	WorkerTagSets [][]string
}

// TagIndex is the resolved tag vocabulary for one validation run, indexed
// for the lint checks. Build once per run with BuildTagIndex and reuse
// across every task type being validated.
type TagIndex struct {
	// builtinOrFile is the union of the seed vocabulary (unless disabled)
	// and every tag declared in a .blanket/known-tags{.conf,.d/*.conf}
	// file — a tag here is "declared", not just "used somewhere".
	builtinOrFile map[string]bool

	// observedBy maps a tag to the set of type names that use it, so
	// "is this tag known, excluding the type being checked" (012) can be
	// answered without a tag trivially counting itself as known.
	observedBy map[string]map[string]bool

	// sortedKnown is the union of builtinOrFile and observedBy's keys,
	// sorted, for deterministic near-miss search (010/011).
	sortedKnown []string
}

// BuildTagIndex resolves the tag vocabulary for typesDirs + tts once, for
// reuse across every type being validated in the same run.
func BuildTagIndex(typesDirs []string, tts []TaskType, opts KnownTagsOptions) *TagIndex {
	idx := &TagIndex{
		builtinOrFile: map[string]bool{},
		observedBy:    map[string]map[string]bool{},
	}

	if !opts.NoBuiltinTags {
		for _, t := range SeedTags {
			idx.builtinOrFile[t] = true
		}
	}
	for _, typesDir := range typesDirs {
		for _, f := range knownTagsFiles(typesDir) {
			for _, t := range readKnownTagsFile(f) {
				idx.builtinOrFile[strings.TrimSpace(t)] = true
			}
		}
	}
	for _, tt := range tts {
		name := tt.GetName()
		for _, tag := range tt.Config.GetStringSlice("tags") {
			if idx.observedBy[tag] == nil {
				idx.observedBy[tag] = map[string]bool{}
			}
			idx.observedBy[tag][name] = true
		}
	}

	seen := map[string]bool{}
	for t := range idx.builtinOrFile {
		seen[t] = true
	}
	for t := range idx.observedBy {
		seen[t] = true
	}
	idx.sortedKnown = make([]string, 0, len(seen))
	for t := range seen {
		idx.sortedKnown = append(idx.sortedKnown, t)
	}
	sort.Strings(idx.sortedKnown)

	return idx
}

// isKnownExcluding reports whether tag counts as known from the
// perspective of excludeType — true if it's builtin/file-declared, or
// observed on some *other* loaded type. A tag only ever used by the type
// being checked does not count as known here, or code 012 could never
// fire.
func (idx *TagIndex) isKnownExcluding(tag, excludeType string) bool {
	if idx.builtinOrFile[tag] {
		return true
	}
	for typeName := range idx.observedBy[tag] {
		if typeName != excludeType {
			return true
		}
	}
	return false
}

// isDeclared reports whether tag comes from the built-in vocabulary or a
// known-tags file — narrower than isKnownExcluding, which also counts
// mere usage on another type.
func (idx *TagIndex) isDeclared(tag string) bool {
	return idx.builtinOrFile[tag]
}

// knownExcluding returns the known-tag list filtered down to tags that
// count as known from excludeType's perspective (see isKnownExcluding) —
// a tag observed only on the type currently being checked is dropped, so
// that type's own (possibly-typo'd) tag never trivially "matches itself"
// in the near-miss search below.
func (idx *TagIndex) knownExcluding(excludeType string) []string {
	out := make([]string, 0, len(idx.sortedKnown))
	for _, tag := range idx.sortedKnown {
		if idx.isKnownExcluding(tag, excludeType) {
			out = append(out, tag)
		}
	}
	return out
}

// nearest returns the closest tag to query among candidates by edit
// distance (and its distance), breaking ties by picking the
// alphabetically-first candidate for determinism (candidates is sorted).
// ok is false if candidates is empty.
func nearest(query string, candidates []string) (tag string, distance int, ok bool) {
	best := -1
	for _, candidate := range candidates {
		d := lib.Levenshtein(query, candidate)
		if best == -1 || d < best {
			best = d
			tag = candidate
		}
	}
	return tag, best, best != -1
}

// namespacedMatchesForValue returns every candidate namespaced tag
// (`namespace:value`) whose value part exactly equals query, in the
// order given. Used by code 011 — "bash" matching "exec:bash".
func namespacedMatchesForValue(query string, candidates []string) []string {
	var matches []string
	for _, candidate := range candidates {
		ns, value, found := strings.Cut(candidate, ":")
		if found && ns != "" && value == query {
			matches = append(matches, candidate)
		}
	}
	return matches
}

// LintTags runs the tag-focused checks (010-014) against one task type.
// idx must come from BuildTagIndex over every loaded type (including tt),
// so 012's "used elsewhere" logic works.
func LintTags(tt *TaskType, idx *TagIndex, opts TagLintOptions) []Finding {
	name := tt.GetName()
	var findings []Finding

	// Candidates for 010/011 exclude tags observed only on this type — a
	// type's own (possibly-typo'd) tag must never count as "already
	// known" when checking that very type, or it would trivially match
	// itself and the typo would never surface.
	candidates := idx.knownExcluding(name)

	for _, tag := range tt.Config.GetStringSlice("tags") {
		if !idx.isKnownExcluding(tag, name) {
			matched011 := false
			if !strings.Contains(tag, ":") {
				if suggestions := namespacedMatchesForValue(tag, candidates); len(suggestions) > 0 {
					findings = append(findings, Finding{
						Type: name, Code: "011", Level: LevelWarn,
						Message:    fmt.Sprintf("tag %q is unnamespaced", tag),
						Suggestion: fmt.Sprintf("did you mean %s?", quoteJoin(suggestions)),
					})
					matched011 = true
				}
			}
			if !matched011 {
				if nearTag, dist, ok := nearest(tag, candidates); ok && dist > 0 && dist <= nearMissMaxDistance {
					findings = append(findings, Finding{
						Type: name, Code: "010", Level: LevelWarn,
						Message:    fmt.Sprintf("tag %q is close to known tag %q but not identical", tag, nearTag),
						Suggestion: fmt.Sprintf("did you mean %q?", nearTag),
					})
				}
			}
		}

		if opts.WarnNewTag && !idx.isKnownExcluding(tag, name) {
			findings = append(findings, Finding{
				Type: name, Code: "012", Level: LevelWarn,
				Message: fmt.Sprintf("tag %q is new — not declared anywhere and not used by any other task type", tag),
			})
		}

		if opts.WarnUndeclaredTag && !idx.isDeclared(tag) {
			findings = append(findings, Finding{
				Type: name, Code: "013", Level: LevelWarn,
				Message:    fmt.Sprintf("tag %q is not declared in the known-tags vocabulary", tag),
				Suggestion: "add it to .blanket/known-tags.conf (or a file under .blanket/known-tags.d/) if it's intentional",
			})
		}
	}

	if opts.CheckWorkers {
		tags := tt.Config.GetStringSlice("tags")
		if !anyWorkerSatisfies(opts.WorkerTagSets, tags) {
			findings = append(findings, Finding{
				Type: name, Code: "014", Level: LevelWarn,
				Message: "no registered, non-stopped worker advertises a superset of this type's tags — it may never be claimed",
			})
		}
	}

	return findings
}

// anyWorkerSatisfies reports whether at least one worker's tag set is a
// superset of taskTags — the same rule lib/bolt/database_util.go applies
// when matching a claim. An empty workerTagSets always returns false: if
// no worker is registered at all, nothing can claim the task regardless
// of how few tags it requires.
func anyWorkerSatisfies(workerTagSets [][]string, taskTags []string) bool {
	for _, wtags := range workerTagSets {
		wset := make(map[string]bool, len(wtags))
		for _, t := range wtags {
			wset[t] = true
		}
		satisfied := true
		for _, tag := range taskTags {
			if !wset[tag] {
				satisfied = false
				break
			}
		}
		if satisfied {
			return true
		}
	}
	return false
}

func quoteJoin(ss []string) string {
	quoted := make([]string, len(ss))
	for i, s := range ss {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(quoted, " or ")
}

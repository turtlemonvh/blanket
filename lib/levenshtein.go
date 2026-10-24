package lib

import (
	"sort"
	"strings"
)

// Levenshtein returns the edit distance between a and b: the minimum
// number of single-character insertions, deletions, or substitutions
// needed to turn one string into the other.
//
// Adapted from the standard Wagner-Fischer dynamic-programming algorithm
// (Wagner & Fischer, "The String-to-String Correction Problem", 1974) —
// a two-row rolling version to keep memory at O(min(len(a), len(b)))
// rather than the full O(len(a)*len(b)) matrix. Used by
// `blanket task-validate`'s tag near-miss check (codes 010/011) rather
// than pulling in an external, largely-unmaintained fuzzy-match module
// for what's a ~30-line algorithm.
func Levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)

	// Keep the shorter string as columns to minimize row width.
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	if len(rb) == 0 {
		return len(ra)
	}

	prevRow := make([]int, len(rb)+1)
	for j := range prevRow {
		prevRow[j] = j
	}

	for i := 1; i <= len(ra); i++ {
		curRow := make([]int, len(rb)+1)
		curRow[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prevRow[j] + 1
			ins := curRow[j-1] + 1
			sub := prevRow[j-1] + cost
			curRow[j] = min3(del, ins, sub)
		}
		prevRow = curRow
	}

	return prevRow[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// ClosestMatches returns up to n of candidates, nearest to query first by
// Levenshtein distance (case-insensitive), ties broken alphabetically.
// It backs the "did you mean" hint on an unknown task type name or doc
// page key (turtlemonvh/blanket#229).
//
// There is no distance cutoff: the candidate lists it serves are short
// (task types, doc pages), and the closest real names are a better next
// guess than nothing even when none of them is very close.
func ClosestMatches(query string, candidates []string, n int) []string {
	type scored struct {
		name string
		dist int
	}
	q := strings.ToLower(query)
	all := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		all = append(all, scored{c, Levenshtein(q, strings.ToLower(c))})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].dist != all[j].dist {
			return all[i].dist < all[j].dist
		}
		return all[i].name < all[j].name
	})
	if len(all) > n {
		all = all[:n]
	}
	out := make([]string, len(all))
	for i, s := range all {
		out[i] = s.name
	}
	return out
}

package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLog writes lines to a fresh file and returns its path. The file
// is closed before returning, so nothing here holds it open on Windows.
func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "blanket.stdout.log")
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	require.NoError(t, os.WriteFile(p, []byte(content), 0644))
	return p
}

func numberedLines(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("line %d", i+1)
	}
	return out
}

func nums(res *LogSearchResult) []int {
	var out []int
	for _, l := range res.Lines {
		out = append(out, l.Num)
	}
	return out
}

func TestSearchLog_Page(t *testing.T) {
	p := writeLog(t, numberedLines(10)...)

	res, err := SearchLog(p, LogSearchOptions{Offset: 2, Limit: 3})
	require.NoError(t, err)
	assert.Equal(t, []int{3, 4, 5}, nums(res))
	assert.Equal(t, "line 3", res.Lines[0].Text)
	assert.Equal(t, 10, res.TotalLines)
	assert.Equal(t, 5, res.NextOffset)

	// The last page reports no next offset.
	res, err = SearchLog(p, LogSearchOptions{Offset: 8, Limit: 5})
	require.NoError(t, err)
	assert.Equal(t, []int{9, 10}, nums(res))
	assert.Equal(t, -1, res.NextOffset)
}

func TestSearchLog_NegativeOffsetIsTail(t *testing.T) {
	p := writeLog(t, numberedLines(10)...)

	res, err := SearchLog(p, LogSearchOptions{Offset: -3, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []int{8, 9, 10}, nums(res))
	assert.Equal(t, 7, res.Start)
	assert.Equal(t, -1, res.NextOffset)

	// Further back than the file is long just starts at the top.
	res, err = SearchLog(p, LogSearchOptions{Offset: -1000, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, nums(res))
	assert.Equal(t, 2, res.NextOffset)
}

func TestSearchLog_GrepWithContext(t *testing.T) {
	// Matches at lines 3, 5 and 12: 3 and 5 share context, 12 stands alone.
	lines := numberedLines(15)
	lines[2] = "ERROR one"
	lines[4] = "ERROR two"
	lines[11] = "ERROR three"
	p := writeLog(t, lines...)

	res, err := SearchLog(p, LogSearchOptions{Grep: regexp.MustCompile(`ERROR`), Context: 2, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 3, res.TotalMatches)
	assert.Equal(t, 3, res.Count)
	assert.Equal(t, []int{1, 2, 3, 4, 5, 6, 7, 10, 11, 12, 13, 14}, nums(res))
	for _, l := range res.Lines {
		assert.Equal(t, strings.HasPrefix(l.Text, "ERROR"), l.Match, "line %d", l.Num)
	}
	assert.Equal(t, -1, res.NextOffset)
}

func TestSearchLog_GrepPagesThroughMatches(t *testing.T) {
	lines := numberedLines(30)
	for _, i := range []int{4, 9, 14, 19, 24} {
		lines[i] = fmt.Sprintf("hit %d", i+1)
	}
	p := writeLog(t, lines...)
	re := regexp.MustCompile(`^hit`)

	res, err := SearchLog(p, LogSearchOptions{Grep: re, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []int{5, 10}, nums(res))
	assert.Equal(t, 5, res.TotalMatches)
	assert.Equal(t, 2, res.NextOffset)

	res, err = SearchLog(p, LogSearchOptions{Grep: re, Offset: res.NextOffset, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []int{15, 20}, nums(res))
	assert.Equal(t, 4, res.NextOffset)

	// A negative offset counts matches back from the end.
	res, err = SearchLog(p, LogSearchOptions{Grep: re, Offset: -1, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []int{25}, nums(res))
	assert.Equal(t, -1, res.NextOffset)
}

func TestSearchLog_LongLinesTruncated(t *testing.T) {
	long := strings.Repeat("x", 5000)
	huge := strings.Repeat("y", logSearchMatchBytes+1000) // past what's kept in memory
	p := writeLog(t, "short", long, huge)

	res, err := SearchLog(p, LogSearchOptions{Limit: 10, MaxLineChars: 2000})
	require.NoError(t, err)
	require.Len(t, res.Lines, 3)
	assert.Equal(t, "short", res.Lines[0].Text)
	assert.Equal(t, strings.Repeat("x", 2000)+"…[+3000 chars]", res.Lines[1].Text)
	assert.Equal(t, strings.Repeat("y", 2000)+fmt.Sprintf("…[+%d chars]", logSearchMatchBytes+1000-2000), res.Lines[2].Text)
}

func TestSearchLog_PageTooLargeRefused(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = strings.Repeat("z", 1000)
	}
	p := writeLog(t, lines...)

	_, err := SearchLog(p, LogSearchOptions{Limit: 20, MaxPageChars: 5000})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLogPageTooLarge))
	assert.Contains(t, err.Error(), "smaller limit")

	// A page that fits is fine.
	res, err := SearchLog(p, LogSearchOptions{Limit: 4, MaxPageChars: 5000})
	require.NoError(t, err)
	assert.Len(t, res.Lines, 4)
}

func TestSearchLog_MissingFile(t *testing.T) {
	_, err := SearchLog(filepath.Join(t.TempDir(), "nope.log"), LogSearchOptions{Limit: 5})
	assert.True(t, errors.Is(err, os.ErrNotExist))
	_, err = SearchLog(filepath.Join(t.TempDir(), "nope.log"), LogSearchOptions{Offset: -5, Limit: 5})
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestSearchLog_NoTrailingNewlineAndCRLF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "blanket.stdout.log")
	require.NoError(t, os.WriteFile(p, []byte("a\r\nb\r\nc"), 0644))

	res, err := SearchLog(p, LogSearchOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Lines, 3)
	assert.Equal(t, "a", res.Lines[0].Text)
	assert.Equal(t, "c", res.Lines[2].Text)
}

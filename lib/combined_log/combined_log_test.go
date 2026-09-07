package combined_log

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readRecords parses a whole combined log file.
func readRecords(t *testing.T, p string) []Record {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	var out []Record
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		rec, ok := ParseRecord(line)
		require.True(t, ok, "unparseable record %q", line)
		out = append(out, rec)
	}
	return out
}

func newWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), FileName)
	w, err := Create(p)
	require.NoError(t, err)
	return w, p
}

// The point of the file: writes land in the order they were made, with
// the stream each came from attached.
func TestWriter_RecordsArrivalOrder(t *testing.T) {
	w, p := newWriter(t)
	out, errw := w.Stream("stdout"), w.Stream("stderr")

	fmt.Fprint(out, "one\n")
	fmt.Fprint(errw, "two\n")
	fmt.Fprint(out, "three\n")
	require.NoError(t, w.Close())

	recs := readRecords(t, p)
	require.Len(t, recs, 3)
	assert.Equal(t, []string{"one", "two", "three"},
		[]string{recs[0].Line, recs[1].Line, recs[2].Line})
	assert.Equal(t, []string{"stdout", "stderr", "stdout"},
		[]string{recs[0].Stream, recs[1].Stream, recs[2].Stream})
	// seq counts within a stream, not within the file.
	assert.Equal(t, []int{1, 1, 2},
		[]int{recs[0].Seq, recs[1].Seq, recs[2].Seq})
	for _, r := range recs {
		assert.Greater(t, r.Ts, int64(0), "every record is timestamped")
	}
}

// A write is not a line: the child's writes are chunked however the
// runtime felt like chunking them, and a record has to be a whole line
// or the interleaving is recorded at the wrong granularity.
func TestWriter_BuffersPartialLines(t *testing.T) {
	w, p := newWriter(t)
	out, errw := w.Stream("stdout"), w.Stream("stderr")

	fmt.Fprint(out, "hel")
	fmt.Fprint(errw, "a whole stderr line\n")
	fmt.Fprint(out, "lo\nworld\n")
	require.NoError(t, w.Close())

	recs := readRecords(t, p)
	require.Len(t, recs, 3)
	// The stderr line completed first, so it is recorded first even
	// though stdout wrote first.
	assert.Equal(t, "a whole stderr line", recs[0].Line)
	assert.Equal(t, "stderr", recs[0].Stream)
	assert.Equal(t, "hello", recs[1].Line)
	assert.Equal(t, "world", recs[2].Line)
}

// Several complete lines in one write are several records.
func TestWriter_SplitsMultiLineWrites(t *testing.T) {
	w, p := newWriter(t)
	fmt.Fprint(w.Stream("stdout"), "a\nb\nc\n")
	require.NoError(t, w.Close())

	recs := readRecords(t, p)
	require.Len(t, recs, 3)
	assert.Equal(t, []string{"a", "b", "c"},
		[]string{recs[0].Line, recs[1].Line, recs[2].Line})
}

// A task that ends mid-line must not lose that line.
func TestWriter_FlushesPartialLinesOnClose(t *testing.T) {
	w, p := newWriter(t)
	fmt.Fprint(w.Stream("stdout"), "no newline here")
	fmt.Fprint(w.Stream("stderr"), "nor here")
	require.NoError(t, w.Close())

	recs := readRecords(t, p)
	require.Len(t, recs, 2)
	// Stable order for the leftovers, whatever the map iteration says.
	assert.Equal(t, "stdout", recs[0].Stream)
	assert.Equal(t, "no newline here", recs[0].Line)
	assert.Equal(t, "stderr", recs[1].Stream)
	assert.Equal(t, "nor here", recs[1].Line)
}

// Lines are trimmed the way tailed_file trims them: the newline goes,
// and nothing else does.
func TestWriter_TrimsOnlyTheNewline(t *testing.T) {
	w, p := newWriter(t)
	fmt.Fprint(w.Stream("stdout"), "  padded  \r\n")
	require.NoError(t, w.Close())

	recs := readRecords(t, p)
	require.Len(t, recs, 1)
	assert.Equal(t, "  padded  \r", recs[0].Line)
}

// Close is idempotent, and a write after it is dropped rather than
// panicking on a closed file -- the copy goroutines feeding this thing
// are not ours to sequence.
func TestWriter_CloseIsIdempotent(t *testing.T) {
	w, p := newWriter(t)
	out := w.Stream("stdout")
	fmt.Fprint(out, "one\n")
	require.NoError(t, w.Close())
	require.NoError(t, w.Close())
	fmt.Fprint(out, "after close\n")

	recs := readRecords(t, p)
	require.Len(t, recs, 1)
	assert.Equal(t, "one", recs[0].Line)
}

// Both streams are written from their own goroutine (os/exec copies each
// pipe in one), so the file has to hold up under concurrent writers.
func TestWriter_ConcurrentStreams(t *testing.T) {
	w, p := newWriter(t)

	var wg sync.WaitGroup
	for _, name := range []string{"stdout", "stderr"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			s := w.Stream(name)
			for i := 0; i < 200; i++ {
				fmt.Fprintf(s, "%s %d\n", name, i)
			}
		}(name)
	}
	wg.Wait()
	require.NoError(t, w.Close())

	recs := readRecords(t, p)
	require.Len(t, recs, 400)
	// Every line is whole, and each stream's own lines keep their order.
	next := map[string]int{"stdout": 0, "stderr": 0}
	for _, r := range recs {
		assert.Equal(t, fmt.Sprintf("%s %d", r.Stream, next[r.Stream]), r.Line)
		assert.Equal(t, next[r.Stream]+1, r.Seq)
		next[r.Stream]++
	}
}

// An unterminated line can't be allowed to buffer forever: past
// maxLineBytes it is cut and recorded anyway, and no bytes are dropped
// in the process.
func TestWriter_CutsAnEndlessLine(t *testing.T) {
	w, p := newWriter(t)
	out := w.Stream("stdout")
	const chunk = 4096
	total := maxLineBytes + 10
	for written := 0; written < total; written += chunk {
		n := chunk
		if rem := total - written; rem < n {
			n = rem
		}
		fmt.Fprint(out, strings.Repeat("x", n))
	}
	require.NoError(t, w.Close())

	recs := readRecords(t, p)
	require.Len(t, recs, 2, "cut once at the cap, with the remainder flushed at close")
	assert.Equal(t, maxLineBytes, len(recs[0].Line))
	assert.Equal(t, total, len(recs[0].Line)+len(recs[1].Line))
}

func TestParseRecord(t *testing.T) {
	rec, ok := ParseRecord(`{"ts":1756900001000,"stream":"stderr","seq":3,"line":"hi"}`)
	require.True(t, ok)
	assert.Equal(t, Record{Ts: 1756900001000, Stream: "stderr", Seq: 3, Line: "hi"}, rec)

	for _, bad := range []string{"", "   ", "not json", "{}", `{"line":"no stream"}`} {
		_, ok := ParseRecord(bad)
		assert.False(t, ok, "expected %q to be rejected", bad)
	}
}

// A nil Writer is the "knob is off" case: the worker holds one either
// way and must not have to nil-check at every call site.
func TestNilWriterIsInert(t *testing.T) {
	var w *Writer
	fmt.Fprint(w.Stream("stdout"), "dropped\n")
	assert.NoError(t, w.Sync())
	assert.NoError(t, w.Err())
	assert.NoError(t, w.Close())
}

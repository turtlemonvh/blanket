//go:build linux

package proclive

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The linux implementation is pure parsing over procfs, so the interesting
// cases are text ones -- above all a comm field containing spaces and
// parentheses, which is what naive "split the line on spaces and take field
// 22" parsers get wrong. Getting it wrong means reading a garbage start
// time, which the tolerance check then reports as a pid-reuse mismatch:
// "this worker is conclusively dead" about a perfectly healthy one.

func TestParseStatStartTicks(t *testing.T) {
	// Field layout: pid (comm) state ppid pgrp session tty_nr tpgid flags
	// minflt cminflt majflt cmajflt utime stime cutime cstime priority nice
	// num_threads itrealvalue starttime ...  -- starttime is field 22, i.e.
	// the 20th value after the closing paren.
	tail := " S 1 1 1 0 -1 4194304 100 0 0 0 5 5 0 0 20 0 1 0 987654 " +
		"1000 2000 3000"

	cases := []struct {
		name  string
		line  string
		want  int64
		wantK bool
	}{
		{"plain comm", "4213 (blanket)" + tail, 987654, true},
		{"comm with spaces", "4213 (my prog)" + tail, 987654, true},
		{"comm with parens", "4213 (my (old) prog)" + tail, 987654, true},
		{"truncated", "4213 (blanket) S 1 1", 0, false},
		{"no comm", "4213 blanket S 1 1", 0, false},
		{"empty", "", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseStatStartTicks(tc.line)
			assert.Equal(t, tc.wantK, ok)
			if tc.wantK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestStartTime_ReadsFakeProcfs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stat"),
		[]byte("cpu 1 2 3\nbtime 1700000000\nprocesses 42\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "4213"), 0755))
	// 500 ticks at 100Hz = 5s after boot.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "4213", "stat"),
		[]byte("4213 (blanket worker) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 500 0 0 0\n"), 0644))

	restoreRoot, restoreBoot := procRoot, cachedBootTime
	procRoot, cachedBootTime = dir, 0
	defer func() { procRoot, cachedBootTime = restoreRoot, restoreBoot }()

	ts, ok := StartTime(4213)
	require.True(t, ok)
	assert.Equal(t, int64(1700000005), ts)

	exists, err := processExists(4213)
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = processExists(4214)
	require.NoError(t, err)
	assert.False(t, exists, "a pid with no /proc entry does not exist")

	// The full contract, over the fake tree: a matching start time is
	// conclusively alive, a mismatched one conclusively dead.
	alive, conclusive := IsAlive(4213, 1700000005)
	assert.True(t, alive)
	assert.True(t, conclusive)

	alive, conclusive = IsAlive(4213, 1699000000)
	assert.False(t, alive)
	assert.True(t, conclusive)
}

func TestStartTime_ToleratesSecondOfSkew(t *testing.T) {
	// The recorded value round-trips through unix seconds while procfs
	// quantises to clock ticks against a second-resolution boot time, so a
	// second either way must still read as the same process.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stat"),
		[]byte("btime 1700000000\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "77"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "77", "stat"),
		[]byte("77 (x) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 1000 0\n"), 0644))

	restoreRoot, restoreBoot := procRoot, cachedBootTime
	procRoot, cachedBootTime = dir, 0
	defer func() { procRoot, cachedBootTime = restoreRoot, restoreBoot }()

	for _, recorded := range []int64{1700000009, 1700000010, 1700000011} {
		alive, conclusive := IsAlive(77, recorded)
		assert.True(t, alive, "recorded %d", recorded)
		assert.True(t, conclusive, "recorded %d", recorded)
	}
	alive, _ := IsAlive(77, 1700000030)
	assert.False(t, alive, "20s of drift is a different process")
}

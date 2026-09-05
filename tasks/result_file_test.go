// Tests for the `result_file` task type key (turtlemonvh/blanket#27):
// the containment rule, and the fact that a violating value keeps the
// whole type from loading rather than being tolerated until read time.

package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanResultFile_Accepted(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"result.json":         "result.json",
		"  result.json  ":     "result.json",
		"./result.json":       "result.json",
		"out/result.json":     "out/result.json",
		"out/./result.json":   "out/result.json",
		"out/sub/../res.json": "out/res.json",
		// Windows-style separators normalize to the same relative path,
		// so a type authored on Windows loads on a Linux worker.
		`out\result.json`: "out/result.json",
	}
	for raw, want := range cases {
		got, err := CleanResultFile(raw)
		assert.NoError(t, err, "expected %q to be accepted", raw)
		assert.Equal(t, want, got, "cleaning %q", raw)
	}
}

func TestCleanResultFile_Rejected(t *testing.T) {
	cases := []string{
		"/etc/passwd",
		"/result.json",
		"../result.json",
		"../../etc/passwd",
		"out/../../result.json",
		"..",
		".",
		`\\server\share\result.json`,
		`C:\result.json`,
		`..\result.json`,
	}
	for _, raw := range cases {
		_, err := CleanResultFile(raw)
		assert.Error(t, err, "expected %q to be rejected", raw)
	}
}

// A type declaring an escaping result_file is rejected at load, so it
// never reaches the serving path at all.
func TestReadTaskType_RejectsEscapingResultFile(t *testing.T) {
	_, err := ReadTaskType(strings.NewReader("command = \"true\"\nresult_file = \"../escape.json\"\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "result_file")
}

func TestReadTaskType_AcceptsContainedResultFile(t *testing.T) {
	tt, err := ReadTaskType(strings.NewReader("command = \"true\"\nresult_file = \"out/result.json\"\n"))
	require.NoError(t, err)

	rel, err := tt.ResultFile()
	assert.NoError(t, err)
	assert.Equal(t, "out/result.json", rel)
}

func TestReadTaskTypeFromFilepath_RejectsEscapingResultFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "escapes.toml")
	require.NoError(t, os.WriteFile(fp, []byte("command = \"true\"\nresult_file = \"/etc/passwd\"\n"), 0644))

	_, err := ReadTaskTypeFromFilepath(fp)
	assert.Error(t, err)
}

// 009 reports the same problem through task-validate, which uses the
// lenient loader and so sees types the serving path drops.
func TestValidateTaskType_ResultFileFinding(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "escapes.toml")
	require.NoError(t, os.WriteFile(fp, []byte("command = \"true\"\nresult_file = \"../escape.json\"\n"), 0644))

	tt, loadErr := ReadTaskTypeFromFilepathForValidation(fp)
	findings := ValidateTaskType(&tt, loadErr)

	var found *Finding
	for i := range findings {
		if findings[i].Code == "009" {
			found = &findings[i]
			break
		}
	}
	require.NotNil(t, found, "expected a 009 finding, got %#v", findings)
	assert.Equal(t, LevelError, found.Level)
	assert.Contains(t, found.Message, "escape")
}

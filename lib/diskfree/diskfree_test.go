package diskfree

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAvailableReportsSomethingPlausible(t *testing.T) {
	got, err := Available(t.TempDir())

	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		// The unported case is a first-class answer, not a failure; see
		// the package comment.
		assert.True(t, errors.Is(err, ErrUnsupported))
		return
	}

	require.NoError(t, err)
	// A temp dir on a machine capable of running the test suite has some
	// room on it. The point is only that a real number came back rather
	// than a zero from a silently-failing syscall.
	assert.Greater(t, got, uint64(0))
}

func TestAvailableOnAMissingPath(t *testing.T) {
	_, err := Available(filepath.Join(t.TempDir(), "definitely-not-here"))
	assert.Error(t, err, "a path that does not exist has no filesystem to report on")
}

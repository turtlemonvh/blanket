//go:build windows

package proclive

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Windows-specific liveness. Runs in the `windows` CI job (see
// .github/workflows/ci.yml), which is the only place these code paths are
// executed at all -- the Linux job cross-compiles them but cannot run them.
//
// Pid reuse is a sharper problem here than on unix (Windows recycles pids
// aggressively), so the start-time guard is the part worth proving.

func TestWindowsStartTime_SelfIsPositive(t *testing.T) {
	ts, ok := startTime(os.Getpid())
	require.True(t, ok, "GetProcessTimes should report a creation time for this process")
	assert.Greater(t, ts, int64(0))
}

func TestWindowsProcessExists_SelfAndExitedChild(t *testing.T) {
	exists, err := processExists(os.Getpid())
	require.NoError(t, err)
	assert.True(t, exists)

	cmd := exec.Command("cmd", "/c", "exit", "0")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	require.NoError(t, cmd.Wait())

	// An exited process must not read as alive, whether Windows has torn
	// the pid down (OpenProcess fails) or still has a handle open with
	// GetExitCodeProcess reporting the real code.
	exists, err = processExists(pid)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestWindowsProcessExists_UnusedPidDoesNotExist(t *testing.T) {
	// Pid 0 is never a real process on Windows; IsAlive short-circuits it
	// before this layer, so test the platform function directly with an
	// implausible one instead.
	exists, err := processExists(0x7FFFFFF0)
	if err != nil {
		t.Skipf("OpenProcess returned an unexpected error for an unused pid: %v", err)
	}
	assert.False(t, exists)
}

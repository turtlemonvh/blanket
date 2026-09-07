package proclive

import (
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Portable behaviour: the (alive, conclusive) contract itself. These run
// everywhere -- linux in the main CI job, windows in the `windows` job.
// darwin has no runner, so its implementation is covered only by the
// cross-compile in `make docker-build` plus the shared contract below,
// which is why every failure path in that file returns the inconclusive
// answer rather than a guess.

func TestIsAlive_SelfIsAliveAndConclusive(t *testing.T) {
	pid := os.Getpid()

	startTs, ok := StartTime(pid)
	require.True(t, ok, "should be able to read this process's own start time")
	require.Greater(t, startTs, int64(0))

	alive, conclusive := IsAlive(pid, startTs)
	assert.True(t, alive)
	assert.True(t, conclusive, "a matching start time is a conclusive answer")
}

func TestIsAlive_LivePidWithoutStartTimeIsInconclusive(t *testing.T) {
	// The documented encoding for "not recorded" -- what a journal written
	// by a pre-phase-3 worker carries. Alive, but pid reuse can't be ruled
	// out, so the reaper must not act.
	alive, conclusive := IsAlive(os.Getpid(), 0)
	assert.True(t, alive)
	assert.False(t, conclusive)
}

func TestIsAlive_MismatchedStartTimeIsConclusivelyDead(t *testing.T) {
	// Same pid, a start time from long ago: the pid was recycled, so the
	// process that was recorded is gone. This is the one case where "the
	// pid exists" must still answer "dead".
	alive, conclusive := IsAlive(os.Getpid(), 1000000)
	assert.False(t, alive)
	assert.True(t, conclusive)
}

func TestIsAlive_ZeroAndNegativePidsAreInconclusive(t *testing.T) {
	for _, pid := range []int{0, -1} {
		alive, conclusive := IsAlive(pid, 12345)
		assert.False(t, alive, "pid %d", pid)
		assert.False(t, conclusive, "pid %d: an unrecorded pid is not evidence of death", pid)
	}
	if _, ok := StartTime(0); ok {
		t.Error("StartTime(0) should not report a start time")
	}
}

func TestIsAlive_ExitedChildIsConclusivelyDead(t *testing.T) {
	// A real process that really exits: the strongest evidence the reaper
	// ever gets, and the one that lets it act.
	cmd := exitingCommand()
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid

	startTs, _ := StartTime(pid)
	require.NoError(t, cmd.Wait())

	alive, conclusive := IsAlive(pid, startTs)
	assert.False(t, alive)
	assert.True(t, conclusive)
}

// exitingCommand returns a command that exits immediately, on any platform
// the tests actually run on.
func exitingCommand() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "exit", "0")
	}
	return exec.Command("sh", "-c", "exit 0")
}

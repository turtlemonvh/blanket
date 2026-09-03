//go:build !windows

package worker

import (
	"os"
	"syscall"
)

// ForceStopProcess sends SIGKILL to the worker process with the given pid,
// terminating it immediately regardless of what it's doing. This backs the
// stop-worker "force" option — a heavier hammer than the normal
// Stopped-flag handshake, which only asks the worker's poll loop to notice
// (on its next Refetch) and exit after finishing whatever task it's
// currently running.
func ForceStopProcess(pid int) error {
	if pid == 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGKILL)
}

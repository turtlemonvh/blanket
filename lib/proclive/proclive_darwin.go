//go:build darwin

package proclive

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// Darwin liveness. There is no procfs, so:
//
//   - existence comes from kill(pid, 0), the portable POSIX probe: ESRCH
//     means no such process, EPERM means it exists and belongs to somebody
//     else (which still answers the question), and success means it exists.
//   - the start time comes from the kern.proc.pid.<pid> sysctl, whose
//     kinfo_proc carries p_starttime. A syscall rather than shelling out to
//     `ps -o lstart=`: parsing a localised date out of a subprocess on every
//     reaper pass would be both slower and less reliable, and this package's
//     contract already has a safe answer for "couldn't tell".
//
// Not exercised by CI — there is no macOS runner — so it is deliberately
// small and every failure path returns the inconclusive answer rather than
// a guess. The cross-compile in `make docker-build` is what keeps it
// compiling.
func processExists(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	case errors.Is(err, syscall.EPERM):
		// Somebody else's process: it exists, we just may not signal it.
		return true, nil
	default:
		return false, err
	}
}

func startTime(pid int) (int64, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil {
		return 0, false
	}
	sec := int64(kp.Proc.P_starttime.Sec)
	if sec <= 0 {
		return 0, false
	}
	return sec, true
}

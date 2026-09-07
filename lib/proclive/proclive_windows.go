//go:build windows

package proclive

import (
	"errors"
	"syscall"
)

// Windows liveness.
//
// OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION is the modern,
// least-privileged handle that still answers both questions we have:
// GetExitCodeProcess says whether the process is still running, and
// GetProcessTimes gives its creation time — the start-time guard against pid
// reuse, which matters more on Windows than elsewhere because Windows
// recycles pids aggressively.
//
// PROCESS_QUERY_LIMITED_INFORMATION is not in the standard library's
// syscall package, hence the local constant. It is available from Vista
// onward; if it were ever refused, the error path below returns the
// inconclusive answer and the reaper falls back to heartbeat staleness.
const processQueryLimitedInformation = 0x1000

// stillActive is STILL_ACTIVE (259): the exit code GetExitCodeProcess
// reports for a process that has not exited. A process that genuinely exits
// with 259 is indistinguishable from a running one by this API — a
// documented Windows quirk. It costs nothing here: the mistake is in the
// "alive" direction, which is the safe one for every caller of this package.
const stillActive = 259

// errInvalidParameter is ERROR_INVALID_PARAMETER (87), what OpenProcess
// returns for a pid that is not a live process. The standard library's
// syscall package does not name it on windows, so it is spelled out here.
const errInvalidParameter = syscall.Errno(87)

func openForQuery(pid int) (syscall.Handle, error) {
	return syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
}

func processExists(pid int) (bool, error) {
	h, err := openForQuery(pid)
	if err != nil {
		// The pid is not a live process (ERROR_INVALID_PARAMETER is what
		// Windows returns for one that has fully gone away). Access denied
		// and friends mean "exists, but not ours to inspect" — but they are
		// indistinguishable enough at this layer that the conservative
		// reading is "we could not tell".
		if errors.Is(err, errInvalidParameter) {
			return false, nil
		}
		return false, err
	}
	defer syscall.CloseHandle(h)

	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false, err
	}
	// A handle can outlive the process it refers to, so an open handle is
	// not on its own proof of life.
	return code == stillActive, nil
}

func startTime(pid int) (int64, bool) {
	h, err := openForQuery(pid)
	if err != nil {
		return 0, false
	}
	defer syscall.CloseHandle(h)

	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, false
	}
	sec := creation.Nanoseconds() / 1e9
	if sec <= 0 {
		return 0, false
	}
	return sec, true
}

//go:build linux

package proclive

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// Linux liveness, read straight out of procfs. No cgo, no subprocess.
//
//	/proc/<pid>          exists  -> the pid is live
//	/proc/<pid>/stat     field 22 (starttime) -> ticks since boot
//	/proc/stat           "btime <unix seconds>" -> when boot was
//
// so the process's start time is btime + starttime/HZ.
//
// Parsing field 22 needs care: field 2 (comm) is the executable name in
// parentheses and may itself contain spaces and parentheses, e.g.
// "(my prog (old))". The kernel guarantees it is the *last* ')' on the
// line, so the fields after it are found by splitting from there rather
// than by splitting the whole line.

// clockTicksPerSecond is the kernel's USER_HZ, which procfs' starttime is
// expressed in. Reading it properly means sysconf(_SC_CLK_TCK), which needs
// cgo; it is 100 on every Linux port in practice and is what procfs' own
// documentation assumes, so it is hardcoded here rather than dragging cgo
// into the build.
const clockTicksPerSecond = 100

var (
	// procRoot is a var so tests can point the parser at a fake procfs.
	procRoot = "/proc"

	// btime cannot change while the machine is up and /proc/stat is a
	// moderately expensive synthetic file, so it is read once. Guarded by
	// a mutex because a worker stamping start times and the server's
	// reaper both call in.
	bootTimeMu     sync.Mutex
	cachedBootTime int64
)

func processExists(pid int) (bool, error) {
	_, err := os.Stat(procRoot + "/" + strconv.Itoa(pid))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	// Permissions or a procfs read failure: we genuinely do not know.
	return false, err
}

func startTime(pid int) (int64, bool) {
	bts, err := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	ticks, ok := parseStatStartTicks(string(bts))
	if !ok {
		return 0, false
	}
	boot, ok := bootTime()
	if !ok {
		return 0, false
	}
	return boot + ticks/clockTicksPerSecond, true
}

// parseStatStartTicks pulls field 22 (starttime, in clock ticks since boot)
// out of a /proc/<pid>/stat line.
func parseStatStartTicks(line string) (int64, bool) {
	// comm is field 2 and is parenthesised; it may contain spaces and
	// parentheses of its own, so skip to the final ')'.
	end := strings.LastIndex(line, ")")
	if end < 0 || end+2 >= len(line) {
		return 0, false
	}
	// Fields from here start at 3 (state), so starttime (22) is index 19.
	fields := strings.Fields(line[end+1:])
	const startTimeOffset = 19
	if len(fields) <= startTimeOffset {
		return 0, false
	}
	ticks, err := strconv.ParseInt(fields[startTimeOffset], 10, 64)
	if err != nil || ticks < 0 {
		return 0, false
	}
	return ticks, true
}

// bootTime reads btime from /proc/stat: the unix time the machine booted.
func bootTime() (int64, bool) {
	bootTimeMu.Lock()
	defer bootTimeMu.Unlock()

	if cachedBootTime != 0 {
		return cachedBootTime, true
	}
	bts, err := os.ReadFile(procRoot + "/stat")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(bts), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		if err != nil || v <= 0 {
			return 0, false
		}
		cachedBootTime = v
		return v, true
	}
	return 0, false
}

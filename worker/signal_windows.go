//go:build windows

package worker

import "os"

// ForceStopProcess terminates the worker process with the given pid.
// Windows has no POSIX signals — os.Process.Signal there only accepts
// os.Kill — so this calls Process.Kill directly (TerminateProcess), the
// best-effort windows equivalent of the SIGKILL sent by the unix
// implementation of ForceStopProcess.
func ForceStopProcess(pid int) error {
	if pid == 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

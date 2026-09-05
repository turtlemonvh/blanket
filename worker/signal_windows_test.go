//go:build windows

package worker

import (
	"os/exec"
	"testing"
	"time"
)

// TestForceStopProcess_KillsRunningProcess_Windows is the Windows
// counterpart to TestForceStopProcess_KillsRunningProcess in
// signal_test.go (which skips itself on windows — see the comment
// there). There's no portable "just sleep" binary on Windows, so this
// spawns `cmd /c timeout /t 30` (timeout.exe ships with every Windows
// install) and confirms ForceStopProcess (Process.Kill, i.e.
// TerminateProcess) actually terminates it.
func TestForceStopProcess_KillsRunningProcess_Windows(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "timeout", "/t", "30", "/nobreak")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test process: %v", err)
	}
	pid := cmd.Process.Pid

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := ForceStopProcess(pid); err != nil {
		t.Fatalf("ForceStopProcess returned error: %v", err)
	}

	select {
	case <-done:
		// process exited, as expected
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatal("process did not exit after ForceStopProcess")
	}
}

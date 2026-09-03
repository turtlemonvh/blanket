package worker

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestForceStopProcess_ZeroPidIsNoop(t *testing.T) {
	if err := ForceStopProcess(0); err != nil {
		t.Fatalf("expected nil error for pid 0, got %v", err)
	}
}

// TestForceStopProcess_KillsRunningProcess spawns a real, longer-lived
// child process and confirms ForceStopProcess actually terminates it —
// the behavior the stop-worker "force" option relies on.
//
// Unix-only: there's no portable "just sleep" command on Windows, and the
// Windows implementation isn't otherwise exercised by CI (its build is
// cross-compile-checked only, not run — see CLAUDE.md's cross-compile
// gotcha).
func TestForceStopProcess_KillsRunningProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exercised on unix only; see comment above")
	}

	cmd := exec.Command("sleep", "30")
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

//go:build windows

package command

import (
	"strings"
	"testing"

	"github.com/turtlemonvh/blanket/lib/service"
)

func testWindowsServiceConfig() service.Config {
	return service.Config{
		ExecPath:   `C:\Users\alice\AppData\Local\blanket\bin\blanket.exe`,
		ConfigPath: `C:\Users\alice\AppData\Local\blanket\config.json`,
		Port:       8773,
		LogPath:    `C:\Users\alice\AppData\Local\blanket\logs\blanket-service.log`,
	}
}

// TestServiceInstall_RunsSchtasksCreate exercises serviceInstall with
// runVisible stubbed so the test never actually registers a Scheduled
// Task on the machine running `go test` — only renders the schtasks
// invocation and checks its argv, mirroring
// command/service_linux_test.go's TestServiceInstall_EnablesWhenSystemctlUsable.
func TestServiceInstall_RunsSchtasksCreate(t *testing.T) {
	var calls [][]string
	origRun := runVisible
	runVisible = func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	defer func() { runVisible = origRun }()

	cfg := testWindowsServiceConfig()
	if err := serviceInstall(cfg); err != nil {
		t.Fatalf("serviceInstall returned error: %v", err)
	}

	if len(calls) != 1 {
		t.Fatalf("expected 1 schtasks call, got %d: %v", len(calls), calls)
	}
	if calls[0][0] != "schtasks" {
		t.Errorf("expected schtasks call, got %q", calls[0][0])
	}
	got := strings.Join(calls[0][1:], " ")
	want := strings.Join(service.WindowsCreateArgs(cfg), " ")
	if got != want {
		t.Errorf("expected args %q, got %q", want, got)
	}
}

// TestServiceUninstall_NoopWhenTaskMissing confirms uninstall is a no-op
// (not an error) when no Scheduled Task is registered — the common case
// on a fresh CI runner — without ever calling schtasks for real.
func TestServiceUninstall_NoopWhenTaskMissing(t *testing.T) {
	origExists := taskExists
	taskExists = func() bool { return false }
	defer func() { taskExists = origExists }()

	var calls [][]string
	origRun := runVisible
	runVisible = func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	defer func() { runVisible = origRun }()

	if err := serviceUninstall(); err != nil {
		t.Fatalf("serviceUninstall returned error: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("expected no schtasks calls when task is missing, got %v", calls)
	}
}

// TestServiceUninstall_RunsSchtasksDeleteWhenPresent exercises the branch
// where a task is registered, with both taskExists and runVisible stubbed
// so nothing real is deleted.
func TestServiceUninstall_RunsSchtasksDeleteWhenPresent(t *testing.T) {
	origExists := taskExists
	taskExists = func() bool { return true }
	defer func() { taskExists = origExists }()

	var calls [][]string
	origRun := runVisible
	runVisible = func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	defer func() { runVisible = origRun }()

	if err := serviceUninstall(); err != nil {
		t.Fatalf("serviceUninstall returned error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 schtasks call, got %d: %v", len(calls), calls)
	}
	got := strings.Join(calls[0], " ")
	want := "schtasks " + strings.Join(service.WindowsDeleteArgs(), " ")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestServiceStatus_ReportsNotInstalledWhenTaskMissing exercises the
// no-task branch of serviceStatus, again without shelling out for real.
func TestServiceStatus_ReportsNotInstalledWhenTaskMissing(t *testing.T) {
	origExists := taskExists
	taskExists = func() bool { return false }
	defer func() { taskExists = origExists }()

	if err := serviceStatus(); err != nil {
		t.Fatalf("serviceStatus returned error: %v", err)
	}
}

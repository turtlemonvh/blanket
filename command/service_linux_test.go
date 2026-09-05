//go:build linux

package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turtlemonvh/blanket/lib/service"
)

func testServiceConfig(t *testing.T, home string) service.Config {
	t.Helper()
	return service.Config{
		ExecPath:   filepath.Join(home, "bin", "blanket"),
		ConfigPath: filepath.Join(home, ".config", "blanket", "config.json"),
		Port:       8773,
		LogPath:    filepath.Join(home, ".local", "share", "blanket", "logs", "blanket-service.log"),
	}
}

// TestServiceInstall_WritesUnitFile exercises serviceInstall against a
// scratch $HOME/$XDG_CONFIG_HOME (never the real one), with
// systemctlUserUsable stubbed to false so the test is deterministic
// regardless of whether the machine running `go test` (e.g. `make
// docker-test`'s container, which has no systemd user session) has a
// usable systemd. See TestServiceInstall_EnablesWhenSystemctlUsable below
// for the branch where it's stubbed true.
func TestServiceInstall_WritesUnitFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	origUsable := systemctlUserUsable
	systemctlUserUsable = func() bool { return false }
	defer func() { systemctlUserUsable = origUsable }()

	cfg := testServiceConfig(t, home)
	if err := serviceInstall(cfg); err != nil {
		t.Fatalf("serviceInstall returned error: %v", err)
	}

	unitPath := filepath.Join(home, ".config", "systemd", "user", service.SystemdUnitName)
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("expected unit file at %s: %v", unitPath, err)
	}
	content := string(data)
	if !strings.Contains(content, "ExecStart="+cfg.ExecPath) {
		t.Errorf("expected ExecStart with %s, got:\n%s", cfg.ExecPath, content)
	}
	if !strings.Contains(content, cfg.LogPath) {
		t.Errorf("expected log path %s in unit file, got:\n%s", cfg.LogPath, content)
	}
}

// TestServiceInstall_XDGConfigHomeOverride confirms the unit lands under
// $XDG_CONFIG_HOME/systemd/user when that's set, not ~/.config — the same
// override rule command.InitializeConfig uses for the config file search.
func TestServiceInstall_XDGConfigHomeOverride(t *testing.T) {
	home := t.TempDir()
	xdgConfig := filepath.Join(home, "xdgconf")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	origUsable := systemctlUserUsable
	systemctlUserUsable = func() bool { return false }
	defer func() { systemctlUserUsable = origUsable }()

	cfg := testServiceConfig(t, home)
	if err := serviceInstall(cfg); err != nil {
		t.Fatalf("serviceInstall returned error: %v", err)
	}

	unitPath := filepath.Join(xdgConfig, "systemd", "user", service.SystemdUnitName)
	if _, err := os.Stat(unitPath); err != nil {
		t.Fatalf("expected unit file at %s: %v", unitPath, err)
	}
}

// TestServiceUninstall_RemovesUnitFile confirms uninstall deletes the
// file written by install, and is a no-op (not an error) when nothing is
// installed.
func TestServiceUninstall_RemovesUnitFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	origUsable := systemctlUserUsable
	systemctlUserUsable = func() bool { return false }
	defer func() { systemctlUserUsable = origUsable }()

	cfg := testServiceConfig(t, home)
	if err := serviceInstall(cfg); err != nil {
		t.Fatalf("serviceInstall returned error: %v", err)
	}
	unitPath := filepath.Join(home, ".config", "systemd", "user", service.SystemdUnitName)
	if _, err := os.Stat(unitPath); err != nil {
		t.Fatalf("setup: expected unit file at %s: %v", unitPath, err)
	}

	if err := serviceUninstall(); err != nil {
		t.Fatalf("serviceUninstall returned error: %v", err)
	}
	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Errorf("expected unit file to be removed, stat err: %v", err)
	}

	// Uninstalling again (nothing left to remove) must not error.
	if err := serviceUninstall(); err != nil {
		t.Fatalf("second serviceUninstall (no-op case) returned error: %v", err)
	}
}

// TestServiceInstall_EnablesWhenSystemctlUsable stubs both
// systemctlUserUsable and runVisible so the "systemd is usable" branch is
// exercised without ever shelling out to a real systemctl — the container
// `make docker-test` runs in has no systemd user session, and a real
// invocation could hang waiting for a D-Bus connection that will never
// come. See the PR description for a manual run against a real session on
// a host where systemctl --user is usable.
func TestServiceInstall_EnablesWhenSystemctlUsable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	origUsable := systemctlUserUsable
	systemctlUserUsable = func() bool { return true }
	defer func() { systemctlUserUsable = origUsable }()

	var calls [][]string
	origRun := runVisible
	runVisible = func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	defer func() { runVisible = origRun }()

	cfg := testServiceConfig(t, home)
	if err := serviceInstall(cfg); err != nil {
		t.Fatalf("serviceInstall returned error: %v", err)
	}

	unitPath := filepath.Join(home, ".config", "systemd", "user", service.SystemdUnitName)
	if _, err := os.Stat(unitPath); err != nil {
		t.Fatalf("expected unit file to be written: %v", err)
	}

	if len(calls) != 2 {
		t.Fatalf("expected 2 systemctl calls (daemon-reload, enable --now), got %d: %v", len(calls), calls)
	}
	if got := strings.Join(calls[0], " "); got != "systemctl --user daemon-reload" {
		t.Errorf("expected daemon-reload call, got %q", got)
	}
	if got := strings.Join(calls[1], " "); got != "systemctl --user enable --now "+service.SystemdUnitName {
		t.Errorf("expected enable --now call, got %q", got)
	}
}

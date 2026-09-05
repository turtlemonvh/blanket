//go:build linux

package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/turtlemonvh/blanket/lib/service"
)

// systemdUserUnitDir returns ~/.config/systemd/user (respecting
// XDG_CONFIG_HOME), the standard install location for per-user systemd
// units. Same override rule as command.InitializeConfig's config search.
func systemdUserUnitDir() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "systemd", "user"), nil
}

// systemctlUserUsable reports whether `systemctl --user` can talk to a
// running systemd user session in this environment. It's common for this
// to be false — no systemd (some containers), or systemd present but no
// user session/D-Bus bus wired up (many WSL1/WSL2 setups, most Docker
// containers) — and that's not a blanket bug, so callers treat it as a
// non-fatal "can't verify/enable automatically" case rather than an error.
//
// A package-level var (rather than a plain func) so tests can stub it out
// and exercise the graceful-degradation branch deterministically instead
// of depending on whether the machine running `go test` happens to have a
// usable systemd user session.
var systemctlUserUsable = func() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "--user", "list-units", "--no-pager").Run() == nil
}

// serviceInstall writes the systemd user unit and, if a usable systemd
// user session is available, enables and starts it. If not (no systemd,
// or no session bus — common under WSL/containers), it writes the unit
// file anyway and prints the manual command to run once a real session
// exists, per issue #59's "handle gracefully, non-fatal" requirement.
func serviceInstall(cfg service.Config) error {
	dir, err := systemdUserUnitDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	unitPath := filepath.Join(dir, service.SystemdUnitName)
	if err := os.WriteFile(unitPath, []byte(service.SystemdUnit(cfg)), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", unitPath, err)
	}
	fmt.Printf("Wrote systemd user unit: %s\n", unitPath)

	if !systemctlUserUsable() {
		fmt.Println()
		fmt.Println("systemctl --user is not usable here (no systemd, or no user session/D-Bus")
		fmt.Println("bus available — common under WSL or in containers). The unit file above was")
		fmt.Println("still written; enable it once a real user session is available with:")
		fmt.Printf("  systemctl --user daemon-reload && systemctl --user enable --now %s\n", service.SystemdUnitName)
		return nil
	}

	if err := runVisible("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	if err := runVisible("systemctl", "--user", "enable", "--now", service.SystemdUnitName); err != nil {
		return fmt.Errorf("systemctl --user enable --now: %w", err)
	}
	fmt.Println("Enabled and started blanket.service.")
	return nil
}

// serviceUninstall stops and disables the unit (best-effort — a stale or
// already-removed unit isn't an error) and deletes the unit file.
func serviceUninstall() error {
	dir, err := systemdUserUnitDir()
	if err != nil {
		return err
	}
	unitPath := filepath.Join(dir, service.SystemdUnitName)

	if _, statErr := os.Stat(unitPath); statErr != nil {
		fmt.Println("No systemd user unit installed; nothing to remove.")
		return nil
	}

	if systemctlUserUsable() {
		// Best-effort: an already-stopped or never-enabled unit returns a
		// non-zero exit here, which shouldn't block removing the file.
		_ = runVisible("systemctl", "--user", "disable", "--now", service.SystemdUnitName)
	}

	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("removing %s: %w", unitPath, err)
	}
	fmt.Printf("Removed systemd user unit: %s\n", unitPath)

	if systemctlUserUsable() {
		_ = runVisible("systemctl", "--user", "daemon-reload")
	}
	return nil
}

// serviceStatus prints whether the unit is installed and, if systemctl
// --user is usable, its live status.
func serviceStatus() error {
	dir, err := systemdUserUnitDir()
	if err != nil {
		return err
	}
	unitPath := filepath.Join(dir, service.SystemdUnitName)

	if _, statErr := os.Stat(unitPath); statErr != nil {
		fmt.Println("blanket.service is not installed.")
		return nil
	}
	fmt.Printf("Unit file: %s\n", unitPath)

	if !systemctlUserUsable() {
		fmt.Println("systemctl --user is not usable here; cannot query live status.")
		return nil
	}

	// `systemctl --user status` exits non-zero for an inactive/failed unit,
	// which is informational output here, not a command failure.
	out, _ := exec.Command("systemctl", "--user", "status", service.SystemdUnitName, "--no-pager").CombinedOutput()
	fmt.Print(string(out))
	return nil
}

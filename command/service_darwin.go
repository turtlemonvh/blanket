//go:build darwin

package command

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"

	"github.com/turtlemonvh/blanket/lib/service"
)

// launchAgentsDir returns ~/Library/LaunchAgents, the standard install
// location for a per-user launchd agent.
func launchAgentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents"), nil
}

func launchdPlistPath() (string, error) {
	dir, err := launchAgentsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, service.LaunchdLabel+".plist"), nil
}

// guiTarget returns the "gui/<uid>" domain target launchctl bootstrap/
// bootout expect for a per-user agent running in the current login
// session.
func guiTarget() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolving current user: %w", err)
	}
	return "gui/" + u.Uid, nil
}

// serviceInstall writes the LaunchAgent plist and bootstraps it into the
// current GUI session with launchctl. `launchctl bootstrap` is the
// modern (10.11+) replacement for `launchctl load`; it fails clearly
// (rather than hanging) when there's no GUI session to bootstrap into
// (e.g. an SSH-only session or a headless CI runner), which this reports
// as a non-fatal warning per issue #59 rather than an install failure —
// the plist is still written and can be bootstrapped manually once a real
// login session exists.
func serviceInstall(cfg service.Config) error {
	dir, err := launchAgentsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	plistPath, err := launchdPlistPath()
	if err != nil {
		return err
	}
	if err := os.WriteFile(plistPath, []byte(service.LaunchdPlist(cfg)), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", plistPath, err)
	}
	fmt.Printf("Wrote LaunchAgent plist: %s\n", plistPath)

	target, err := guiTarget()
	if err != nil {
		return err
	}

	if err := runVisible("launchctl", "bootstrap", target, plistPath); err != nil {
		fmt.Println()
		fmt.Println("launchctl bootstrap failed — this is expected outside a real GUI login")
		fmt.Println("session (e.g. over SSH). The plist above was still written; bootstrap it")
		fmt.Println("manually once logged in at the console with:")
		fmt.Printf("  launchctl bootstrap %s %s\n", target, plistPath)
		return nil
	}
	fmt.Println("Bootstrapped com.turtlemonvh.blanket into the current GUI session.")
	return nil
}

// serviceUninstall boots the agent out of the session (best-effort — an
// already-stopped or never-bootstrapped agent isn't an error) and removes
// the plist.
func serviceUninstall() error {
	plistPath, err := launchdPlistPath()
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(plistPath); statErr != nil {
		fmt.Println("No LaunchAgent plist installed; nothing to remove.")
		return nil
	}

	if target, err := guiTarget(); err == nil {
		_ = runVisible("launchctl", "bootout", target+"/"+service.LaunchdLabel)
	}

	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("removing %s: %w", plistPath, err)
	}
	fmt.Printf("Removed LaunchAgent plist: %s\n", plistPath)
	return nil
}

// serviceStatus prints whether the plist is installed and, if possible,
// launchctl's live status for it.
func serviceStatus() error {
	plistPath, err := launchdPlistPath()
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(plistPath); statErr != nil {
		fmt.Println("blanket LaunchAgent is not installed.")
		return nil
	}
	fmt.Printf("Plist file: %s\n", plistPath)

	target, err := guiTarget()
	if err != nil {
		return nil
	}
	// `launchctl print` exits non-zero when the service isn't loaded into
	// the target session, which is informational output here, not a
	// command failure.
	out, _ := exec.Command("launchctl", "print", target+"/"+service.LaunchdLabel).CombinedOutput()
	fmt.Print(string(out))
	return nil
}

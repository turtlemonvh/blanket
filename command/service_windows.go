//go:build windows

package command

import (
	"fmt"
	"os/exec"

	"github.com/turtlemonvh/blanket/lib/service"
)

// serviceInstall registers a Scheduled Task that starts blanket at logon
// via schtasks. /F makes this idempotent (overwrites a pre-existing task
// of the same name) so re-running `blanket service install` after moving
// the binary or changing --port just updates the registration.
func serviceInstall(cfg service.Config) error {
	if err := runVisible("schtasks", service.WindowsCreateArgs(cfg)...); err != nil {
		return fmt.Errorf("schtasks /Create: %w", err)
	}
	fmt.Printf("Registered Scheduled Task %q to run blanket at logon.\n", service.WindowsTaskName)
	return nil
}

// serviceUninstall removes the Scheduled Task. schtasks /Delete exits
// non-zero (ERROR: The system cannot find the file specified) when the
// task doesn't exist, which is treated as "nothing to remove" rather than
// a hard failure so `blanket uninstall` stays idempotent.
func serviceUninstall() error {
	if !taskExists() {
		fmt.Printf("No Scheduled Task named %q installed; nothing to remove.\n", service.WindowsTaskName)
		return nil
	}
	if err := runVisible("schtasks", service.WindowsDeleteArgs()...); err != nil {
		return fmt.Errorf("schtasks /Delete: %w", err)
	}
	fmt.Printf("Removed Scheduled Task %q.\n", service.WindowsTaskName)
	return nil
}

// serviceStatus prints schtasks' own status output for the task, if any.
func serviceStatus() error {
	if !taskExists() {
		fmt.Printf("Scheduled Task %q is not installed.\n", service.WindowsTaskName)
		return nil
	}
	out, err := exec.Command("schtasks", service.WindowsQueryArgs()...).CombinedOutput()
	fmt.Print(string(out))
	return err
}

func taskExists() bool {
	return exec.Command("schtasks", service.WindowsQueryArgs()...).Run() == nil
}

package command

import (
	"fmt"

	"github.com/kardianos/osext"
	"github.com/spf13/cobra"
	"github.com/turtlemonvh/blanket/lib/service"
)

// uninstallCmd is intentionally conservative (issue #59): it only removes
// the autostart service entry created by `blanket service install`
// (systemd user unit / LaunchAgent / Scheduled Task) — never the binary,
// config, or task/result data, since those are harder to reverse and a
// user re-running the install script expects their task history and task
// type library to still be there. It prints where those other things
// live so a user who does want a full removal knows exactly what to
// delete by hand.
var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove blanket's autostart service entry",
	Long: `Removes the background service entry registered by "blanket service
install" (systemd user unit on Linux, LaunchAgent on macOS, Scheduled
Task on Windows), so blanket no longer starts automatically on login or
boot.

This does NOT remove the blanket binary, its config file, or its task/
result data — those are left in place untouched. Delete them yourself
(the paths are printed below) if you want a full removal.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := serviceUninstall(); err != nil {
			return err
		}
		printWhatWasLeft()
		return nil
	},
}

func init() {
	RootCmd.AddCommand(uninstallCmd)
}

func printWhatWasLeft() {
	fmt.Println()
	fmt.Println("blanket itself was left in place. Remove these manually for a full uninstall:")

	if execPath, err := osext.Executable(); err == nil {
		fmt.Printf("  binary:  %s\n", execPath)
	} else if installDir, dirErr := service.DefaultInstallDir(); dirErr == nil {
		fmt.Printf("  binary:  (could not resolve the running binary's path; the installer's default is %s)\n", installDir)
	}

	if configDir, err := service.DefaultConfigDir(); err == nil {
		fmt.Printf("  config:  %s\n", configDir)
	}

	if dataDir, err := service.DefaultDataDir(); err == nil {
		fmt.Printf("  data:    %s\n", dataDir)
	}
}

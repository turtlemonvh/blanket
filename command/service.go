package command

/*
Registers blanket as a background service that starts on login/boot
(issue #59): a systemd user unit on Linux, a launchd LaunchAgent on
macOS, a Scheduled Task on Windows. The three actual implementations
(serviceInstall/serviceUninstall/serviceStatus) live in
service_linux.go/service_darwin.go/service_windows.go, one file per
platform tagged with //go:build, matching the pattern in
worker/daemon_unix.go and worker/daemon_windows.go — this file only
builds the shared Config and wires up the cobra commands, so it compiles
(and its serviceInstall/etc. calls resolve) on every target OS.

The service always runs plain `blanket` (the root command — the server;
see command/serve.go), not `blanket worker`: the README's quick start
runs the server as one long-lived process and workers as separate,
explicitly-tagged processes started per host capability (`blanket worker
-t exec:bash,os:unix`), so there's no single "the worker" to autostart
here. Autostarting a worker would also require guessing which tags this
host should advertise, which install.sh/install.ps1 have no way to know.
*/

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kardianos/osext"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/service"
)

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage blanket as a background service (start on login/boot)",
	Long: `Registers (or removes) blanket as a background service that starts
automatically on login/boot: a systemd user unit on Linux, a launchd
LaunchAgent on macOS, or a Scheduled Task on Windows.

The service runs plain "blanket" (the server) using the --config and
--port this command itself resolves, not "blanket worker" — see "blanket
worker --help" to run a worker separately, since workers need
host-specific capability tags that can't be guessed automatically.`,
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Register blanket to start automatically on login/boot",
	RunE: func(cmd *cobra.Command, args []string) error {
		InitializeConfig()
		cfg, err := buildServiceConfig()
		if err != nil {
			return err
		}
		return serviceInstall(cfg)
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove blanket's autostart service entry",
	RunE: func(cmd *cobra.Command, args []string) error {
		return serviceUninstall()
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether blanket's autostart service entry is installed and running",
	RunE: func(cmd *cobra.Command, args []string) error {
		return serviceStatus()
	},
}

func init() {
	serviceCmd.AddCommand(serviceInstallCmd, serviceUninstallCmd, serviceStatusCmd)
	RootCmd.AddCommand(serviceCmd)
}

// buildServiceConfig resolves the paths the service definition needs:
// the exact binary this command was invoked as, the config file/port this
// process resolved (so the service runs against the same config the
// operator had active when they ran `blanket service install` — mirrors
// worker.buildDaemonCmd's forwarding rule from issue #45), and a log file
// under blanket's XDG data dir.
func buildServiceConfig() (service.Config, error) {
	execPath, err := osext.Executable()
	if err != nil {
		return service.Config{}, fmt.Errorf("resolving blanket executable path: %w", err)
	}

	dataDir, err := service.DefaultDataDir()
	if err != nil {
		return service.Config{}, fmt.Errorf("resolving data directory for service log: %w", err)
	}
	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return service.Config{}, fmt.Errorf("creating %s: %w", logDir, err)
	}

	// Resolve the config path to an absolute one: systemd/launchd/schtasks
	// run the service with their own working directory (not the one
	// `blanket service install` happened to be run from), so a relative
	// -c/--config value here would silently fail to open once the service
	// actually starts. Caught by hand while exercising this against a real
	// systemd --user session — see the PR description's manual test notes.
	configPath := viper.ConfigFileUsed()
	if configPath != "" {
		absConfigPath, err := filepath.Abs(configPath)
		if err != nil {
			return service.Config{}, fmt.Errorf("resolving absolute path for config file %q: %w", configPath, err)
		}
		configPath = absConfigPath
	}

	return service.Config{
		ExecPath:   execPath,
		ConfigPath: configPath,
		Port:       viper.GetInt("port"),
		LogPath:    filepath.Join(logDir, "blanket-service.log"),
	}, nil
}

// runVisible runs an external command (systemctl / launchctl / schtasks),
// echoing it first so `blanket service install/uninstall` output makes it
// obvious what the CLI is doing under the hood.
//
// A package-level var (rather than a plain func) so platform tests can
// substitute a fake that records the call instead of actually shelling
// out — important on Linux, where the real systemctl/launchctl/schtasks
// binary may not exist (or may hang waiting for a D-Bus session that
// isn't there) in the container `make docker-test` runs in.
var runVisible = func(name string, args ...string) error {
	fmt.Printf("  $ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

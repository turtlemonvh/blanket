// Package service renders the platform-specific background-service
// definitions blanket registers on install (issue #59): a systemd user
// unit on Linux, a launchd LaunchAgent plist on macOS, and a Scheduled
// Task on Windows. Rendering is kept OS-agnostic and dependency-free so it
// can be unit-tested on any host regardless of which OS it targets — the
// per-platform commands that actually shell out to systemctl / launchctl /
// schtasks live in command/service_<os>.go, tagged with //go:build per the
// repo convention (see CLAUDE.md "Platform-specific code uses //go:build
// tags, not runtime switches").
package service

import (
	"fmt"
	"strconv"
	"strings"
)

// Config carries everything needed to render any of the three service
// definitions. Not every field is used by every renderer (e.g. the
// Windows task has no persistent log-redirection flag the way a systemd
// unit or launchd plist does), but keeping one shared struct means the
// three renderers stay easy to compare and the caller only builds it once.
type Config struct {
	// ExecPath is the absolute path to the blanket binary to run.
	ExecPath string
	// ConfigPath is the resolved config file (viper.ConfigFileUsed()) the
	// service should run against. May be empty, in which case the
	// generated command omits --config and blanket falls back to its own
	// default config search (see command.InitializeConfig).
	ConfigPath string
	// Port is always forwarded as --port: it has a sensible default even
	// when ConfigPath is empty. Mirrors worker.buildDaemonCmd's forwarding
	// rule (see issue #45) so the service runs against the exact
	// port/config the operator resolved when they ran `blanket service
	// install`, not whatever a bare `blanket` invocation would default to.
	Port int
	// LogPath is where the service's stdout/stderr are redirected.
	LogPath string
}

// commandArgs returns the "blanket [--config P] --port N" argument list
// (ExecPath excluded), shared by all three renderers so the forwarding
// rule lives in exactly one place.
func (c Config) commandArgs() []string {
	args := []string{}
	if c.ConfigPath != "" {
		args = append(args, "--config", c.ConfigPath)
	}
	args = append(args, "--port", strconv.Itoa(c.Port))
	return args
}

// CommandLine returns the full shell-quoted command line (ExecPath plus
// commandArgs), used by the launchd/schtasks renderers that need a single
// string rather than an argv array.
func (c Config) CommandLine() string {
	parts := append([]string{quoteIfNeeded(c.ExecPath)}, quoteArgs(c.commandArgs())...)
	return strings.Join(parts, " ")
}

func quoteIfNeeded(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

// SystemdUnitName is the unit file name installed under
// ~/.config/systemd/user/.
const SystemdUnitName = "blanket.service"

// SystemdUnit renders the systemd user unit that runs `blanket [--config
// ...] --port ...` as a simple, auto-restarting service.
func SystemdUnit(c Config) string {
	return fmt.Sprintf(`[Unit]
Description=Blanket task server
After=network.target

[Service]
Type=simple
ExecStart=%s %s
Restart=on-failure
RestartSec=5
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, quoteIfNeeded(c.ExecPath), strings.Join(quoteArgs(c.commandArgs()), " "), c.LogPath, c.LogPath)
}

func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = quoteIfNeeded(a)
	}
	return out
}

// LaunchdLabel is the plist Label / filename stem installed under
// ~/Library/LaunchAgents/.
const LaunchdLabel = "com.turtlemonvh.blanket"

// LaunchdPlist renders the macOS LaunchAgent plist that runs blanket at
// login and restarts it if it exits unexpectedly (KeepAlive).
func LaunchdPlist(c Config) string {
	var argv strings.Builder
	argv.WriteString("\t\t<string>" + xmlEscape(c.ExecPath) + "</string>\n")
	for _, a := range c.commandArgs() {
		argv.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, LaunchdLabel, argv.String(), xmlEscape(c.LogPath), xmlEscape(c.LogPath))
}

func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

// WindowsTaskName is the Scheduled Task name used for /TN on schtasks.
const WindowsTaskName = "Blanket"

// WindowsCreateArgs returns the argv (excluding the "schtasks" program
// name itself) for registering the Scheduled Task that starts blanket at
// logon. /RL LIMITED runs it with standard (non-elevated) rights, matching
// a normal interactive login session; /F overwrites a pre-existing task of
// the same name so re-running `blanket service install` is idempotent.
func WindowsCreateArgs(c Config) []string {
	return []string{
		"/Create",
		"/TN", WindowsTaskName,
		"/TR", c.CommandLine(),
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/F",
	}
}

// WindowsDeleteArgs returns the argv for removing the Scheduled Task.
func WindowsDeleteArgs() []string {
	return []string{"/Delete", "/TN", WindowsTaskName, "/F"}
}

// WindowsQueryArgs returns the argv for querying the Scheduled Task's
// current status.
func WindowsQueryArgs() []string {
	return []string{"/Query", "/TN", WindowsTaskName, "/V", "/FO", "LIST"}
}

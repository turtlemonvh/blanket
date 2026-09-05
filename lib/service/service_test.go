package service

import (
	"strings"
	"testing"
)

func testConfig() Config {
	return Config{
		ExecPath:   "/home/alice/.local/bin/blanket",
		ConfigPath: "/home/alice/.config/blanket/config.json",
		Port:       8773,
		LogPath:    "/home/alice/.local/share/blanket/logs/blanket-service.log",
	}
}

func TestSystemdUnit_ContainsExecStartAndRestartPolicy(t *testing.T) {
	unit := SystemdUnit(testConfig())

	wantSubstrings := []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"ExecStart=/home/alice/.local/bin/blanket --config /home/alice/.config/blanket/config.json --port 8773",
		"Restart=on-failure",
		"StandardOutput=append:/home/alice/.local/share/blanket/logs/blanket-service.log",
		"StandardError=append:/home/alice/.local/share/blanket/logs/blanket-service.log",
		"WantedBy=default.target",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(unit, want) {
			t.Errorf("expected systemd unit to contain %q, got:\n%s", want, unit)
		}
	}
}

func TestSystemdUnit_OmitsConfigFlagWhenUnset(t *testing.T) {
	cfg := testConfig()
	cfg.ConfigPath = ""
	unit := SystemdUnit(cfg)

	if strings.Contains(unit, "--config") {
		t.Errorf("expected no --config flag when ConfigPath is empty, got:\n%s", unit)
	}
	if !strings.Contains(unit, "ExecStart=/home/alice/.local/bin/blanket --port 8773") {
		t.Errorf("expected ExecStart with only --port, got:\n%s", unit)
	}
}

func TestSystemdUnit_QuotesPathsWithSpaces(t *testing.T) {
	cfg := testConfig()
	cfg.ExecPath = "/home/alice/My Apps/blanket"
	unit := SystemdUnit(cfg)

	if !strings.Contains(unit, `ExecStart="/home/alice/My Apps/blanket"`) {
		t.Errorf("expected quoted ExecStart path, got:\n%s", unit)
	}
}

func TestLaunchdPlist_ContainsLabelAndProgramArguments(t *testing.T) {
	plist := LaunchdPlist(testConfig())

	wantSubstrings := []string{
		"<key>Label</key>",
		"<string>com.turtlemonvh.blanket</string>",
		"<key>ProgramArguments</key>",
		"<string>/home/alice/.local/bin/blanket</string>",
		"<string>--config</string>",
		"<string>/home/alice/.config/blanket/config.json</string>",
		"<string>--port</string>",
		"<string>8773</string>",
		"<key>RunAtLoad</key>",
		"<true/>",
		"<key>StandardOutPath</key>",
		"<string>/home/alice/.local/share/blanket/logs/blanket-service.log</string>",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(plist, want) {
			t.Errorf("expected plist to contain %q, got:\n%s", want, plist)
		}
	}
}

func TestLaunchdPlist_EscapesXMLSpecialCharacters(t *testing.T) {
	cfg := testConfig()
	cfg.ExecPath = `/home/alice/blanket "beta" & co`
	plist := LaunchdPlist(cfg)

	if !strings.Contains(plist, "&amp;") || !strings.Contains(plist, "&quot;") {
		t.Errorf("expected XML-escaped special characters, got:\n%s", plist)
	}
	if strings.Contains(plist, `"beta"`) {
		t.Errorf("expected raw quote characters to be escaped, got:\n%s", plist)
	}
}

func TestWindowsCreateArgs_ContainsExpectedFlags(t *testing.T) {
	args := WindowsCreateArgs(testConfig())

	want := []string{
		"/Create",
		"/TN", "Blanket",
		"/TR", `/home/alice/.local/bin/blanket --config /home/alice/.config/blanket/config.json --port 8773`,
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/F",
	}
	if len(args) != len(want) {
		t.Fatalf("expected %d args, got %d: %v", len(want), len(args), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg %d: expected %q, got %q (full: %v)", i, want[i], args[i], args)
		}
	}
}

func TestWindowsDeleteArgs(t *testing.T) {
	args := WindowsDeleteArgs()
	want := []string{"/Delete", "/TN", "Blanket", "/F"}
	if len(args) != len(want) {
		t.Fatalf("expected %v, got %v", want, args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg %d: expected %q, got %q", i, want[i], args[i])
		}
	}
}

func TestWindowsQueryArgs(t *testing.T) {
	args := WindowsQueryArgs()
	want := []string{"/Query", "/TN", "Blanket", "/V", "/FO", "LIST"}
	if len(args) != len(want) {
		t.Fatalf("expected %v, got %v", want, args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg %d: expected %q, got %q", i, want[i], args[i])
		}
	}
}

func TestCommandLine_QuotesExecPathWithSpaces(t *testing.T) {
	cfg := testConfig()
	cfg.ExecPath = "/home/alice/My Apps/blanket"
	got := cfg.CommandLine()
	want := `"/home/alice/My Apps/blanket" --config /home/alice/.config/blanket/config.json --port 8773`
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultDataDir mirrors the data-directory resolution the install
// scripts use (scripts/install.sh / scripts/install.ps1): XDG_DATA_HOME
// (or ~/.local/share) plus "blanket" on Linux/macOS, %LOCALAPPDATA%\blanket
// on Windows. The service log file lives here (under a "logs"
// subdirectory) so it sits next to the rest of blanket's on-disk state
// rather than in a separate, easy-to-forget location.
//
// This branches on runtime.GOOS rather than living in a //go:build-tagged
// file: it's the same pattern already used by
// command.InitializeConfig for config-path resolution (see root.go) —
// picking a search path by OS, not doing OS-specific syscalls — so it's
// fine to keep in one file compiled everywhere.
func DefaultDataDir() (string, error) {
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			return "", fmt.Errorf("%%LOCALAPPDATA%% is not set")
		}
		return filepath.Join(localAppData, "blanket"), nil
	}

	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "blanket"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "blanket"), nil
}

// DefaultConfigDir mirrors the config-directory resolution used by the
// install scripts and command.InitializeConfig: XDG_CONFIG_HOME (or
// ~/.config) plus "blanket" on Linux/macOS, %LOCALAPPDATA%\blanket on
// Windows (blanket's Windows install keeps config and data in the same
// directory — see scripts/install.ps1). Used only for the informational
// "here's what uninstall did not remove" message; not authoritative for
// where blanket itself looks for a config file (that's
// command.InitializeConfig's job, and it searches several candidates).
func DefaultConfigDir() (string, error) {
	if runtime.GOOS == "windows" {
		return DefaultDataDir()
	}

	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(configHome, "blanket"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".config", "blanket"), nil
}

// DefaultInstallDir mirrors the binary-install-directory default used by
// scripts/install.sh (~/.local/bin) and scripts/install.ps1
// (%LOCALAPPDATA%\blanket\bin). Like DefaultConfigDir, this is only used
// for informational display (blanket uninstall's "here's what's left"
// message) — the actual running binary's path comes from
// osext.Executable(), not this guess.
func DefaultInstallDir() (string, error) {
	if runtime.GOOS == "windows" {
		dataDir, err := DefaultDataDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dataDir, "bin"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".local", "bin"), nil
}

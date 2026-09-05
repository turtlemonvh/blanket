package service

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestDefaultDataDir_XDGOverride and its config/install-dir siblings pin
// down the non-Windows branch against scripts/install.sh's directory
// resolution (XDG_*_HOME override, else ~/... default) — see that
// script's CONFIG_DIR/DATA_DIR/INSTALL_DIR computation. Skipped on
// Windows, where these functions take the %LOCALAPPDATA% branch instead
// (see TestDefaultDataDir_WindowsUsesLocalAppData below).
func TestDefaultDataDir_XDGOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG_DATA_HOME branch is non-windows only")
	}
	t.Setenv("XDG_DATA_HOME", "/scratch/xdgdata")

	got, err := DefaultDataDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/scratch/xdgdata", "blanket")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestDefaultDataDir_FallsBackToHomeLocalShare(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-windows fallback only")
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/scratch/home")

	got, err := DefaultDataDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/scratch/home", ".local", "share", "blanket")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestDefaultConfigDir_XDGOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG_CONFIG_HOME branch is non-windows only")
	}
	t.Setenv("XDG_CONFIG_HOME", "/scratch/xdgconfig")

	got, err := DefaultConfigDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/scratch/xdgconfig", "blanket")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestDefaultInstallDir_FallsBackToHomeLocalBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-windows fallback only")
	}
	t.Setenv("HOME", "/scratch/home")

	got, err := DefaultInstallDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/scratch/home", ".local", "bin")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestDefaultDataDir_WindowsUsesLocalAppData(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only branch")
	}
	t.Setenv("LOCALAPPDATA", `C:\Users\alice\AppData\Local`)

	got, err := DefaultDataDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(`C:\Users\alice\AppData\Local`, "blanket")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

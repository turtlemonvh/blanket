package command

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// TestBuildServiceConfig_ResolvesRelativeConfigPathToAbsolute is the
// regression test for a bug caught while manually exercising `blanket
// service install` against a real systemd --user session (see the PR
// description): passing a relative -c/--config value resolved a relative
// ConfigPath, which systemd then failed to open because it runs the unit
// with its own working directory, not the one `blanket service install`
// happened to be invoked from. ConfigPath must always be absolute.
func TestBuildServiceConfig_ResolvesRelativeConfigPathToAbsolute(t *testing.T) {
	origConfigFile := viper.ConfigFileUsed()
	origPort := viper.Get("port")
	defer func() {
		viper.SetConfigFile(origConfigFile)
		viper.Set("port", origPort)
	}()

	t.Setenv("XDG_DATA_HOME", t.TempDir())
	viper.SetConfigFile("relative/config.json")
	viper.Set("port", 8888)

	cfg, err := buildServiceConfig()
	if err != nil {
		t.Fatalf("buildServiceConfig returned error: %v", err)
	}

	if !filepath.IsAbs(cfg.ConfigPath) {
		t.Errorf("expected absolute ConfigPath, got %q", cfg.ConfigPath)
	}
	if !strings.HasSuffix(cfg.ConfigPath, filepath.Join("relative", "config.json")) {
		t.Errorf("expected ConfigPath to end with relative/config.json, got %q", cfg.ConfigPath)
	}
	if cfg.Port != 8888 {
		t.Errorf("expected port 8888, got %d", cfg.Port)
	}
}

// TestBuildServiceConfig_OmitsConfigPathWhenUnset mirrors
// worker.TestBuildDaemonCmd_OmitsConfigWhenUnset: when no config file was
// resolved, ConfigPath should stay empty rather than becoming an absolute
// path to a nonexistent "" (e.g. the current directory).
func TestBuildServiceConfig_OmitsConfigPathWhenUnset(t *testing.T) {
	origConfigFile := viper.ConfigFileUsed()
	origPort := viper.Get("port")
	defer func() {
		viper.SetConfigFile(origConfigFile)
		viper.Set("port", origPort)
	}()

	t.Setenv("XDG_DATA_HOME", t.TempDir())
	viper.Reset()

	cfg, err := buildServiceConfig()
	if err != nil {
		t.Fatalf("buildServiceConfig returned error: %v", err)
	}
	if cfg.ConfigPath != "" {
		t.Errorf("expected empty ConfigPath, got %q", cfg.ConfigPath)
	}
}

// Internal-package tests for the daemon-relaunch command builder — see
// buildDaemonCmd in worker.go. Kept separate from worker_test.go (an
// external test package) because this exercises an unexported method.
package worker

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/objectid"
)

// TestBuildDaemonCmd_ForwardsConfigAndPort is the regression test for
// issue #45: the daemonized child previously fell back to viper's default
// config resolution instead of the exact config/port the parent process
// resolved, so it silently talked to the wrong server whenever the parent
// ran on a non-default -c/-p.
func TestBuildDaemonCmd_ForwardsConfigAndPort(t *testing.T) {
	origConfigFile := viper.ConfigFileUsed()
	origPort := viper.Get("port")
	defer func() {
		viper.SetConfigFile(origConfigFile)
		viper.Set("port", origPort)
	}()

	viper.SetConfigFile("/tmp/blanket-test-config.json")
	viper.Set("port", 8799)

	c := &WorkerConf{Tags: []string{"exec:bash"}}
	cmd := c.buildDaemonCmd("/usr/local/bin/blanket")

	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "--config /tmp/blanket-test-config.json") {
		t.Errorf("expected --config /tmp/blanket-test-config.json in args, got: %v", cmd.Args)
	}
	if !strings.Contains(args, "--port 8799") {
		t.Errorf("expected --port 8799 in args, got: %v", cmd.Args)
	}
}

// TestBuildDaemonCmd_OmitsConfigWhenUnset covers the (unusual, but
// possible) case where the parent process resolved no config file at
// all — the flag should be omitted rather than forwarded as an empty
// string, which would make the child fail to parse its own flags.
//
// viper.SetConfigFile("") is a no-op in viper (it only assigns when the
// argument is non-empty), so the only way to force ConfigFileUsed() back
// to "" is viper.Reset() — safe here because every other test in this
// package sets the viper keys it needs directly rather than relying on
// ambient state (see worker_test.go).
func TestBuildDaemonCmd_OmitsConfigWhenUnset(t *testing.T) {
	origConfigFile := viper.ConfigFileUsed()
	origPort := viper.Get("port")
	defer func() {
		viper.SetConfigFile(origConfigFile)
		viper.Set("port", origPort)
	}()

	viper.Reset()

	c := &WorkerConf{Tags: []string{"exec:bash"}}
	cmd := c.buildDaemonCmd("/usr/local/bin/blanket")

	for i, a := range cmd.Args {
		if a == "--config" {
			t.Errorf("expected no --config flag when no config file was resolved, got args: %v (found at index %d)", cmd.Args, i)
		}
	}
}

// TestBuildDaemonCmd_PreservesExistingFlags confirms the pre-existing
// --tags/--id/--logfile/--checkinterval forwarding still works after the
// extraction from Run()'s inline cmd-building code.
func TestBuildDaemonCmd_PreservesExistingFlags(t *testing.T) {
	origConfigFile := viper.ConfigFileUsed()
	defer viper.SetConfigFile(origConfigFile)
	viper.SetConfigFile("/tmp/blanket-test-config.json")

	id := objectid.NewObjectId()
	c := &WorkerConf{
		Tags:          []string{"exec:bash", "os:unix"},
		Id:            id,
		Logfile:       "/tmp/worker.log",
		CheckInterval: 3.5,
	}
	cmd := c.buildDaemonCmd("/usr/local/bin/blanket")

	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"--tags exec:bash,os:unix",
		"--id " + id.Hex(),
		"--logfile /tmp/worker.log",
		"--checkinterval 3.500000",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("expected %q in args, got: %v", want, cmd.Args)
		}
	}
}

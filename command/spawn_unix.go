//go:build !windows

package command

import (
	"os/exec"
	"syscall"
)

// setSpawnAttrs puts a server started by `blanket upgrade` or `blanket
// rollback` into its own process group, so it outlives the CLI that
// started it.
//
// This is the same trick worker.setDaemonAttrs plays, and it is needed
// here for the same reason plus one more: the CLI only ever starts the
// server when the old one exited for a supervisor that turned out not to
// exist (or on windows, which never self-restarts). In that situation the
// operator's terminal is the only parent around, and a server that died
// with the shell would make the upgrade look like it had worked until the
// next time they closed a window.
func setSpawnAttrs(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

//go:build !windows

package worker

import (
	"os/exec"
	"syscall"
)

// setDaemonAttrs puts the spawned worker in its own process group so it
// survives the parent shell. Unix-only; the windows build provides a
// no-op stub.
func setDaemonAttrs(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

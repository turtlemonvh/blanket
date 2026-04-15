//go:build windows

package worker

import "os/exec"

// setDaemonAttrs is a no-op on windows: syscall.SysProcAttr has no
// Setpgid field, and windows has no process-group concept — a detached
// child already survives the parent shell.
func setDaemonAttrs(cmd *exec.Cmd) {}

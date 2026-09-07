//go:build windows

package command

import "os/exec"

// setSpawnAttrs is a no-op on windows: syscall.SysProcAttr has no Setpgid
// field, and a detached child already survives its parent shell.
func setSpawnAttrs(cmd *exec.Cmd) {}

//go:build !windows

package server

/*

Unix half of the self-restart (turtlemonvh/blanket#23 phase 2).

SIGUSR2 re-execs the server in place: syscall.Exec replaces the process
image while keeping the PID and file descriptors 0/1/2, so a server started
with `nohup blanket &` or inside tmux keeps its terminal, its logs, and the
PID anything else recorded. This is decision row 2's "unsupervised" mode.

Windows has no equivalent and never self-restarts -- see restart_windows.go
for why (a detached grandchild escapes a service's job object and holds the
bolt lock invisibly).

*/

import (
	"os"
	"syscall"

	log "github.com/sirupsen/logrus"
)

// restartSignal is the signal that means "restart in place".
var restartSignal os.Signal = syscall.SIGUSR2

// reexecSelf replaces this process image with a fresh copy of the binary on
// disk, same argv, same environment. It only returns on failure.
//
// Callers must have finished the whole teardown first -- in particular the
// BoltDB handle must be closed, or the new image fails to acquire the lock
// against a process that no longer exists to release it.
func reexecSelf() error {
	// os.Executable resolves the path the binary was started from; note it
	// strips a " (deleted)" suffix, so after an in-place binary swap this
	// deliberately picks up the *new* file. That is the point here.
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"executable": exe,
		"pid":        os.Getpid(),
	}).Warn("re-executing server in place")

	return syscall.Exec(exe, os.Args, os.Environ())
}

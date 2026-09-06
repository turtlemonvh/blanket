//go:build windows

package server

/*

Windows half of the self-restart (turtlemonvh/blanket#23 phase 2): there
isn't one.

There is no SIGUSR2 on Windows, and re-exec-in-place has no equivalent
either. Spawning a detached replacement is worse than doing nothing: the
grandchild escapes a Windows service's job object, so `sc stop blanket`
can't reach it, and it keeps holding the BoltDB lock invisibly -- the worst
failure mode in the system. So the server never restarts itself here; the
upgrade CLI (phase 6), which is the user's own foreground process, starts
the server or prints `sc start blanket`.

*/

import (
	"errors"
	"os"

	log "github.com/sirupsen/logrus"
)

// restartSignal is nil on Windows: nothing subscribes a restart signal, so
// isRestartSignal is always false and reexecSelf is unreachable in normal
// operation. It stays defined so the lifecycle code is platform-agnostic.
var restartSignal os.Signal = nil

// ErrRestartUnsupported is returned by reexecSelf on platforms that never
// self-restart.
var ErrRestartUnsupported = errors.New("self-restart is not supported on windows")

func reexecSelf() error {
	log.Warn("self-restart is not supported on windows; start the server again with `sc start blanket` or `blanket serve`")
	return ErrRestartUnsupported
}

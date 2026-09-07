//go:build !linux && !darwin && !windows

package proclive

import "errors"

// Every other platform: no implementation, and therefore no answer.
//
// This is the whole reason IsAlive returns a "conclusive" flag. On a
// platform blanket has never been built for, the reaper degrades to
// heartbeat staleness alone and never acts on pid liveness — which is the
// safe direction, and needs no special-casing at the call sites.
var errUnsupportedPlatform = errors.New("proclive: pid liveness is not implemented on this platform")

func processExists(pid int) (bool, error) { return false, errUnsupportedPlatform }

func startTime(pid int) (int64, bool) { return 0, false }

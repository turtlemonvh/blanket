// Package timing is the single place the `timeMultiplier` config knob is
// applied.
//
// `timeMultiplier` exists so tests can compress every wall-clock duration
// in the system by a constant factor (a multiplier below 1 makes things
// faster; above 1, slower). Historically it was applied ad hoc — some
// durations routed through it, some didn't — which makes a compressed test
// run behave differently from production in ways that are hard to reason
// about. See turtlemonvh/blanket#23.
//
// Every new duration should be expressed as an unscaled constant and run
// through Scale (or ScaleSeconds) at the point of use, so a single
// multiplier change moves all of them together. Pre-existing call sites are
// left alone except where phase 1 already touches them.
package timing

import (
	"time"

	"github.com/spf13/viper"
)

// DefaultMultiplier is used when `timeMultiplier` is unset or nonsensical
// (zero or negative), which would otherwise collapse every duration to 0
// and turn timers into hot loops.
const DefaultMultiplier = 1.0

// Multiplier returns the effective time multiplier.
func Multiplier() float64 {
	m := viper.GetFloat64("timeMultiplier")
	if m <= 0 {
		return DefaultMultiplier
	}
	return m
}

// Scale converts an unscaled duration constant into the duration that
// should actually be used at runtime.
func Scale(d time.Duration) time.Duration {
	return time.Duration(float64(d) * Multiplier())
}

// ScaleSeconds is Scale for a duration expressed as a (possibly
// fractional) number of seconds — the shape most of blanket's config
// values take, e.g. a worker's CheckInterval.
func ScaleSeconds(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second) * Multiplier())
}

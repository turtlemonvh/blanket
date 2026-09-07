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
	"math"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"
)

// DefaultMultiplier is used when `timeMultiplier` is unset or nonsensical
// (zero or negative), which would otherwise collapse every duration to 0
// and turn timers into hot loops.
const DefaultMultiplier = 1.0

// multiplierBits holds the current multiplier as the bit pattern of a
// float64 (atomic.Value would work too, but a fixed-width atomic avoids
// the boxing/interface-type-mismatch footguns that come with storing
// arbitrary values). Scale/ScaleSeconds/Multiplier are called from every
// scheduler, worker-poll, and stream-idle goroutine in the system, so
// this needs to be a plain atomic load, not a viper.GetFloat64 -- viper's
// global instance is not safe for concurrent Set/Get, and tests calling
// viper.Set concurrently with a leftover server's background goroutines
// is exactly what turtlemonvh/blanket#128 is about.
var multiplierBits atomic.Uint64

func init() {
	multiplierBits.Store(math.Float64bits(DefaultMultiplier))
}

// SetMultiplier sets the effective time multiplier directly, bypassing
// viper entirely. This is the path tests should use instead of
// viper.Set("timeMultiplier", ...): it's race-free against concurrent
// Multiplier()/Scale() calls from other goroutines, unlike viper's global
// Set/Get pair. A value <= 0 falls back to DefaultMultiplier, matching
// Multiplier()'s historical behavior for an unset or nonsensical config
// value.
func SetMultiplier(f float64) {
	if f <= 0 {
		f = DefaultMultiplier
	}
	multiplierBits.Store(math.Float64bits(f))
}

// LoadFromConfig reads the `timeMultiplier` config key via viper and
// stores it as the effective multiplier. Call this once, at startup,
// after viper has read config files/env/flags (see command/root.go's
// InitializeConfig) -- everywhere else, including every hot path in this
// package, reads the atomic value instead of touching viper.
func LoadFromConfig() {
	SetMultiplier(viper.GetFloat64("timeMultiplier"))
}

// Multiplier returns the effective time multiplier.
func Multiplier() float64 {
	return math.Float64frombits(multiplierBits.Load())
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

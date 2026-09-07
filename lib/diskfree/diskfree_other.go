//go:build !linux && !darwin && !windows

package diskfree

// No implementation, and therefore no answer. See the package comment for
// why that is reported rather than guessed at.
func available(path string) (uint64, error) { return 0, ErrUnsupported }

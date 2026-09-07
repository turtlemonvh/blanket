//go:build !windows

package upgrade

// SweepDisplaced is a no-op off Windows: the unix swap is one rename and
// displaces nothing. It exists so command/upgrade.go can call it
// unconditionally instead of carrying a runtime.GOOS branch, which is the
// pattern CONTRIBUTORS.md asks for (build tags, not runtime switches).
func SweepDisplaced(targetPath string) []string { return nil }

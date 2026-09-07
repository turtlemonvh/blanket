//go:build windows

package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// swap on Windows cannot be a single rename.
//
// Windows refuses to replace a file that is currently mapped for
// execution — MoveFileEx onto a running .exe fails with
// ERROR_ACCESS_DENIED — but it does allow the running .exe to be *renamed
// out of the way*, because the handle follows the file rather than the
// path. So the swap is two renames:
//
//	blanket.exe            -> blanket.exe.old-<ts>     (the running one)
//	.blanket-upgrade-XXXX  -> blanket.exe              (the new one)
//
// and the displaced file is removed on a later run rather than now: it is
// still executing, so the delete would fail, and failing the upgrade over
// a leftover temp file would be absurd. `blanket upgrade` and
// `blanket rollback` sweep it on their next invocation.
//
// If the second rename fails, the first is undone. Leaving a Windows box
// with no blanket.exe at all is the one outcome worse than a failed
// upgrade.
func swap(stagedPath, targetPath string) error {
	displaced := fmt.Sprintf("%s.old-%d", targetPath, time.Now().UnixNano())

	if err := os.Rename(targetPath, displaced); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("could not move the running binary aside: %w", err)
		}
		// Nothing there to displace (a fresh install); fall through.
		displaced = ""
	}

	if err := os.Rename(stagedPath, targetPath); err != nil {
		if displaced != "" {
			// Best effort: put it back.
			if rerr := os.Rename(displaced, targetPath); rerr != nil {
				return fmt.Errorf("could not install %s (%v) and could not restore %s (%v); "+
					"the previous binary is at %s", targetPath, err, targetPath, rerr, displaced)
			}
		}
		return err
	}

	// Try once; expected to fail while the old process runs.
	if displaced != "" {
		_ = os.Remove(displaced)
	}
	return nil
}

// SweepDisplaced removes `<binary>.old-*` files left by a previous swap
// whose process has since exited. Called at the start of an upgrade or
// rollback, where a failure is only worth a log line.
func SweepDisplaced(targetPath string) []string {
	matches, err := filepath.Glob(targetPath + ".old-*")
	if err != nil {
		return nil
	}
	var removed []string
	for _, m := range matches {
		if os.Remove(m) == nil {
			removed = append(removed, m)
		}
	}
	return removed
}

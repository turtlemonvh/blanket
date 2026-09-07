//go:build !windows

package upgrade

import "os"

// swap on unix is a single rename.
//
// Renaming over the file a running process is executing is safe and is the
// standard way to replace a binary: the kernel holds the old inode open
// for the running process, which keeps executing the image it started
// with, while every new exec of the path gets the new file. That is
// precisely the property an upgrade needs, since the server is still
// serving at this point in the sequence and will not be replaced until it
// re-execs or its supervisor restarts it.
//
// It is also why the *write* must not be in place: writing into the
// existing inode mutates the image the running process is executing from.
func swap(stagedPath, targetPath string) error {
	return os.Rename(stagedPath, targetPath)
}

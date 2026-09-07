//go:build linux || darwin

package diskfree

import "golang.org/x/sys/unix"

// statfs(2) reports block counts plus a block size. The field types differ
// between platforms (linux's Bsize is int64, darwin's is uint32; Bavail is
// uint64 on both), so every term is converted explicitly rather than
// relying on either platform's shape.
func available(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

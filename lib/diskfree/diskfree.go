// Package diskfree answers one question — "how many bytes can I still
// write to the filesystem holding this path?" — and, like lib/proclive,
// says when it cannot answer at all.
//
// The consumer is the pre-migration backup (turtlemonvh/blanket#23 phase
// 4). A backup is a byte-for-byte copy of the bolt file made with
// tx.WriteTo, so it momentarily doubles the database's footprint on disk.
// Filling the disk while doing that would be a spectacular own goal: the
// operation whose entire job is to make an upgrade safe would be the thing
// that breaks the install. Hence a precheck.
//
// Refusing on an *unknown* free-space figure would be worse than useless,
// though — it would make backups impossible on any platform this package
// hasn't been ported to, and a backup is mandatory before a migration. So
// Available returns ErrUnsupported rather than a made-up number, and the
// caller treats "don't know" as "proceed, but say so" (see
// bolt.CheckBackupSpace).
package diskfree

import "errors"

// ErrUnsupported is returned by Available on a platform with no
// implementation. Callers should treat it as "no answer", not as "no
// space".
var ErrUnsupported = errors.New("diskfree: not implemented on this platform")

// Available reports the number of bytes available to an unprivileged
// writer on the filesystem containing path. path must exist; a directory
// is the usual thing to pass.
//
// Deliberately the *available* count rather than the free one: on ext4 and
// friends a percentage of the filesystem is reserved for root, and a
// blanket process writing a backup will not get to use it.
func Available(path string) (uint64, error) {
	return available(path)
}

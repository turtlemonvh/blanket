// Package upgrade is the machinery behind `blanket upgrade` and `blanket
// rollback` (turtlemonvh/blanket#23 phase 6): checksum verification,
// release discovery, offline bundles, atomic staging, rollback slots, the
// CLI-owned journal, and the update notice.
//
// It is deliberately viper-free and process-free. Everything here takes
// explicit paths and an explicit http.Client, so the whole surface is
// testable from `go test` without a config file, a server, or a network —
// and so command/upgrade.go stays a sequencer rather than a second
// implementation of any of it.
package upgrade

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Sums is a parsed SHA256SUMS file: asset basename -> lowercase hex digest.
//
// Releases from phase 6 onward publish one covering every binary in the
// release, and every download this package performs is checked against it.
// There is deliberately no --no-verify escape hatch (brief decision row
// 11): a flag that turns off the only integrity check would be the flag
// every "just make it work" answer on the internet told people to pass.
// An older release with no SHA256SUMS asset is not auto-upgradable, and
// the error says to use --bundle instead.
type Sums map[string]string

var (
	// ErrNoChecksumEntry means the SHA256SUMS file parsed fine but says
	// nothing about the file we are about to install. That is not a
	// "close enough": a sums file that does not cover the asset provides
	// exactly as much assurance as no sums file at all.
	ErrNoChecksumEntry = errors.New("no SHA256SUMS entry for this file")

	// ErrChecksumMismatch means the bytes on disk are not the bytes the
	// release says they are.
	ErrChecksumMismatch = errors.New("checksum mismatch")
)

// ParseSums reads the standard `sha256sum` output format:
//
//	<64 hex digits>  <name>
//
// One or more spaces separate the two fields, and a `*` before the name
// (sha256sum's "binary mode" marker) is tolerated, as is a leading `./`.
// Blank lines and `#` comments are skipped so the file can carry a header.
//
// Names are reduced to their basename. A sums file generated from a
// directory of release assets and one generated inside a bundle differ
// only in path prefix, and a verifier that cared about the difference
// would reject perfectly good bundles.
func ParseSums(r io.Reader) (Sums, error) {
	sums := Sums{}
	sc := bufio.NewScanner(r)
	// A SHA256SUMS file is a few hundred bytes; a huge "line" means we are
	// reading something that is not one, and should say so rather than
	// buffering it.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) < 2 {
			return nil, fmt.Errorf("SHA256SUMS line %d is not <digest> <name>: %q", line, text)
		}
		digest := strings.ToLower(fields[0])
		if len(digest) != 64 {
			return nil, fmt.Errorf("SHA256SUMS line %d: %q is not a sha256 digest", line, fields[0])
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return nil, fmt.Errorf("SHA256SUMS line %d: %q is not hex", line, fields[0])
		}
		name := strings.Join(fields[1:], " ")
		name = strings.TrimPrefix(name, "*")
		sums[path.Base(strings.ReplaceAll(name, "\\", "/"))] = digest
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(sums) == 0 {
		return nil, errors.New("SHA256SUMS is empty")
	}
	return sums, nil
}

// ParseSumsFile is ParseSums over a file on disk.
func ParseSumsFile(p string) (Sums, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseSums(f)
}

// Lookup returns the digest recorded for an asset name.
func (s Sums) Lookup(name string) (string, bool) {
	d, ok := s[path.Base(strings.ReplaceAll(name, "\\", "/"))]
	return d, ok
}

// FileSHA256 hashes a file, streaming it rather than reading it in.
func FileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyFile checks the file at p against the SHA256SUMS entry for
// assetName. A missing entry is a failure, not a pass.
func VerifyFile(p, assetName string, sums Sums) error {
	want, ok := sums.Lookup(assetName)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoChecksumEntry, assetName)
	}
	return VerifyFileDigest(p, assetName, want)
}

// VerifyFileDigest checks the file at p against an expected digest. Used
// by rollback, where the digest comes from the slot's own metadata rather
// than from a sums file.
func VerifyFileDigest(p, assetName, want string) error {
	got, err := FileSHA256(p)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%w for %s: expected %s, got %s", ErrChecksumMismatch, assetName, strings.ToLower(want), got)
	}
	return nil
}

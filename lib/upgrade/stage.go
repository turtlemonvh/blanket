package upgrade

/*

Atomic staging (turtlemonvh/blanket#23 phase 6).

The rule the whole file exists to enforce: **the installed path is never a
partial file**. Downloading straight over `~/.local/bin/blanket` — which is
what scripts/install.sh did before this phase, and what most one-line
installers still do — has three distinct failure modes, and a truncated
download hits all three at once:

  - a download that dies half-way leaves an unrunnable binary where a
    working one used to be, and the operator's next move ("run blanket") is
    the one thing that cannot help them;
  - the bytes are never checked, so a corrupted or substituted download is
    installed with no signal at all;
  - on unix, writing to the file a running process has mapped gets ETXTBSY
    on some kernels and silently corrupts the running image on others.

So: download to a temp file **in the same directory** as the target (a
different directory can be a different filesystem, and then the final step
is a copy rather than a rename), verify it against SHA256SUMS, and only
then rename it into place. Rename within a directory is atomic: any reader
sees either the old file or the new one, never a mixture.

The last step is platform-specific — see swap_unix.go and swap_windows.go —
because Windows cannot rename over a file that is currently executing.

*/

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// StagePrefix is the temp-file prefix used next to the installed binary.
// Dotted so it is out of the way in a `ls`, and distinctive so a stray one
// left by a killed CLI is obviously ours.
const StagePrefix = ".blanket-upgrade-"

// StagedFile is a verified, not-yet-installed binary.
type StagedFile struct {
	Path      string
	AssetName string
	SHA256    string
	Size      int64
}

// Stage writes the bytes from r to a temp file beside targetPath, marks it
// executable, and verifies it against the SHA256SUMS entry for assetName.
//
// A verification failure removes the temp file and returns the error: a
// staged file that failed its checksum has exactly one correct fate, and
// leaving it around for --resume to find would make that fate optional.
func Stage(targetPath, assetName string, sums Sums, r io.Reader) (*StagedFile, error) {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, StagePrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("could not create a staging file in %s: %w", dir, err)
	}
	name := tmp.Name()

	n, err := io.Copy(tmp, r)
	if err != nil {
		tmp.Close()
		os.Remove(name)
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return nil, err
	}
	// CreateTemp makes 0600. The staged file becomes the installed binary
	// by rename, so it must already carry the mode the installed binary
	// needs -- there is no window in which to chmod it afterwards.
	if err := os.Chmod(name, 0o755); err != nil {
		os.Remove(name)
		return nil, err
	}

	if err := VerifyFile(name, assetName, sums); err != nil {
		os.Remove(name)
		return nil, err
	}
	sum, err := FileSHA256(name)
	if err != nil {
		os.Remove(name)
		return nil, err
	}

	return &StagedFile{Path: name, AssetName: assetName, SHA256: sum, Size: n}, nil
}

// StageFromFile stages a binary that is already on disk (the --bundle
// path). Identical verification: an offline install is not a less
// trustworthy one, and the bundle carries the same SHA256SUMS the release
// does.
func StageFromFile(targetPath, assetName, srcPath string, sums Sums) (*StagedFile, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Stage(targetPath, assetName, sums, f)
}

// StageFromURL downloads and stages in one pass, never holding the whole
// binary in memory.
func StageFromURL(ctx context.Context, client *http.Client, url, targetPath, assetName string, sums Sums) (*StagedFile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: HTTP %d", url, res.StatusCode)
	}
	return Stage(targetPath, assetName, sums, res.Body)
}

// Swap installs a staged file at targetPath. See swap_unix.go /
// swap_windows.go for what "install" has to mean on each.
func Swap(stagedPath, targetPath string) error {
	return swap(stagedPath, targetPath)
}

// CleanStaging removes leftover staging files next to targetPath, except
// the one named by keep. A CLI that was killed mid-download leaves one
// behind, and it is dead weight next to the binary rather than anything
// --resume can use (an unverified partial download is not resumable: the
// only safe thing to do with it is to fetch again).
func CleanStaging(targetPath, keep string) []string {
	dir := filepath.Dir(targetPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range entries {
		if e.IsDir() || len(e.Name()) <= len(StagePrefix) || e.Name()[:len(StagePrefix)] != StagePrefix {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if keep != "" && p == keep {
			continue
		}
		if os.Remove(p) == nil {
			removed = append(removed, p)
		}
	}
	return removed
}

// ResolveInstalledPath follows symlinks to the file an upgrade must
// actually replace.
//
// This matters more than it looks. A packaged install often has
// `/usr/local/bin/blanket -> /opt/blanket/bin/blanket`, and renaming over
// the *symlink* would replace the link with a regular file — quietly
// unhooking the install from whatever manages it, and leaving the real
// binary untouched so a rollback would have nothing to undo.
func ResolveInstalledPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// A path that cannot be resolved is reported as-is; the caller's
		// next step (stat/open) produces the better error message.
		return abs, nil
	}
	return resolved, nil
}

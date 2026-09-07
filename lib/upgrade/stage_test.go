package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStageIsAtomic is the property the whole staging design exists for:
// at no point does the installed path hold anything but a complete file.
func TestStageIsAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "blanket")
	if err := os.WriteFile(target, []byte("OLD BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}

	body := "NEW BINARY"
	sum := sha256Of(t, body)

	staged, err := Stage(target, "blanket-linux-amd64", Sums{"blanket-linux-amd64": sum}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	// Staged beside the target, not over it.
	if filepath.Dir(staged.Path) != dir {
		t.Errorf("staged in %s, want %s -- a different directory can be a different filesystem", filepath.Dir(staged.Path), dir)
	}
	if got, _ := os.ReadFile(target); string(got) != "OLD BINARY" {
		t.Errorf("staging touched the installed binary: %q", got)
	}
	if fi, err := os.Stat(staged.Path); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("staged file is not executable (%v); it becomes the binary by rename, with no window to chmod in", fi.Mode())
	}

	if err := Swap(staged.Path, target); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("after swap target holds %q, want %q", got, body)
	}
	if _, err := os.Stat(staged.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staged file should be gone after the swap")
	}
}

// TestStageRefusesBadChecksum: a staged file that failed verification has
// exactly one correct fate, and leaving it on disk would make that
// optional.
func TestStageRefusesBadChecksum(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "blanket")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Stage(target, "blanket-linux-amd64",
		Sums{"blanket-linux-amd64": sha256Of(t, "WHAT WE EXPECTED")}, strings.NewReader("SOMETHING ELSE"))
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "OLD" {
		t.Errorf("a failed stage must not touch the installed binary; it holds %q", got)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, StagePrefix+"*"))
	if len(leftovers) != 0 {
		t.Errorf("a failed stage left %v behind", leftovers)
	}
}

func TestStageRefusesUncoveredAsset(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "blanket")
	_, err := Stage(target, "blanket-linux-amd64", Sums{"something-else": sha256Of(t, "x")}, strings.NewReader("x"))
	if !errors.Is(err, ErrNoChecksumEntry) {
		t.Fatalf("want ErrNoChecksumEntry, got %v", err)
	}
}

func TestCleanStaging(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "blanket")
	keep := filepath.Join(dir, StagePrefix+"keep")
	drop := filepath.Join(dir, StagePrefix+"drop")
	other := filepath.Join(dir, "unrelated")
	for _, p := range []string{target, keep, drop, other} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	CleanStaging(target, keep)

	if _, err := os.Stat(drop); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale staging file was not removed")
	}
	for _, p := range []string{keep, other, target} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should have survived: %v", p, err)
		}
	}
}

func TestResolveInstalledPathFollowsSymlinks(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("symlinks need privilege on windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real-blanket")
	if err := os.WriteFile(real, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "blanket")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	got, err := ResolveInstalledPath(link)
	if err != nil {
		t.Fatal(err)
	}
	// Renaming over the symlink would replace the link with a regular
	// file, unhooking the install from whatever manages it and leaving the
	// real binary untouched.
	wantReal, _ := filepath.EvalSymlinks(real)
	if got != wantReal {
		t.Errorf("ResolveInstalledPath(%s) = %s, want %s", link, got, wantReal)
	}
}

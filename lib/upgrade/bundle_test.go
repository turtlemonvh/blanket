package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeBundleDir builds a bundle laid out the way scripts/bundle.sh does.
func makeBundleDir(t *testing.T, version string, bodies map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "blanket-bundle-"+version)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	m := Manifest{Schema: BundleSchema, Version: version, Generator: "test"}
	var sums strings.Builder
	for name, body := range bodies {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		sum := sha256Of(t, body)
		fmt.Fprintf(&sums, "%s  %s\n", sum, name)
		m.Binaries = append(m.Binaries, ManifestBinary{Name: name, SHA256: sum, Size: int64(len(body))})
	}
	if err := os.WriteFile(filepath.Join(root, SumsAssetName), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(root, ManifestName), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func tarGzDir(t *testing.T, root, out string) {
	t.Helper()
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	base := filepath.Base(root)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		hdr := &tar.Header{Name: base + "/" + e.Name(), Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
}

func TestOpenBundleDirectory(t *testing.T) {
	name := AssetNameFor("linux", "amd64")
	root := makeBundleDir(t, "v0.5.0", map[string]string{name: "NEW BINARY"})

	b, err := OpenBundle(root)
	if err != nil {
		t.Fatalf("OpenBundle: %v", err)
	}
	defer b.Close()

	if b.Manifest.Version != "v0.5.0" {
		t.Errorf("manifest version = %q", b.Manifest.Version)
	}
	if _, ok := b.Sums.Lookup(name); !ok {
		t.Errorf("bundle sums do not cover %s", name)
	}
	if _, err := os.Stat(b.BinaryPath(name)); err != nil {
		t.Errorf("bundle binary missing: %v", err)
	}
}

func TestOpenBundleTarball(t *testing.T) {
	name := AssetNameFor("linux", "amd64")
	root := makeBundleDir(t, "v0.5.0", map[string]string{name: "NEW BINARY"})
	tgz := filepath.Join(t.TempDir(), "bundle.tar.gz")
	tarGzDir(t, root, tgz)

	b, err := OpenBundle(tgz)
	if err != nil {
		t.Fatalf("OpenBundle: %v", err)
	}
	extracted := b.Root
	if b.Manifest.Version != "v0.5.0" {
		t.Errorf("manifest version = %q", b.Manifest.Version)
	}

	// An offline install verifies identically to an online one: the
	// bundle is not trusted for being local.
	target := filepath.Join(t.TempDir(), "blanket")
	if _, err := StageFromFile(target, name, b.BinaryPath(name), b.Sums); err != nil {
		t.Fatalf("staging from a bundle should verify and succeed: %v", err)
	}

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(extracted); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Close should remove the extraction directory %s", extracted)
	}
}

func TestParseManifest(t *testing.T) {
	if _, err := ParseManifest([]byte(`{"version":"v1.0.0"}`)); err == nil {
		t.Error("a manifest with no schema should be refused")
	}
	if _, err := ParseManifest([]byte(`{"schema":1}`)); err == nil {
		t.Error("a manifest naming no version should be refused")
	}
	if _, err := ParseManifest([]byte(fmt.Sprintf(`{"schema":%d,"version":"v9"}`, BundleSchema+1))); err == nil {
		t.Error("a manifest from a newer schema should be refused, not guessed at")
	}
	m, err := ParseManifest([]byte(`{"schema":1,"version":"v1.2.3","binaries":[{"name":"blanket-linux-amd64","sha256":"aa"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := m.Binary("blanket-linux-amd64"); !ok || b.SHA256 != "aa" {
		t.Errorf("Binary lookup failed: %+v", m)
	}
}

// TestExtractRefusesTraversal: the archive is a file an operator was told
// to download, so an entry named ../../.ssh/authorized_keys must not be
// able to turn "unpack this" into "write anywhere I can write".
func TestExtractRefusesTraversal(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("pwned")
	tw.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()

	dir := t.TempDir()
	tgz := filepath.Join(dir, "evil.tar.gz")
	if err := os.WriteFile(tgz, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	err := extractTarGz(tgz, filepath.Join(dir, "out"))
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("want a traversal refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("the traversal entry was written")
	}
}

func TestOpenBundleRejectsNonBundle(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenBundle(dir); err == nil {
		t.Error("a directory with no manifest is not a bundle")
	}
	p := filepath.Join(dir, "nope.tar.gz")
	if err := os.WriteFile(p, []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBundle(p); err == nil {
		t.Error("a non-gzip file should be refused")
	}
}

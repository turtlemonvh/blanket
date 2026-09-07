package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodSums = `# blanket v0.5.0
9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08  blanket-linux-amd64
2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae  ./blanket-darwin-amd64
fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9 *blanket-windows-amd64.exe
`

func TestParseSums(t *testing.T) {
	sums, err := ParseSums(strings.NewReader(goodSums))
	if err != nil {
		t.Fatalf("ParseSums: %v", err)
	}
	if len(sums) != 3 {
		t.Fatalf("want 3 entries, got %d: %v", len(sums), sums)
	}
	// A `./` prefix and a `*` binary marker must not change the key: a
	// sums file generated from a directory and one generated inside a
	// bundle differ only in that, and rejecting either would reject a
	// perfectly good bundle.
	if _, ok := sums.Lookup("blanket-darwin-amd64"); !ok {
		t.Errorf("./-prefixed name did not resolve: %v", sums)
	}
	if _, ok := sums.Lookup("blanket-windows-amd64.exe"); !ok {
		t.Errorf("*-prefixed name did not resolve: %v", sums)
	}
}

func TestParseSumsRejectsGarbage(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"only comments": "# nothing here\n\n",
		"short digest":  "abc123  blanket-linux-amd64\n",
		"not hex":       "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz  blanket-linux-amd64\n",
		"no name":       "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08\n",
	}
	for name, body := range cases {
		if _, err := ParseSums(strings.NewReader(body)); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

func writeTemp(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVerifyFile(t *testing.T) {
	dir := t.TempDir()
	// sha256("hello") -- the digest in goodSums for blanket-linux-amd64 is
	// sha256("a"), so this file is deliberately the wrong one.
	p := writeTemp(t, dir, "blanket-linux-amd64", "a")

	sums, err := ParseSums(strings.NewReader(goodSums))
	if err != nil {
		t.Fatal(err)
	}

	// sha256("a") == ca978112... , not the 9f86d0.. in the fixture, so
	// this must be a mismatch rather than a pass.
	if err := VerifyFile(p, "blanket-linux-amd64", sums); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}

	// Now with the right digest.
	real, err := FileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(p, "blanket-linux-amd64", Sums{"blanket-linux-amd64": real}); err != nil {
		t.Fatalf("matching digest should verify: %v", err)
	}

	// A missing entry is a failure, not a pass: a sums file that doesn't
	// cover the asset provides exactly as much assurance as no file.
	if err := VerifyFile(p, "blanket-plan9-amd64", sums); !errors.Is(err, ErrNoChecksumEntry) {
		t.Fatalf("want ErrNoChecksumEntry, got %v", err)
	}
}

func TestFileSHA256KnownValue(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "x", "abc")
	got, err := FileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("sha256(abc) = %s, want %s", got, want)
	}
}

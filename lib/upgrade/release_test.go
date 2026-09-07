package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeReleases serves a Releases API and its assets from one httptest
// server, which is what lets the whole discovery path be exercised without
// touching api.github.com -- a test suite that reached GitHub would fail
// whenever an unauthenticated CI runner got rate-limited.
type fakeReleases struct {
	t        *testing.T
	releases []Release
	assets   map[string]string // path -> body
	server   *httptest.Server
}

func newFakeReleases(t *testing.T) *fakeReleases {
	f := &fakeReleases{t: t, assets: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/blanket/releases", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(f.releases)
	})
	mux.HandleFunc("/repos/acme/blanket/releases/tags/", func(w http.ResponseWriter, r *http.Request) {
		tag := strings.TrimPrefix(r.URL.Path, "/repos/acme/blanket/releases/tags/")
		for _, rel := range f.releases {
			if rel.TagName == tag {
				json.NewEncoder(w).Encode(rel)
				return
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := f.assets[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeReleases) addRelease(tag string, prerelease bool, files map[string]string) {
	rel := Release{TagName: tag, Prerelease: prerelease}
	for name, body := range files {
		p := "/dl/" + tag + "/" + name
		f.assets[p] = body
		rel.Assets = append(rel.Assets, Asset{Name: name, URL: f.server.URL + p, Size: int64(len(body))})
	}
	f.releases = append([]Release{rel}, f.releases...)
}

func (f *fakeReleases) client() *Client {
	return &Client{BaseURL: f.server.URL, Repo: "acme/blanket", HTTP: f.server.Client()}
}

func TestLatestSkipsPrereleases(t *testing.T) {
	f := newFakeReleases(t)
	f.addRelease("v0.4.0", false, map[string]string{SumsAssetName: "x"})
	f.addRelease("v0.5.0-rc1", true, map[string]string{SumsAssetName: "x"})

	rel, err := f.client().Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v0.4.0" {
		t.Errorf("Latest picked %s; a prerelease must not be offered as an upgrade", rel.TagName)
	}
	// It is still reachable by name.
	rc, err := f.client().Tag(context.Background(), "v0.5.0-rc1")
	if err != nil || rc.TagName != "v0.5.0-rc1" {
		t.Errorf("Tag(v0.5.0-rc1) = (%v, %v)", rc, err)
	}
}

func TestSumsFromRelease(t *testing.T) {
	body := "NEW BINARY"
	name := AssetNameFor("linux", "amd64")
	sums := fmt.Sprintf("%s  %s\n", sha256Of(t, body), name)

	f := newFakeReleases(t)
	f.addRelease("v0.5.0", false, map[string]string{name: body, SumsAssetName: sums})

	c := f.client()
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := c.Sums(context.Background(), rel)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed.Lookup(name); !ok {
		t.Fatalf("sums do not cover %s: %v", name, parsed)
	}
}

// TestReleaseWithoutChecksumsIsRefused is brief decision row 11: releases
// published before phase 6 carry no SHA256SUMS, and there is no
// --no-verify to get around it.
func TestReleaseWithoutChecksumsIsRefused(t *testing.T) {
	f := newFakeReleases(t)
	f.addRelease("v0.3.0", false, map[string]string{AssetNameFor("linux", "amd64"): "old"})

	c := f.client()
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sums(context.Background(), rel); !errors.Is(err, ErrNoChecksums) {
		t.Fatalf("want ErrNoChecksums, got %v", err)
	}
}

func TestStageFromURLVerifies(t *testing.T) {
	body := "NEW BINARY"
	name := AssetNameFor("linux", "amd64")
	f := newFakeReleases(t)
	f.addRelease("v0.5.0", false, map[string]string{
		name:          body,
		SumsAssetName: fmt.Sprintf("%s  %s\n", sha256Of(t, body), name),
	})

	c := f.client()
	rel, _ := c.Latest(context.Background())
	sums, err := c.Sums(context.Background(), rel)
	if err != nil {
		t.Fatal(err)
	}
	asset, ok := rel.Asset(name)
	if !ok {
		t.Fatal("asset missing")
	}

	dir := t.TempDir()
	target := dir + "/blanket"
	staged, err := StageFromURL(context.Background(), f.server.Client(), asset.URL, target, name, sums)
	if err != nil {
		t.Fatalf("StageFromURL: %v", err)
	}
	if staged.Size != int64(len(body)) {
		t.Errorf("staged %d bytes, want %d", staged.Size, len(body))
	}

	// The same download with a sums file that disagrees must refuse.
	bad := Sums{name: sha256Of(t, "something else")}
	if _, err := StageFromURL(context.Background(), f.server.Client(), asset.URL, target, name, bad); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
}

func TestAssetNameFor(t *testing.T) {
	cases := map[[2]string]string{
		{"linux", "amd64"}:   "blanket-linux-amd64",
		{"darwin", "amd64"}:  "blanket-darwin-amd64",
		{"windows", "amd64"}: "blanket-windows-amd64.exe",
	}
	for k, want := range cases {
		if got := AssetNameFor(k[0], k[1]); got != want {
			t.Errorf("AssetNameFor(%s, %s) = %s, want %s", k[0], k[1], got, want)
		}
	}
}

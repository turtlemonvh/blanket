package upgrade

/*

Release discovery (turtlemonvh/blanket#23 phase 6).

Talks to the GitHub Releases API and nothing else. The interesting parts
are what it refuses to do:

  - **It never trusts an asset it cannot check.** A release with no
    `SHA256SUMS` asset is reported as not auto-upgradable, with the
    `--bundle` workaround named in the error. Releases cut before phase 6
    are all in that category and always will be — the checksums are
    generated at build time, and there is no honest way to add them to a
    published release after the fact (brief decision row 11).
  - **It never picks a release for you beyond "latest".** Prereleases are
    skipped by `Latest`, and reachable only by naming the tag.

BaseURL exists so the subprocess test can serve a fake Releases API off
localhost. It is a hidden flag and config key rather than a constant for
exactly one reason: a test suite that reaches api.github.com is a test
suite that fails when GitHub rate-limits an unauthenticated CI runner.

*/

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
)

const (
	// DefaultRepo is the repository releases are discovered from.
	DefaultRepo = "turtlemonvh/blanket"
	// DefaultReleasesBaseURL is the GitHub API root.
	DefaultReleasesBaseURL = "https://api.github.com"
	// SumsAssetName is the checksum manifest every release from phase 6
	// onward carries.
	SumsAssetName = "SHA256SUMS"
	// maxMetadataBytes caps the JSON and SHA256SUMS reads. Both are small;
	// the cap is here so a wrong URL cannot balloon the CLI's memory.
	maxMetadataBytes = 4 << 20
)

// ErrNoChecksums is returned for a release that publishes no SHA256SUMS
// asset.
var ErrNoChecksums = errors.New("this release publishes no " + SumsAssetName + " asset")

// Asset is one file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release is the subset of the GitHub API response this needs.
type Release struct {
	TagName    string  `json:"tag_name"`
	Name       string  `json:"name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	HTMLURL    string  `json:"html_url"`
	Assets     []Asset `json:"assets"`
}

// Asset finds an asset by name.
func (r *Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Client talks to a Releases API.
type Client struct {
	BaseURL string
	Repo    string
	HTTP    *http.Client
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultReleasesBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) repo() string {
	if c.Repo == "" {
		return DefaultRepo
	}
	return c.Repo
}

func (c *Client) http() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

// Latest returns the newest non-draft, non-prerelease release.
//
// It reads /releases rather than /releases/latest so a repository whose
// most recent tag is a prerelease still resolves to the newest *stable*
// release, and so the "no stable release yet" case is reported as such
// instead of as a 404.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	var rels []Release
	if err := c.getJSON(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=30", c.base(), c.repo()), &rels); err != nil {
		return nil, err
	}
	for i := range rels {
		if rels[i].Draft || rels[i].Prerelease {
			continue
		}
		return &rels[i], nil
	}
	return nil, errors.New("no published release found")
}

// Tag returns one release by tag name.
func (c *Client) Tag(ctx context.Context, tag string) (*Release, error) {
	var rel Release
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", c.base(), c.repo(), tag)
	if err := c.getJSON(ctx, url, &rel); err != nil {
		return nil, fmt.Errorf("could not find release %s: %w", tag, err)
	}
	return &rel, nil
}

// Sums downloads and parses a release's SHA256SUMS asset. A release
// without one gets ErrNoChecksums, not a shrug.
func (c *Client) Sums(ctx context.Context, rel *Release) (Sums, error) {
	a, ok := rel.Asset(SumsAssetName)
	if !ok {
		return nil, fmt.Errorf("%w (%s)", ErrNoChecksums, rel.TagName)
	}
	body, err := c.get(ctx, a.URL)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return ParseSums(io.LimitReader(body, maxMetadataBytes))
}

func (c *Client) getJSON(ctx context.Context, url string, out interface{}) error {
	body, err := c.get(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, maxMetadataBytes))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (c *Client) get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "blanket-upgrade")
	res, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		res.Body.Close()
		msg := strings.TrimSpace(string(b))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("GET %s: HTTP %d %s", url, res.StatusCode, msg)
	}
	return res.Body, nil
}

// AssetNameFor returns the release asset name for a GOOS/GOARCH pair, in
// the naming the Makefile's cross-compile targets produce.
//
// The upgrade always installs the binary for the platform it is *running
// on*, never one named by a flag. Cross-installing is a thing a person
// does deliberately with a bundle and `cp`, and making it a flag on
// `upgrade` would mean one typo replaces a working install with a binary
// the machine cannot execute.
func AssetNameFor(goos, goarch string) string {
	name := fmt.Sprintf("blanket-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// LocalAssetName is AssetNameFor this build.
func LocalAssetName() string { return AssetNameFor(runtime.GOOS, runtime.GOARCH) }

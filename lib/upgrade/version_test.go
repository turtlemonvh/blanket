package upgrade

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.4.0", "v0.5.0", -1},
		{"v0.5.0", "v0.4.0", 1},
		{"v0.5.0", "v0.5.0", 0},
		{"0.5.0", "v0.5.0", 0},
		{"v0.5.1", "v0.5.0", 1},
		{"v1.0.0", "v0.99.99", 1},
		// A prerelease sorts below the release it precedes, which is what
		// stops an rc being offered as an upgrade to the version after it.
		{"v0.5.0-rc1", "v0.5.0", -1},
		{"v0.5.0", "v0.5.0-rc1", 1},
		{"v0.5.0-rc1", "v0.5.0-rc2", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	if !IsNewer("v0.4.0", "v0.5.0") {
		t.Error("v0.5.0 is newer than v0.4.0")
	}
	if IsNewer("v0.5.0", "v0.4.0") {
		t.Error("v0.4.0 is not newer than v0.5.0")
	}
	if IsNewer("v0.5.0", "v0.5.0-rc1") {
		t.Error("a prerelease must not be offered over the release")
	}
	// A dev build has no release to compare against, so the honest answer
	// to "should I offer this" is yes.
	if !IsNewer("", "v0.5.0") {
		t.Error("a dev build should be offered an upgrade")
	}
	// ... but nonsense on the other side is never an upgrade.
	if IsNewer("v0.4.0", "not-a-version") {
		t.Error("an unparseable candidate is never newer")
	}
}

func TestSameVersion(t *testing.T) {
	if !SameVersion("v0.5.0", "0.5.0") {
		t.Error("the v prefix must not matter")
	}
	if SameVersion("", "") {
		t.Error("two dev builds are not the same version; there is nothing to compare")
	}
	if SameVersion("v0.5.0", "v0.5.1") {
		t.Error("different versions")
	}
}

// TestVersionFromBanner: the CLI verifies a restart by reading the banner
// the server reports, because it must be able to verify a server built
// before any new response field would have existed.
func TestVersionFromBanner(t *testing.T) {
	cases := map[string]string{
		"blanket v0.5.0 (built 2026-09-06 11:14 PM EDT)":       "v0.5.0",
		"blanket v1.0.0-rc1 (built 2026-09-06)":                "v1.0.0-rc1",
		"blanket (dev) branch=master commit=abc (built today)": "",
		"":                        "",
		"something else entirely": "",
	}
	for banner, want := range cases {
		if got := VersionFromBanner(banner); got != want {
			t.Errorf("VersionFromBanner(%q) = %q, want %q", banner, got, want)
		}
	}
}

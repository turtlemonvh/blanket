package upgrade

import (
	"regexp"
	"strconv"
	"strings"
)

// Version comparison, deliberately small.
//
// blanket's tags are `vMAJOR.MINOR.PATCH`, optionally with a prerelease
// suffix (`v0.4.0-rc1`). That is the whole vocabulary this needs to
// understand, and a full semver implementation would be a dependency plus
// a body of behaviour nothing in blanket exercises. What it must get right
// is the one comparison the update notice and `--check` make: "is the tag
// on the releases page newer than the one this binary was built from?"

var versionRe = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:[-+](.*))?$`)

// ParsedVersion is a tag broken into its parts. Ok is false for anything
// that does not look like a version at all, which is how a dev build
// (`VERSION` unset, so the string is empty) is told apart from a release.
type ParsedVersion struct {
	Major, Minor, Patch int
	Prerelease          string
	Ok                  bool
}

// ParseVersion parses `v0.4.0`, `0.4`, `v1`, `v0.4.0-rc1`.
func ParseVersion(s string) ParsedVersion {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ParsedVersion{}
	}
	atoi := func(x string) int {
		if x == "" {
			return 0
		}
		n, _ := strconv.Atoi(x)
		return n
	}
	return ParsedVersion{
		Major:      atoi(m[1]),
		Minor:      atoi(m[2]),
		Patch:      atoi(m[3]),
		Prerelease: m[4],
		Ok:         true,
	}
}

// CompareVersions returns -1, 0 or 1 for a<b, a==b, a>b. Versions that do
// not parse compare equal to nothing: the caller gets 0 and is expected to
// have checked Ok first if the distinction matters.
//
// A prerelease sorts *below* the same numbers without one — v0.4.0-rc1 is
// older than v0.4.0 — which is the property that stops an rc from being
// offered as an upgrade to the release it preceded.
func CompareVersions(a, b string) int {
	pa, pb := ParseVersion(a), ParseVersion(b)
	if !pa.Ok || !pb.Ok {
		return 0
	}
	for _, pair := range [][2]int{{pa.Major, pb.Major}, {pa.Minor, pb.Minor}, {pa.Patch, pb.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa.Prerelease == pb.Prerelease:
		return 0
	case pa.Prerelease == "":
		return 1
	case pb.Prerelease == "":
		return -1
	case pa.Prerelease < pb.Prerelease:
		return -1
	default:
		return 1
	}
}

// IsNewer reports whether candidate is strictly newer than current. An
// unparseable current version (a dev build) makes everything "newer",
// because a dev build has no release it can meaningfully be compared to
// and the honest answer to "should I offer this" is yes.
func IsNewer(current, candidate string) bool {
	if !ParseVersion(candidate).Ok {
		return false
	}
	if !ParseVersion(current).Ok {
		return true
	}
	return CompareVersions(candidate, current) > 0
}

// SameVersion reports whether two tags name the same release, tolerating a
// missing or present `v` prefix on either side.
func SameVersion(a, b string) bool {
	na, nb := strings.TrimPrefix(strings.TrimSpace(a), "v"), strings.TrimPrefix(strings.TrimSpace(b), "v")
	return na != "" && na == nb
}

// versionInBanner pulls the tag out of the banner string the server
// reports at GET /version and GET /ops/restart/status — which is
// `blanket v0.4.0 (built ...)` for a tagged build and
// `blanket (dev) branch=... commit=...` for everything else.
//
// Reading the banner rather than adding a `rawVersion` field to the
// response is a deliberate trade: post-upgrade verification is the only
// consumer, and a new response field is a change to docs/api.md and to
// every server that has to be upgraded *to* before the CLI can verify it.
// The CLI must be able to verify a server it just installed, including one
// built before this field would have existed.
var bannerRe = regexp.MustCompile(`^blanket\s+(\S+)`)

// VersionFromBanner returns the tag in a banner, or "" for a dev build.
func VersionFromBanner(banner string) string {
	m := bannerRe.FindStringSubmatch(strings.TrimSpace(banner))
	if m == nil {
		return ""
	}
	if !ParseVersion(m[1]).Ok {
		return ""
	}
	return m[1]
}

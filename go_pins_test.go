package main

import (
	"os"
	"regexp"
	"testing"
)

// The three Go version pins have to agree, and until now that was a prose
// invariant in CLAUDE.md that nothing enforced. It broke silently:
// Dependabot's #185 (a fsnotify + x/sys bump) rewrote go.mod's `go`
// directive to 1.26.0, deleted the `toolchain go1.25.14` line and deleted
// the comment explaining why it was there, while the Dockerfile and
// scripts/setup.sh stayed on 1.25.14. Nothing failed, so nothing noticed.
//
// What it cost: the toolchain image bakes a Go that `go` then refuses to
// use, so with GOTOOLCHAIN=auto every fresh CI container downloaded a
// second toolchain and re-exec'd into it -- measured locally at ~7.5s per
// fresh container. A container built to make builds hermetic had
// acquired a per-job network fetch from proxy.golang.org, which is a
// dependency it exists to not have.
//
// The pin cannot simply be restored, which is worth knowing before
// "fixing" this differently: `go mod tidy` deletes a `toolchain` line
// that merely repeats the `go` line, and x/sys v0.48.0 requires go
// 1.26.0, so there is no lower `go` line to pin a newer toolchain
// against. The Dockerfile sets GOTOOLCHAIN=local instead, which makes a
// mismatch a build failure rather than a silent download, and this test
// is what keeps the versions themselves aligned.
//
// It is in package main because main is the one package that sits next
// to the Dockerfile and scripts/, the same reason main owns the docs
// go:embed.

var (
	goModGo       = regexp.MustCompile(`(?m)^go (\S+)$`)
	dockerfileArg = regexp.MustCompile(`(?m)^ARG GO_VERSION=(\S+)$`)
	setupShVar    = regexp.MustCompile(`(?m)^GO_VERSION="\$\{GO_VERSION:-([^}]+)\}"`)
)

func readPin(t *testing.T, path string, re *regexp.Regexp, what string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read %s: %v", path, err)
	}
	m := re.FindSubmatch(b)
	if m == nil {
		t.Fatalf("no %s found in %s -- if it was removed on purpose, CLAUDE.md's "+
			"three-pins gotcha and this test both need updating; if a tool removed it, "+
			"put it back (see turtlemonvh/blanket#185)", what, path)
	}
	return string(m[1])
}

func TestGoVersionPinsAgree(t *testing.T) {
	goDirective := readPin(t, "go.mod", goModGo, "`go X.Y.Z` directive")
	dockerfile := readPin(t, "Dockerfile", dockerfileArg, "`ARG GO_VERSION=`")
	setup := readPin(t, "scripts/setup.sh", setupShVar, "`GO_VERSION=` default")

	// go.mod's `go` directive is the authority: dependencies raise it, and
	// the other two have to follow or the image ships a Go that cannot
	// build this module.
	if dockerfile != goDirective {
		t.Errorf("Dockerfile's ARG GO_VERSION=%s does not match go.mod's `go %s`; "+
			"with GOTOOLCHAIN=local the toolchain image cannot build this module at all",
			dockerfile, goDirective)
	}
	if setup != goDirective {
		t.Errorf("scripts/setup.sh's GO_VERSION=%s does not match go.mod's `go %s`; "+
			"`make setup` would install a Go that cannot build this module",
			setup, goDirective)
	}
}

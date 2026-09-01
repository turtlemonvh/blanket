package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func originOf(t *testing.T, known []KnownTag, tag string) (string, bool) {
	t.Helper()
	for _, k := range known {
		if k.Tag == tag {
			return k.Origin, true
		}
	}
	return "", false
}

func TestResolveKnownTags_BuiltinOnly(t *testing.T) {
	known := ResolveKnownTags(nil, nil, KnownTagsOptions{})
	origin, ok := originOf(t, known, "os:linux")
	assert.True(t, ok, "expected the seed vocabulary to include os:linux")
	assert.Equal(t, OriginBuiltin, origin)
}

func TestResolveKnownTags_NoBuiltinTags(t *testing.T) {
	known := ResolveKnownTags(nil, nil, KnownTagsOptions{NoBuiltinTags: true})
	_, ok := originOf(t, known, "os:linux")
	assert.False(t, ok, "--no-builtin-tags should exclude the seed vocabulary")
}

func TestResolveKnownTags_ObservedFromTaskTypes(t *testing.T) {
	tt, _ := mustLoad(t, `
command = "echo hi"
tags = ["team:platform"]
`)
	known := ResolveKnownTags(nil, []TaskType{*tt}, KnownTagsOptions{NoBuiltinTags: true})
	origin, ok := originOf(t, known, "team:platform")
	assert.True(t, ok)
	assert.Equal(t, OriginObserved, origin)
}

func TestResolveKnownTags_FromKnownTagsConf(t *testing.T) {
	typesDir := t.TempDir()
	blanketDir := filepath.Join(typesDir, ".blanket")
	assert.NoError(t, os.MkdirAll(blanketDir, 0755))
	assert.NoError(t, os.WriteFile(
		filepath.Join(blanketDir, "known-tags.conf"),
		[]byte("# a comment\nteam:data-eng\n\ncost:rnd\n"),
		0644,
	))

	known := ResolveKnownTags([]string{typesDir}, nil, KnownTagsOptions{NoBuiltinTags: true})
	origin, ok := originOf(t, known, "team:data-eng")
	assert.True(t, ok)
	assert.Equal(t, OriginFile, origin)
	_, ok = originOf(t, known, "cost:rnd")
	assert.True(t, ok)
}

func TestResolveKnownTags_FromKnownTagsD(t *testing.T) {
	typesDir := t.TempDir()
	dDir := filepath.Join(typesDir, ".blanket", "known-tags.d")
	assert.NoError(t, os.MkdirAll(dDir, 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(dDir, "mytags.conf"), []byte("team:platform\n"), 0644))
	assert.NoError(t, os.WriteFile(filepath.Join(dDir, "ignored.txt"), []byte("team:should-not-load\n"), 0644))

	known := ResolveKnownTags([]string{typesDir}, nil, KnownTagsOptions{NoBuiltinTags: true})
	_, ok := originOf(t, known, "team:platform")
	assert.True(t, ok)
	_, ok = originOf(t, known, "team:should-not-load")
	assert.False(t, ok, "only *.conf files under known-tags.d should be read")
}

func TestResolveKnownTags_EarliestOriginWins(t *testing.T) {
	// os:linux is in the seed vocabulary (builtin); also declare it in a
	// known-tags.conf file. Builtin, checked first, should win.
	typesDir := t.TempDir()
	blanketDir := filepath.Join(typesDir, ".blanket")
	assert.NoError(t, os.MkdirAll(blanketDir, 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(blanketDir, "known-tags.conf"), []byte("os:linux\n"), 0644))

	known := ResolveKnownTags([]string{typesDir}, nil, KnownTagsOptions{})
	origin, ok := originOf(t, known, "os:linux")
	assert.True(t, ok)
	assert.Equal(t, OriginBuiltin, origin)
}

func TestResolveKnownTags_SortedAndDeduplicated(t *testing.T) {
	known := ResolveKnownTags(nil, nil, KnownTagsOptions{})
	var tags []string
	seen := map[string]bool{}
	for _, k := range known {
		assert.False(t, seen[k.Tag], "tag %q appeared more than once", k.Tag)
		seen[k.Tag] = true
		tags = append(tags, k.Tag)
	}
	sorted := append([]string(nil), tags...)
	assert.True(t, sortedStrings(sorted), "expected output sorted by tag name")
}

// sortedStrings reports whether ss is in non-decreasing lexical order.
func sortedStrings(ss []string) bool {
	for i := 1; i < len(ss); i++ {
		if strings.Compare(ss[i-1], ss[i]) > 0 {
			return false
		}
	}
	return true
}

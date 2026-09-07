package lib

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	cases := map[string]string{
		"~":              home,
		"~/":             home,
		"~/types/*.toml": filepath.Join(home, "types", "*.toml"),
		"/abs/path":      "/abs/path",
		"relative/path":  "relative/path",
		"~someone/else":  "~someone/else",
		"not~/a/home":    "not~/a/home",
		"":               "",
	}
	for in, want := range cases {
		got, err := ExpandHome(in)
		assert.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
}

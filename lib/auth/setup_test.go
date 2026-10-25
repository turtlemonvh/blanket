package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupCodeShapeAndHash(t *testing.T) {
	c, err := NewSetupCode()
	require.NoError(t, err)
	assert.Regexp(t, `^[A-Z2-9]{4}-[A-Z2-9]{4}$`, c)
	assert.Equal(t, HashSetupCode(c), HashSetupCode(strings.ToLower(strings.Replace(c, "-", " ", 1))))
	assert.NotEqual(t, HashSetupCode(c), HashToken(c), "a setup code hash can't be replayed as a token hash")
}

func TestWriteSecretFileReplacesALooserFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), CredentialsFileName)
	require.NoError(t, os.WriteFile(path, []byte("old"), 0644))
	require.NoError(t, WriteSecretFile(path, "blk_new\n"))
	got, err := ReadSecretFile(path)
	require.NoError(t, err)
	assert.Equal(t, "blk_new", got)
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	assert.Len(t, entries, 1, "no temp file left behind")
}

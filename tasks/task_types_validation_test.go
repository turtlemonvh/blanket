package tasks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadTaskTypeFromFilepathForValidation_MissingCommandStillReturnsConfig(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "broken.toml")
	assert.NoError(t, os.WriteFile(fp, []byte(`tags = ["bash"]`), 0644))

	tt, err := ReadTaskTypeFromFilepathForValidation(fp)
	assert.Error(t, err, "missing command should still surface as an error")
	assert.Equal(t, "broken", tt.GetName(), "name should be set even though the file failed to load")
	assert.Equal(t, fp, tt.ConfigFile)
	assert.Equal(t, []string{"bash"}, tt.Config.GetStringSlice("tags"), "other fields should still parse")
}

func TestReadTaskTypeFromFilepathForValidation_CleanFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "ok.toml")
	assert.NoError(t, os.WriteFile(fp, []byte("command = \"echo hi\"\n"), 0644))

	tt, err := ReadTaskTypeFromFilepathForValidation(fp)
	assert.NoError(t, err)
	assert.Equal(t, "ok", tt.GetName())
}

func TestReadTaskTypesForValidation_IncludesBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "good.toml"), []byte("command = \"echo hi\"\n"), 0644))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("tags = [\"bash\"]\n"), 0644))
	// Deliberately not named *toml* anywhere — validConfigfileName is an
	// unanchored regex ((\w*).toml) that would also match e.g.
	// "not-toml.txt" (via the "." matching the "-"). That's a pre-existing
	// quirk shared with ReadTypes(), not something to paper over here.
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "readme.md"), []byte("ignored"), 0644))

	tts, errs := ReadTaskTypesForValidation([]string{dir})
	assert.Len(t, tts, 2, "should include both good.toml and broken.toml, skip readme.md")
	assert.Len(t, errs, 2)

	byName := map[string]error{}
	for i, tt := range tts {
		byName[tt.GetName()] = errs[i]
	}
	assert.NoError(t, byName["good"])
	assert.Error(t, byName["broken"])
}

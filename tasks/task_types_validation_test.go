package tasks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
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
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "readme.md"), []byte("ignored"), 0644))
	// validConfigfileName used to be the unanchored regex `(\w*).toml`,
	// whose "." matched any character — so "not-toml.txt" also matched
	// (via "t" + any-char "-" + literal "toml"). Now anchored to
	// `^\w+\.toml$` against the basename, this must be skipped. See #50.
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "not-toml.txt"), []byte("ignored"), 0644))

	tts, errs := ReadTaskTypesForValidation([]string{dir})
	assert.Len(t, tts, 2, "should include both good.toml and broken.toml, skip readme.md and not-toml.txt")
	assert.Len(t, errs, 2)

	byName := map[string]error{}
	for i, tt := range tts {
		byName[tt.GetName()] = errs[i]
	}
	assert.NoError(t, byName["good"])
	assert.Error(t, byName["broken"])
}

func TestValidConfigfileName(t *testing.T) {
	cases := []struct {
		name  string
		match bool
	}{
		{"foo.toml", true},
		{"echo_task.toml", true},
		{"not-toml.txt", false}, // regression case for #50
		{"foo.toml.bak", false},
		{"footoml", false},
		{".toml", false},
		{"foo.toml ", false},
		{" foo.toml", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.match, validConfigfileName.MatchString(c.name), "MatchString(%q)", c.name)
	}
}

func TestReadTypes_SkipsNonTomlFilesWithTomlSubstring(t *testing.T) {
	dir := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "good.toml"), []byte("command = \"echo hi\"\n"), 0644))
	// Regression case for #50: validConfigfileName used to be the
	// unanchored regex `(\w*).toml`, so "not-toml.txt" also matched (the
	// "." matched the "-" before the literal "toml"). ReadTypes() must
	// skip it.
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "not-toml.txt"), []byte("ignored"), 0644))

	prevPaths := viper.GetStringSlice("tasks.typesPaths")
	viper.Set("tasks.typesPaths", []string{dir})
	defer viper.Set("tasks.typesPaths", prevPaths)

	tts, err := ReadTypes()
	assert.NoError(t, err)
	assert.Len(t, tts, 1, "should only load good.toml")
	assert.Equal(t, "good", tts[0].GetName())
}

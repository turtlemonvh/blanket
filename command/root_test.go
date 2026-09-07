package command

import (
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

// Viper stores defaults in a nested map keyed on ".", so a scalar key and
// a subtree cannot share a name: SetDefault("database.openTimeout", …)
// replaces the *string* at "database" with a map, and the server then
// starts with an empty database path and dies with
// `open : no such file or directory`.
//
// That is exactly what happened while turtlemonvh/blanket#23 phase 4 was
// being written, and it was invisible to every test that passes an
// explicit database path in a config file — which is all of them, since
// the subprocess harness writes one. It only showed up when the Playwright
// suite's server, which relies on the default, refused to start.
//
// Hence `storage.*` for the phase 4 keys, and hence this test: the trap is
// easy to walk back into with the next config key somebody adds under an
// existing scalar.
func TestConfigDefaultsDoNotShadowEachOther(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	SetConfigDefaults()

	assert.Equal(t, "blanket.db", viper.GetString("database"),
		"the database path default must survive every other default; see this test's comment")
	assert.Equal(t, 8773, viper.GetInt("port"))

	assert.Equal(t, 5*time.Second, viper.GetDuration("storage.openTimeout"))
	assert.Equal(t, "", viper.GetString("storage.backupDir"))
	assert.Equal(t, 3, viper.GetInt("storage.backupRetention"))

	// Every scalar default must still read back as a scalar. A subtree
	// added under any of these later would silently blank it, the same way
	// `database` was blanked.
	for key, want := range map[string]string{
		"database":       "blanket.db",
		"timeMultiplier": "1.0",
		"mcp.mode":       "all",
	} {
		assert.Equal(t, want, viper.GetString(key), "scalar default %q", key)
	}
}

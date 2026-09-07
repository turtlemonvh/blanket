package bolt

import (
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

// The open timeout went from a hardcoded 1s to a configurable 5s in
// turtlemonvh/blanket#23 phase 4, because a supervisor restarting the
// server starts the replacement before the old process has finished
// releasing the lock, and a budget shorter than a normal shutdown turns a
// routine restart into a crash loop.
func TestOpenTimeoutDefaultsAndReadsConfig(t *testing.T) {
	t.Cleanup(func() { viper.Set("database.openTimeout", nil) })

	viper.Set("database.openTimeout", nil)
	assert.Equal(t, DefaultOpenTimeout, OpenTimeout())
	assert.GreaterOrEqual(t, DefaultOpenTimeout, 5*time.Second,
		"must comfortably outlast the server's own shutdown drain deadline")

	viper.Set("database.openTimeout", "12s")
	assert.Equal(t, 12*time.Second, OpenTimeout())

	// A nonsensical value falls back rather than collapsing the timeout to
	// zero, which would make every open a coin flip.
	viper.Set("database.openTimeout", "0s")
	assert.Equal(t, DefaultOpenTimeout, OpenTimeout())
}

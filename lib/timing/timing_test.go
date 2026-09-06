package timing

import (
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestScale(t *testing.T) {
	defer viper.Set("timeMultiplier", nil)

	viper.Set("timeMultiplier", 1.0)
	assert.Equal(t, 2*time.Second, Scale(2*time.Second))
	assert.Equal(t, 500*time.Millisecond, ScaleSeconds(0.5))

	viper.Set("timeMultiplier", 0.1)
	assert.Equal(t, 200*time.Millisecond, Scale(2*time.Second))
	assert.Equal(t, 50*time.Millisecond, ScaleSeconds(0.5))

	viper.Set("timeMultiplier", 2.0)
	assert.Equal(t, 4*time.Second, Scale(2*time.Second))
}

// A missing or nonsensical multiplier must fall back to 1.0 rather than
// collapsing every duration to zero, which would turn every timer in the
// system into a hot loop.
func TestScale_DefaultsWhenUnsetOrInvalid(t *testing.T) {
	defer viper.Set("timeMultiplier", nil)

	for _, v := range []interface{}{nil, 0.0, -1.0, "not a number"} {
		viper.Set("timeMultiplier", v)
		assert.Equal(t, DefaultMultiplier, Multiplier(), "value %v", v)
		assert.Equal(t, 2*time.Second, Scale(2*time.Second), "value %v", v)
	}
}

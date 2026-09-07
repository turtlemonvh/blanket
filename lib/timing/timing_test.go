package timing

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestScale(t *testing.T) {
	defer SetMultiplier(DefaultMultiplier)

	SetMultiplier(1.0)
	assert.Equal(t, 2*time.Second, Scale(2*time.Second))
	assert.Equal(t, 500*time.Millisecond, ScaleSeconds(0.5))

	SetMultiplier(0.1)
	assert.Equal(t, 200*time.Millisecond, Scale(2*time.Second))
	assert.Equal(t, 50*time.Millisecond, ScaleSeconds(0.5))

	SetMultiplier(2.0)
	assert.Equal(t, 4*time.Second, Scale(2*time.Second))
}

// A missing or nonsensical multiplier must fall back to 1.0 rather than
// collapsing every duration to zero, which would turn every timer in the
// system into a hot loop.
func TestScale_DefaultsWhenUnsetOrInvalid(t *testing.T) {
	defer SetMultiplier(DefaultMultiplier)

	for _, v := range []float64{0.0, -1.0} {
		SetMultiplier(v)
		assert.Equal(t, DefaultMultiplier, Multiplier(), "value %v", v)
		assert.Equal(t, 2*time.Second, Scale(2*time.Second), "value %v", v)
	}
}

// TestMultiplier_ConcurrentSetAndScale exercises Scale/Multiplier reading
// concurrently with SetMultiplier writing, under -race. This is the
// concurrency shape turtlemonvh/blanket#128 fixed: previously Multiplier
// read viper.GetFloat64 on this same hot path, which raced with any test
// calling viper.Set from another goroutine (e.g. a leftover server's
// scheduler loop still running while the next test configures itself).
// The atomic-backed multiplier here must never trip -race regardless of
// how many goroutines call Scale/SetMultiplier at once.
func TestMultiplier_ConcurrentSetAndScale(t *testing.T) {
	defer SetMultiplier(DefaultMultiplier)

	const goroutines = 8
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Alternate between valid and invalid values to also
				// exercise the DefaultMultiplier fallback under
				// concurrent access.
				if j%2 == 0 {
					SetMultiplier(float64(n+1) * 0.1)
				} else {
					SetMultiplier(0)
				}
			}
		}(i)

		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				d := Scale(time.Second)
				if d <= 0 {
					t.Errorf("Scale returned non-positive duration: %v", d)
				}
				if m := Multiplier(); m <= 0 {
					t.Errorf("Multiplier returned non-positive value: %v", m)
				}
			}
		}()
	}

	wg.Wait()
}

package command

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/httpx"
)

// BLANKET_TOKEN is read once and removed from the environment, so a worker
// (and every task it runs) started from this process never inherits it.
func TestConfigureClientAuthConsumesTheEnvVar(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	viper.Set("port", port)
	viper.Set("database", filepath.Join(t.TempDir(), "blanket.db"))
	t.Cleanup(func() {
		viper.Set("port", nil)
		viper.Set("database", nil)
		httpx.SetBearerTokenSource(0, nil)
	})
	t.Setenv("BLANKET_TOKEN", "blk_from_env")

	configureClientAuth()

	_, still := os.LookupEnv("BLANKET_TOKEN")
	assert.False(t, still, "BLANKET_TOKEN must not survive into child processes")

	res, err := httpx.Client().Get("http://localhost:" + u.Port() + "/")
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, "Bearer blk_from_env", seen)
}

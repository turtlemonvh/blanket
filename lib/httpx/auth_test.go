package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBearerTokenOnlyGoesToLoopbackOnTheServerPort(t *testing.T) {
	t.Cleanup(func() { SetBearerTokenSource(0, nil) })
	SetBearerTokenSource(8773, func() string { return "blk_secret" })

	cases := map[string]string{
		"http://localhost:8773/task/":      "blk_secret",
		"http://127.0.0.1:8773/task/":      "blk_secret",
		"http://[::1]:8773/task/":          "blk_secret",
		"http://localhost:9999/task/":      "",
		"https://api.github.com/repos/x/y": "",
		"http://example.com:8773/task/":    "",
		"http://10.0.0.1:8773/task/":       "",
	}
	for raw, want := range cases {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		assert.Equal(t, want, tokenFor(&http.Request{URL: u}), raw)
	}
}

func TestAuthTransportSetsTheHeaderWithoutTouchingTheCallersRequest(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	t.Cleanup(func() { SetBearerTokenSource(0, nil) })
	SetBearerTokenSource(port, func() string { return "blk_x" })

	req, _ := http.NewRequest("GET", srv.URL, nil)
	res, err := Client().Do(req)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, "Bearer blk_x", seen)
	assert.Empty(t, req.Header.Get("Authorization"), "the caller's request is not modified")

	// An explicit header wins.
	req, _ = http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer other")
	res, err = LongClient().Do(req)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, "Bearer other", seen)

	// No token: no header.
	SetBearerTokenSource(port, func() string { return "" })
	res, err = Client().Get(srv.URL)
	require.NoError(t, err)
	res.Body.Close()
	assert.Empty(t, seen)
}

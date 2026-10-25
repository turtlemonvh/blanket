package httpx

import (
	"net"
	"net/http"
	"strconv"
	"sync"
)

// Bearer-token injection for calls to the local blanket server
// (turtlemonvh/blanket#101).
//
// Every blanket client in this binary -- the CLI, a worker -- reaches the
// server through the shared clients above, so attaching the token here
// covers all of them without threading it through each call site. The
// same clients also fetch release metadata from GitHub (`blanket
// upgrade`, the update notice), so the token is attached only to requests
// addressed to a loopback host on the configured server port. Anything
// else never sees it.

var (
	authMu     sync.RWMutex
	authPort   string
	authSource func() string
)

// SetBearerTokenSource makes the shared clients send
// `Authorization: Bearer <src()>` on requests to a loopback host at port.
// src is called per request, so it can pick up a credential written after
// the process started; an empty result sends nothing. A nil src turns
// injection off.
func SetBearerTokenSource(port int, src func() string) {
	authMu.Lock()
	defer authMu.Unlock()
	authPort = strconv.Itoa(port)
	authSource = src
}

// tokenFor returns the token to attach to req, or "".
func tokenFor(req *http.Request) string {
	authMu.RLock()
	port, src := authPort, authSource
	authMu.RUnlock()
	if src == nil || req.URL == nil || req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return ""
	}
	if req.URL.Port() != port || !isLoopbackHost(req.URL.Hostname()) {
		return ""
	}
	return src()
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

type authTransport struct {
	base http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") == "" {
		if tok := tokenFor(req); tok != "" {
			// RoundTrippers must not modify the caller's request.
			req = req.Clone(req.Context())
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	return t.base.RoundTrip(req)
}

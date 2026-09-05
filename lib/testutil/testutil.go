// Package testutil provides shared scaffolding for tests that need a real
// blanket server backed by in-memory BoltDB instances -- the shape
// server's own tests use (see server/server_test.go), made importable
// from other packages such as worker that need the same thing to exercise
// against (e.g. the claim loop).
//
// server's own internal test files (package server) cannot import this
// package: testutil imports server to build a *server.ServerConfig, so an
// import back from server's internal tests into testutil would be a
// compile-time import cycle ("import cycle not allowed in test"). That's
// why server/server_test.go keeps its own small NewTestServer instead of
// switching to this one; the two are intentionally similar in shape. See
// https://github.com/turtlemonvh/blanket/issues/78.
package testutil

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/server"
)

// NewTestServer returns a *server.ServerConfig backed by fresh in-memory
// BoltDB database and queue instances, plus a cleanup func that releases
// them. ResultsPath defaults to t.TempDir() (removed automatically by the
// testing package); callers that need a specific results directory (e.g.
// worker integration tests that inspect written task output) should
// overwrite it before use.
func NewTestServer(t testing.TB) (*server.ServerConfig, func()) {
	t.Helper()

	DB, DBCloser := bolt.NewTestDB()
	Q, QCloser := bolt.NewTestQueue()

	return &server.ServerConfig{
			DB:           DB,
			Q:            Q,
			ResultsPath:  t.TempDir(),
			Version:      "blanket (test)",
			TaskEvents:   server.NewEventHub(),
			WorkerEvents: server.NewEventHub(),
		}, func() {
			DBCloser()
			QCloser()
		}
}

// NewTestHTTPServer wraps handler in an httptest.Server, for tests (e.g.
// worker's claim-loop integration tests) that need a live HTTP endpoint
// rather than an in-process httptest.Recorder against a router. Returns
// the server and a cleanup func that closes it.
func NewTestHTTPServer(t testing.TB, handler http.Handler) (*httptest.Server, func()) {
	t.Helper()

	srv := httptest.NewServer(handler)
	return srv, srv.Close
}

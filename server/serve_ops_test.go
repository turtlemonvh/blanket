package server

/*

Tests for the ops surface (turtlemonvh/blanket#23 phase 4).

The non-loopback case has to be tested here rather than in
scripts/smoke.sh: the subprocess harness runs everything on localhost, so
the one address it can never produce is a remote one. In a Go handler test
RemoteAddr is just a field.

*/

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
)

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:54321":  true,
		"127.0.0.5:1":      true,
		"[::1]:8080":       true,
		"::1":              true,
		"192.168.1.10:443": false,
		"10.0.0.1:8773":    false,
		"8.8.8.8:80":       false,
		// A hostname rather than an IP, and an empty address, both fail
		// closed: a guard that errs toward refusing costs one confusing
		// message, one that errs toward allowing costs an install.
		"example.com:80": false,
		"":               false,
		"garbage":        false,
	} {
		assert.Equal(t, want, isLoopbackAddr(addr), "isLoopbackAddr(%q)", addr)
	}
}

func opsBackupRequest(t *testing.T, remoteAddr string, withHeader bool, dir string) *http.Request {
	t.Helper()
	url := "/ops/backup"
	if dir != "" {
		url += "?dir=" + dir
	}
	req, err := http.NewRequest("POST", url, nil)
	require.NoError(t, err)
	req.RemoteAddr = remoteAddr
	if withHeader {
		req.Header.Set(OpsHeader, "test")
	}
	return req
}

func TestOpsBackupFromLoopbackWithTheHeaderSucceeds(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	// Something to find in the backup.
	require.NoError(t, s.DB.SaveTask(&tasks.Task{
		Id:        objectid.NewObjectId(),
		TypeId:    "echo_task",
		State:     "WAITING",
		CreatedTs: time.Now().Unix(),
	}))

	dir := filepath.Join(t.TempDir(), "backups")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, opsBackupRequest(t, "127.0.0.1:44444", true, dir))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var payload struct {
		Path          string `json:"path"`
		SchemaVersion int    `json:"schemaVersion"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.Equal(t, database.InitialSchemaVersion, payload.SchemaVersion)
	assert.FileExists(t, payload.Path)

	// The backup is only a backup if it reopens.
	backups, err := bolt.ListBackups(dir)
	require.NoError(t, err)
	require.Len(t, backups, 1)
	assert.Equal(t, payload.Path, backups[0])
}

func TestOpsBackupRefusesNonLoopback(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	dir := filepath.Join(t.TempDir(), "backups")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, opsBackupRequest(t, "203.0.113.7:9999", true, dir))

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "loopback")

	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries, "a refused request must not have taken a backup")
}

// X-Forwarded-For is written by the caller. Trusting it (as gin's
// ClientIP does by default) would let a remote attacker simply claim to be
// local, so the guard reads RemoteAddr and nothing else.
func TestOpsBackupIgnoresASpoofedForwardedForHeader(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	req := opsBackupRequest(t, "203.0.113.7:9999", true, filepath.Join(t.TempDir(), "backups"))
	req.Header.Set("X-Forwarded-For", "127.0.0.1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestOpsBackupRefusesWithoutTheHeader(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, opsBackupRequest(t, "127.0.0.1:44444", false, filepath.Join(t.TempDir(), "backups")))

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), OpsHeader)
}

// The header only helps if the browser is actually forced to preflight and
// the preflight is actually refused. The wildcard CORS handler must not
// answer for /ops/.
func TestOpsIsCarvedOutOfWildcardCORS(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	preflight, err := http.NewRequest("OPTIONS", "/ops/backup", nil)
	require.NoError(t, err)
	preflight.RemoteAddr = "127.0.0.1:44444"
	preflight.Header.Set("Origin", "https://evil.example")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	preflight.Header.Set("Access-Control-Request-Headers", OpsHeader)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, preflight)

	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"),
		"the wildcard CORS handler must not approve an /ops/ preflight")
	assert.NotEqual(t, http.StatusOK, w.Code)

	// …while the rest of the API keeps the permissive policy it had.
	normal, err := http.NewRequest("OPTIONS", "/task/", nil)
	require.NoError(t, err)
	normal.Header.Set("Origin", "https://example.com")
	normal.Header.Set("Access-Control-Request-Method", "POST")

	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, normal)
	assert.Equal(t, "*", w2.Header().Get("Access-Control-Allow-Origin"),
		"phase 4 must not change CORS for anything outside /ops/")
}

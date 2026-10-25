package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/worker"
)

// Tests for the worker log-file paths a caller can influence
// (turtlemonvh/blanket#101 review, M1): POST /worker/ must not take a
// path, a launch must not pass a stored one to the child, and the
// worker-log routes must not serve a stored path the template didn't
// name.

// useWorkerLogTemplate points workers.logfileNameTemplate into dir for
// the length of the test.
func useWorkerLogTemplate(t *testing.T, dir string) {
	t.Helper()
	prev := viper.Get("workers.logfileNameTemplate")
	viper.Set("workers.logfileNameTemplate", filepath.Join(dir, "worker.{{.Id.Hex}}.log"))
	t.Cleanup(func() { viper.Set("workers.logfileNameTemplate", prev) })
}

func TestLaunchNewWorker_RefusesServerOwnedFields(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	victim := filepath.Join(t.TempDir(), "credentials")
	require.NoError(t, os.WriteFile(victim, []byte("blk_secret"), 0600))

	for _, body := range []string{
		`{"tags": ["exec:bash"], "logfile": "` + victim + `"}`,
		`{"tags": ["exec:bash"], "id": "` + objectid.NewObjectId().Hex() + `"}`,
		`{"tags": ["exec:bash"], "pid": 1}`,
		`{"tags": ["exec:bash"], "stopped": true}`,
		`not json`,
	} {
		req, _ := http.NewRequest("POST", "/worker/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code, "body %s: got %s", body, w.Body.String())
	}

	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, "blk_secret", string(got), "the file named by logfile must be untouched")
	ws, err := s.DB.GetWorkers()
	require.NoError(t, err)
	assert.Empty(t, ws, "a refused request must not launch anything")
}

// TestLaunchWorkerAndWait_DropsStoredLogfile: the restart and respawn
// paths hand launchWorkerAndWait the record as stored, which a
// worker-scope caller can write through PUT /worker/:id. The path must
// not reach the child's --logfile.
func TestLaunchWorkerAndWait_DropsStoredLogfile(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	// A too-low interval stops the launch before anything is forked, after
	// the record has been sanitized.
	w := &worker.WorkerConf{Tags: []string{"exec:bash"}, CheckInterval: 0.1, Logfile: "/tmp/not-yours"}
	_, err := s.launchWorkerAndWait(context.Background(), w)
	assert.ErrorIs(t, err, worker.ErrCheckIntervalTooLow)
	assert.Empty(t, w.Logfile)
}

func TestWorkerLogRoutes_ServeOnlyTheTemplatePath(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()
	useWorkerLogTemplate(t, t.TempDir())

	secret := filepath.Join(t.TempDir(), "credentials")
	require.NoError(t, os.WriteFile(secret, []byte("blk_secret\n"), 0600))

	// A worker record pointing at another file, as PUT /worker/:id could
	// store it.
	evil := worker.WorkerConf{Id: objectid.NewObjectId(), Pid: 4242, Logfile: secret}
	require.NoError(t, s.DB.UpdateWorker(&evil))

	// A worker whose log is where the template says.
	good := worker.WorkerConf{Id: objectid.NewObjectId(), Pid: 4243}
	goodLog, err := good.TemplateLogfile()
	require.NoError(t, err)
	good.Logfile = goodLog
	require.NoError(t, os.WriteFile(goodLog, []byte("worker line\n"), 0600))
	require.NoError(t, s.DB.UpdateWorker(&good))

	for _, suffix := range []string{"/logs", "/log/tail"} {
		req, _ := http.NewRequest("GET", "/worker/"+evil.Id.Hex()+suffix, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code, "%s: %s", suffix, w.Body.String())
		assert.NotContains(t, w.Body.String(), "blk_secret", suffix)

		req, _ = http.NewRequest("GET", "/worker/"+good.Id.Hex()+suffix, nil)
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, suffix)
		assert.Contains(t, w.Body.String(), "worker line", suffix)
	}

	// The SSE stream refuses before it starts following anything.
	req, _ := http.NewRequest("GET", "/worker/"+evil.Id.Hex()+"/log", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.NotContains(t, w.Body.String(), "blk_secret")

	// And so does the MCP workers tool's log tail.
	res, _, err := s.mcpWorkers(context.Background(), nil, blanketWorkersArgs{Id: evil.Id.Hex()})
	require.NoError(t, err)
	assert.NotContains(t, res.Content[0].(*mcp.TextContent).Text, "blk_secret")
}

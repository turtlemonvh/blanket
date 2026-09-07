package server

/*

The API half of the pre-0.3.0 compatibility check (turtlemonvh/blanket#87;
asked for on the phase 4 review of #23).

lib/bolt/compat_test.go asserts the *storage* layer reads an old database
unchanged. This one asserts the thing a user would actually notice: after
upgrading, the tasks and workers that were there still show up in the API.
The two are worth having separately — a record can decode perfectly and
still fail to render, which is exactly the class of upgrade bug that gets
reported as "my tasks are gone".

*/

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/bolt"
	bboltlib "go.etcd.io/bbolt"
)

func TestAPIServesAPre030Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blanket.db")
	fixture, err := bolt.WriteLegacyFixtureDatabase(path)
	require.NoError(t, err)

	db, err := bboltlib.Open(path, 0666, nil)
	require.NoError(t, err)
	defer db.Close()

	DB, err := bolt.OpenBlanketBoltDB(db, nil)
	require.NoError(t, err)

	s := &ServerConfig{
		DB:           DB,
		Q:            bolt.NewBlanketBoltQueue(db),
		ResultsPath:  t.TempDir(),
		Version:      "blanket (test)",
		TaskEvents:   NewEventHub(),
		WorkerEvents: NewEventHub(),
	}
	r := s.GetRouter()

	// GET /task/
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/task/", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var tasksOut []map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &tasksOut))
	require.Len(t, tasksOut, 1)
	assert.Equal(t, fixture.TaskId.Hex(), tasksOut[0]["id"])
	assert.Equal(t, "echo_task", tasksOut[0]["type"])
	assert.Equal(t, "WAITING", tasksOut[0]["state"])

	// GET /task/:id
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/task/"+fixture.TaskId.Hex(), nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// GET /worker/
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/worker/", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var workersOut []map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &workersOut))
	require.Len(t, workersOut, 1)
	assert.Equal(t, fixture.WorkerId.Hex(), workersOut[0]["id"])

	// And the tasks list page renders, which is where a missing field
	// would show up as a template error rather than as bad JSON.
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/ui/", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), fixture.TaskId.Hex())
}

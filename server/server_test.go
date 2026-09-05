package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/lib/docs"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestMain seeds the docs package with docs/*.md read straight off disk.
// Production wires this up via main's go:embed instead (see #66 -- go:embed
// can't reach outside its own package directory, so a non-root package like
// this one can't embed ../docs itself); `go test ./server/...` never runs
// main, so tests that exercise the blanket_docs MCP tool need their own FS.
func TestMain(m *testing.M) {
	docs.SetFS(os.DirFS("../docs"))
	os.Exit(m.Run())
}

const testConfig = `
# https://npf.io/2014/08/intro-to-toml/
tags = ["exec:bash", "os:unix"]

# timeout in seconds
timeout = 200

# The command to execute
command='''
{{.DEFAULT_COMMAND}}
'''

executor="bash"


    [[environment.default]]
    name = "ANIMAL"
    value = "giraffe"

    [[environment.default]]
    name = "SECOND_ANIMAL"
    value = "hippo"

    [[environment.default]]
    name = "FROGS"
    value = "3"


    [[environment.required]]
    name = "DEFAULT_COMMAND"
    description = "The bash command to run. E.g. 'echo $(date)'"

`

// NewTestServer returns a server config backed by in-memory BoltDB
// database and queue instances, and a cleanup func to release them.
//
// worker's integration tests need the same thing (see
// worker/worker_test.go), and now get it from lib/testutil.NewTestServer
// instead of hand-rolling it -- see #78. This in-package copy stays: it
// can't be replaced by lib/testutil's version here, since that package
// imports server to build a *ServerConfig, and server's own internal test
// files importing something that imports server back is a compile-time
// import cycle ("import cycle not allowed in test"). Keep the two in sync
// if their shape ever needs to change.
func NewTestServer() (*ServerConfig, func()) {
	DB, DBCloser := bolt.NewTestDB()
	Q, QCloser := bolt.NewTestQueue()

	return &ServerConfig{
			DB:           DB,
			Q:            Q,
			ResultsPath:  "/tmp/x", // FIMXE: Replace with temp dir and cleanup
			Version:      "blanket (test)",
			TaskEvents:   NewEventHub(),
			WorkerEvents: NewEventHub(),
		}, func() {
			defer DBCloser()
			defer QCloser()
		}
}

// Assert that the request object passed generated an empty list json response
func assertResponseLength(t *testing.T, r *gin.Engine, req *http.Request, nitems int) {
	var body []byte
	var err error
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	body, err = ioutil.ReadAll(w.Body)
	assert.Equal(t, nil, err)

	// Read in body as json
	var results []interface{}
	err = json.Unmarshal(body, &results)
	assert.Equal(t, nil, err)

	// Check # records
	assert.Equal(t, nitems, len(results))
}

// Requires turning off firewall on mac
func TestGetTasks(t *testing.T) {
	var err error

	// Run server
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	var req *http.Request

	// Tasks from all sources, DB, and Q
	req, _ = http.NewRequest("GET", "/task/", nil)
	assertResponseLength(t, r, req, 0)
	req, _ = http.NewRequest("GET", "/task/?states=RUNNING", nil)
	assertResponseLength(t, r, req, 0)
	req, _ = http.NewRequest("GET", "/task/?states=WAITING", nil)
	assertResponseLength(t, r, req, 0)

	// Create a task type to the database
	tskt, err := tasks.ReadTaskType(bytes.NewReader([]byte(testConfig)))
	assert.Equal(t, nil, err)

	// Add some tasks
	//var tsks []*tasks.Task
	for i := 0; i < 10; i++ {
		tsk, err := tskt.NewTask(make(map[string]string))
		assert.Equal(t, nil, err)
		//tsks = append(tsks, &tsk)

		err = s.DB.SaveTask(&tsk)
		assert.Equal(t, nil, err)

		err = s.Q.AddTask(&tsk)
		assert.Equal(t, nil, err)
	}

	// Check counts
	req, _ = http.NewRequest("GET", "/task/", nil)
	assertResponseLength(t, r, req, 10)
	req, _ = http.NewRequest("GET", "/task/?states=RUNNING", nil)
	assertResponseLength(t, r, req, 0)
	req, _ = http.NewRequest("GET", "/task/?states=WAITING", nil)
	assertResponseLength(t, r, req, 10)

	// Create a worker so that we can claim tasks for it
	wconf := worker.WorkerConf{
		Id:      objectid.NewObjectId(),
		Tags:    []string{},
		Stopped: false,
	}
	err = s.DB.UpdateWorker(&wconf)
	assert.Equal(t, nil, err)

	// List workers
	req, _ = http.NewRequest("GET", "/worker/", nil)
	assertResponseLength(t, r, req, 1)

	// Move a few tasks forward
	claimUrl := fmt.Sprintf("/task/claim/%s", wconf.Id.Hex())
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req, _ = http.NewRequest("POST", claimUrl, nil)
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// Check counts
	req, _ = http.NewRequest("GET", "/task/", nil)
	assertResponseLength(t, r, req, 10)
	req, _ = http.NewRequest("GET", "/task/?states=RUNNING", nil)
	assertResponseLength(t, r, req, 0)
	req, _ = http.NewRequest("GET", "/task/?states=CLAIMED", nil)
	assertResponseLength(t, r, req, 5)
	req, _ = http.NewRequest("GET", "/task/?states=WAITING", nil)
	assertResponseLength(t, r, req, 5)

}

// TestLaunchWorker_RejectsLowCheckInterval: the JSON API mirror of
// TestUI_SubmitWorker_RejectsLowCheckInterval. Both creation paths must
// reject sub-minimum intervals to keep the claim loop from hot-spinning.
func TestLaunchWorker_RejectsLowCheckInterval(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	body := []byte(`{"tags": ["exec:bash"], "checkInterval": 0.1}`)
	req, _ := http.NewRequest("POST", "/worker/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"sub-minimum checkInterval should return 400; body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "checkInterval")
}

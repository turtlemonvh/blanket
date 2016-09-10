package server

import (
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/bolt"
	"gopkg.in/tylerb/graceful.v1"
	"io/ioutil"
	"net/http"
	"testing"
	"time"
)

const TEST_SERVER_PORT = 6777

// FIXME: Make this a re-usable test utility for use in worker tests
// Returns a server that can be run and killed
// Uses boltdb for backend
func NewTestServer() (*graceful.Server, func()) {
	DB, DBCloser := bolt.NewTestDB()
	Q, QCloser := bolt.NewTestQueue()
	return Serve(DB, Q), func() {
		defer DBCloser()
		defer QCloser()
	}
}

// Assert that the request object passed generated an empty list json response
func assertEmptyListResponse(t *testing.T, req *http.Request) {
	c := http.Client{}
	var body []byte
	var err error
	var resp *http.Response

	resp, err = c.Do(req)
	assert.Equal(t, nil, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = ioutil.ReadAll(resp.Body)
	assert.Equal(t, nil, err)
	assert.Equal(t, "[]\n", string(body))
}

// Requires turning off firewall on mac
func TestGetTasks(t *testing.T) {
	// Run server
	viper.Set("port", TEST_SERVER_PORT)
	S, closefn := NewTestServer()
	defer closefn()
	go S.ListenAndServe()
	defer S.Stop(time.Millisecond * 100)

	var req *http.Request

	// Wait a second for the server to start up
	// FIXME: Make this more robust by checking in a loop
	time.Sleep(time.Second)

	// Tasks from all sources
	req, _ = http.NewRequest("GET", "http://localhost:6777/task", nil)
	assertEmptyListResponse(t, req)

	// Tasks from DB only
	req, _ = http.NewRequest("GET", "http://localhost:6777/task?states=RUNNING", nil)
	assertEmptyListResponse(t, req)

	// Tasks from Q only
	req, _ = http.NewRequest("GET", "http://localhost:6777/task?states=WAITING", nil)
	assertEmptyListResponse(t, req)
}

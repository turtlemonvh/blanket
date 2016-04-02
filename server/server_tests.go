package server

/*

- create Q and DB objects to pass in
- Start server
- tear down server with Stop() function

*/

import (
	_ "fmt"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/queue"
	"gopkg.in/tylerb/graceful.v1"
	"io/ioutil"
	"net/http"
	"testing"
	"time"
)

// Returns a server that can be run and killed
// Uses boltdb for backend
func NewTestServer() (*graceful.Server, func()) {
	DB, DBCloser := database.NewTestDB()
	Q, QCloser := queue.NewTestQueue()
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
	assert.Equal(t, err, nil)
	assert.Equal(t, resp.StatusCode, http.StatusOK)
	body, err = ioutil.ReadAll(resp.Body)
	assert.Equal(t, string(body), "[]")
	assert.Equal(t, err, nil)
}

func TestGetTasks(t *testing.T) {
	// FIXME: Run on random unused port
	// Run server on port 6777
	S, closefn := NewTestServer()
	defer closefn()
	viper.Set("port", 6777)
	go S.ListenAndServe()
	defer S.Stop(time.Millisecond * 100)

	var req *http.Request

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

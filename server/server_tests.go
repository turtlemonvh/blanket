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

func TestGetTasks(t *testing.T) {
	S, closefn := NewTestServer()
	defer closefn()
	viper.Set("port", 6777)
	go S.ListenAndServe()
	defer S.Stop(time.Millisecond * 100)

	c := http.Client{}
	var body []byte
	var err error
	var resp *http.Response
	var req *http.Request

	// Tasks from all sources
	req, err = http.NewRequest("GET", "http://localhost:6777/task", nil)
	assert.Equal(t, err, nil)
	resp, err = c.Do(req)
	assert.Equal(t, err, nil)
	assert.Equal(t, resp.StatusCode, http.StatusOK)
	body, err = ioutil.ReadAll(resp.Body)
	assert.Equal(t, string(body), "[]")
	assert.Equal(t, err, nil)

	// Tasks from DB only
	req, err = http.NewRequest("GET", "http://localhost:6777/task?states=RUNNING", nil)
	assert.Equal(t, err, nil)
	resp, err = c.Do(req)
	assert.Equal(t, err, nil)
	assert.Equal(t, resp.StatusCode, http.StatusOK)
	body, err = ioutil.ReadAll(resp.Body)
	assert.Equal(t, string(body), "[]")
	assert.Equal(t, err, nil)

	// Tasks from Q only
	req, err = http.NewRequest("GET", "http://localhost:6777/task?states=WAITING", nil)
	assert.Equal(t, err, nil)
	resp, err = c.Do(req)
	assert.Equal(t, err, nil)
	assert.Equal(t, resp.StatusCode, http.StatusOK)
	body, err = ioutil.ReadAll(resp.Body)
	assert.Equal(t, string(body), "[]")
	assert.Equal(t, err, nil)
}

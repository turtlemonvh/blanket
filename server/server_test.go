package server

import (
	"bytes"
	"encoding/json"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/queue"
	"github.com/turtlemonvh/blanket/tasks"
	"gopkg.in/tylerb/graceful.v1"
	"io/ioutil"
	"net/http"
	"testing"
	"time"
)

const TEST_SERVER_PORT = 6777

type SystemTestConfig struct {
	db     database.BlanketDB
	q      queue.BlanketQueue
	closer func()
}

const testConfig = `
# https://npf.io/2014/08/intro-to-toml/
tags = ["bash", "unix"]

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

// FIXME: Make this a re-usable test utility for use in worker tests
// Returns a server that can be run and killed, and a config for working with the system
// Uses boltdb for backend
func NewTestServer() (*graceful.Server, SystemTestConfig) {
	DB, DBCloser := bolt.NewTestDB()
	Q, QCloser := bolt.NewTestQueue()

	return Serve(DB, Q), SystemTestConfig{
		db: DB,
		q:  Q,
		closer: func() {
			defer DBCloser()
			defer QCloser()
		},
	}
}

// Assert that the request object passed generated an empty list json response
func assertResponseLength(t *testing.T, req *http.Request, nitems int) {
	c := http.Client{}
	var body []byte
	var err error
	var resp *http.Response

	resp, err = c.Do(req)
	assert.Equal(t, nil, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err = ioutil.ReadAll(resp.Body)
	assert.Equal(t, nil, err)

	// Read in body as json
	var results []interface{}
	err = json.Unmarshal(body, &results)
	assert.Equal(t, nil, err)

	// Check # records
	assert.Equal(t, len(results), nitems)
}

// Requires turning off firewall on mac
func TestGetTasks(t *testing.T) {
	// Run server
	viper.Set("port", TEST_SERVER_PORT)
	S, config := NewTestServer()
	defer config.closer()
	go S.ListenAndServe()
	defer S.Stop(time.Millisecond * 100)

	var req *http.Request

	// Wait a second for the server to start up
	// FIXME: Make this more robust by checking in a loop
	time.Sleep(time.Second)

	// Tasks from all sources
	req, _ = http.NewRequest("GET", "http://localhost:6777/task", nil)
	assertResponseLength(t, req, 0)

	// Tasks from DB only
	req, _ = http.NewRequest("GET", "http://localhost:6777/task?states=RUNNING", nil)
	assertResponseLength(t, req, 0)

	// Tasks from Q only
	req, _ = http.NewRequest("GET", "http://localhost:6777/task?states=WAITING", nil)
	assertResponseLength(t, req, 0)

	// Create a task type to the database
	tskt, err := tasks.ReadTaskType(bytes.NewReader([]byte(testConfig)))
	assert.Equal(t, nil, err)

	// Add some tasks
	for i := 0; i < 100; i++ {
		tsk, err := tskt.NewTask(make(map[string]string))
		assert.Equal(t, nil, err)

		err = config.q.AddTask(&tsk)
		assert.Equal(t, nil, err)
	}

	// Check counts
	req, _ = http.NewRequest("GET", "http://localhost:6777/task", nil)
	assertResponseLength(t, req, 100)
	//req, _ = http.NewRequest("GET", "http://localhost:6777/task?states=RUNNING", nil)
	//assertResponseLength(t, req, 0)
	//req, _ = http.NewRequest("GET", "http://localhost:6777/task?states=WAITING", nil)
	//assertResponseLength(t, req, 100)

}

package worker

import (
	//"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	//"github.com/turtlemonvh/blanket/server"
	//"gopkg.in/tylerb/graceful.v1"
	//"io/ioutil"
	//"net/http"
	"testing"
	//"time"
)

/*
Tests
- normal case
    - submit 2 tasks
    - both finish on time
    - creates result directory
- timeout case
    - submit 2 tasks
    - task 1 runs over time
    - background process kills it
    - task 2 completes successfully
- worker shutdown case
    - worker starts processing task 1
    - gets sigterm
    - stops before processing task 2
- stopped task state
    - worker starts processing task 1
    - we stop task 1 from the api
    - task 1 is put in state stopped, and exits
    - task 2 is processed successfully

- check that worker and server are both producing logs
    - since worker should be polling database

Other considerations
- use accelerated time
- ensure # goroutines is the same at steady state (no leaking of resources)
    - can use metrics api to get this

*/

func TestDefaultCase(t *testing.T) {
	// Process
	// - Add a task type, using test helper method from that package
	// - Start server, using test helper method from that package
	// - Start a worker
	// - Ensure worker processes task, and we get expected result

	// Things to check
	// - task result dir exists
	// - worker is in database
	// - task is in database
	// - task result dir has expected files, with expected contents

	assert.Equal(t, 1, 1)
}

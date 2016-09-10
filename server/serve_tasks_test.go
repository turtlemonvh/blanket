package server

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
- post task with file contents
- post task with json contents
- search for tasks, using all of the different types of flags to filter
- stop task
- delete task
- cancel task, and still try to run it
    - tombstone should be picked up, and should stop
- try to finish a task
    - a valid one
    - one that doesn't exist
    - one that's in the wrong state
- update progress
    - a valid task
    - one that doesn't exist
    - one in the wrong state
- try to get a task that doesn't exist
- claim a task for a worker
- try to claim a task for a worker that doesn't exist

*/

func TestDefaultCase(t *testing.T) {
	assert.Equal(t, 1, 1)
}

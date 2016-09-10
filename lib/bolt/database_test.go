package bolt

import (
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/worker"
	"gopkg.in/mgo.v2/bson"
	"testing"
	"time"
)

func TestWorkers(t *testing.T) {
	DB, closefn := NewTestDB()
	defer closefn()

	var workers []worker.WorkerConf
	var err error

	workers, err = DB.GetWorkers()
	assert.Equal(t, len(workers), 0)
	assert.Equal(t, err, nil)

	// Add some workers
	// Usually workers interact over http, that is done in the worker tests
	w1 := &worker.WorkerConf{
		Id:            bson.NewObjectId(),
		Stopped:       false,
		Pid:           1,
		Tags:          []string{"bash", "unix"},
		StartedTs:     time.Now().Unix(),
		CheckInterval: 0.5,
		Daemon:        false,
	}
	w1.SetLogfileName()
	err = DB.UpdateWorker(w1)
	assert.Equal(t, err, nil)

	w2 := &worker.WorkerConf{
		Id:            bson.NewObjectId(),
		Stopped:       false,
		Pid:           1,
		Tags:          []string{"python", "python27"},
		StartedTs:     time.Now().Unix(),
		CheckInterval: 0.5,
		Daemon:        false,
	}
	w2.SetLogfileName()
	err = DB.UpdateWorker(w2)
	assert.Equal(t, err, nil)

	// Check that we can fetch each worker individually
	w1_fetched, err := DB.GetWorker(w1.Id)
	assert.Equal(t, err, nil)
	assert.Equal(t, w1.StartedTs, w1_fetched.StartedTs)
	assert.Equal(t, w1.Tags, w1_fetched.Tags)

	w2_fetched, err := DB.GetWorker(w2.Id)
	assert.Equal(t, err, nil)
	assert.Equal(t, w2.StartedTs, w2_fetched.StartedTs)
	assert.Equal(t, w2.Tags, w2_fetched.Tags)

	// Check that we see both workers in the database
	workers, err = DB.GetWorkers()
	assert.Equal(t, err, nil)
	assert.Equal(t, len(workers), 2)

	// Check that DeleteWorker with an invalid id does not error, but does not change count
	err = DB.DeleteWorker(bson.NewObjectId())
	assert.Equal(t, err, nil)
	workers, err = DB.GetWorkers()
	assert.Equal(t, err, nil)
	assert.Equal(t, len(workers), 2)

	// Check that DeleteWorker with a valid id is fine
	err = DB.DeleteWorker(w1.Id)
	assert.Equal(t, err, nil)
	workers, err = DB.GetWorkers()
	assert.Equal(t, err, nil)
	assert.Equal(t, len(workers), 1)

	// Trying to fetch by id of deleted item should return error now
	w1_fetched, err = DB.GetWorker(w1.Id)
	assert.NotEqual(t, err, nil)

	// Should return just 1 item
	workers, err = DB.GetWorkers()
	assert.Equal(t, err, nil)
	assert.Equal(t, len(workers), 1)
}

/*
func TestTasks(t *testing.T) {
	DB, closefn := NewTestDB()
	defer closefn()

	// Create task types
	// FIXME: Add a fixture for this

	// Add tasks of each type using tt.NewTask()
}
*/

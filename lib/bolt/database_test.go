package bolt

import (
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/worker"
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
		Id:            objectid.NewObjectId(),
		Stopped:       false,
		Pid:           1,
		Tags:          []string{"exec:bash", "os:unix"},
		StartedTs:     time.Now().Unix(),
		CheckInterval: 0.5,
		Daemon:        false,
	}
	w1.SetLogfileName()
	err = DB.UpdateWorker(w1)
	assert.Equal(t, err, nil)

	w2 := &worker.WorkerConf{
		Id:            objectid.NewObjectId(),
		Stopped:       false,
		Pid:           1,
		Tags:          []string{"runtime:python3", "runtime:python2"},
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
	err = DB.DeleteWorker(objectid.NewObjectId())
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

// TestStopWorker covers the atomic read-modify-write StopWorker helper:
// it should flip Stopped, bump LastHeardTs, persist both, and leave
// unrelated fields (like Tags) untouched.
func TestStopWorker(t *testing.T) {
	DB, closefn := NewTestDB()
	defer closefn()

	w := &worker.WorkerConf{
		Id:      objectid.NewObjectId(),
		Stopped: false,
		Pid:     123,
		Tags:    []string{"exec:bash"},
	}
	assert.NoError(t, DB.UpdateWorker(w))
	assert.Zero(t, w.LastHeardTs)

	before := time.Now().Unix()
	updated, err := DB.StopWorker(w.Id)
	assert.NoError(t, err)
	assert.True(t, updated.Stopped)
	assert.GreaterOrEqual(t, updated.LastHeardTs, before)
	assert.Equal(t, w.Tags, updated.Tags)

	// Persisted, not just returned.
	fetched, err := DB.GetWorker(w.Id)
	assert.NoError(t, err)
	assert.True(t, fetched.Stopped)
	assert.Equal(t, updated.LastHeardTs, fetched.LastHeardTs)
}

// TestStopWorker_UnknownId confirms StopWorker surfaces the same
// not-found error GetWorker would, instead of silently creating a record.
func TestStopWorker_UnknownId(t *testing.T) {
	DB, closefn := NewTestDB()
	defer closefn()

	_, err := DB.StopWorker(objectid.NewObjectId())
	assert.Error(t, err)
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

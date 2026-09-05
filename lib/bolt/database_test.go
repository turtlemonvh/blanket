package bolt

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
	bolt "go.etcd.io/bbolt"
	"io/ioutil"
	"os"
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

// TestGetTask_OldFormatRecordStillLoads writes a task record shaped like
// one saved before turtlemonvh/blanket#61 added ScheduledTs/CronExpr/
// NextFireTs/ParentId (i.e. those keys are simply absent from the JSON,
// as a pre-existing BoltDB record on disk would be) and confirms it still
// unmarshals cleanly, with the new fields defaulting to their zero values.
// This is the additive-schema-change guarantee: an old blanket.db must
// keep working after upgrading past this feature.
func TestGetTask_OldFormatRecordStillLoads(t *testing.T) {
	f, err := ioutil.TempFile("", "")
	assert.NoError(t, err)
	path := f.Name()
	f.Close()
	os.Remove(path)

	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
	assert.NoError(t, err)
	defer db.Close()

	DB := NewBlanketBoltDB(db)

	taskId := objectid.NewObjectId()
	oldFormatJSON := fmt.Sprintf(`{
		"id": %q,
		"pid": 0,
		"createdTs": 1000,
		"startedTs": 0,
		"lastUpdatedTs": 1000,
		"type": "echo_task",
		"resultDir": "/tmp/x",
		"typeDigest": "",
		"timeout": 10,
		"state": "WAITING",
		"workerId": "000000000000000000000000",
		"progress": 0,
		"defaultEnv": {},
		"tags": ["bash"]
	}`, taskId.Hex())

	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_TASK_BUCKET))
		return b.Put(IdBytes(taskId), []byte(oldFormatJSON))
	})
	assert.NoError(t, err)

	task, err := DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, "echo_task", task.TypeId)
	assert.Equal(t, "WAITING", task.State)
	assert.Equal(t, int64(0), task.ScheduledTs)
	assert.Equal(t, "", task.CronExpr)
	assert.Equal(t, int64(0), task.NextFireTs)
	assert.True(t, task.ParentId.IsZero())
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

// TestUpdateWorker_MergesServerOwnedFields is the regression test for the
// live bug UpdateWorker's blind overwrite caused (turtlemonvh/blanket#23
// phase 1): a worker re-registering silently undid a stopWorker, so a
// worker an operator had just stopped carried on claiming tasks.
//
// A worker always registers itself with Stopped=false (see
// WorkerConf.MustRegister), so the merge is what makes the stop stick.
// The worker-owned fields in the same update must still land.
func TestUpdateWorker_MergesServerOwnedFields(t *testing.T) {
	DB, closefn := NewTestDB()
	defer closefn()

	w := &worker.WorkerConf{
		Id:            objectid.NewObjectId(),
		Tags:          []string{"exec:bash"},
		Pid:           100,
		CheckInterval: 2,
	}
	assert.NoError(t, DB.UpdateWorker(w))

	stopped, err := DB.StopWorker(w.Id)
	assert.NoError(t, err)
	assert.True(t, stopped.Stopped)
	assert.NotZero(t, stopped.LastHeardTs)

	// The worker restarts (or just re-registers) and sends its own view of
	// the record, which says Stopped=false and carries a new pid.
	reregister := &worker.WorkerConf{
		Id:            w.Id,
		Tags:          []string{"exec:bash", "os:unix"},
		Pid:           200,
		Logfile:       "worker.200.log",
		StartedTs:     time.Now().Unix(),
		CheckInterval: 3,
		Stopped:       false,
		LastHeardTs:   0,
	}
	assert.NoError(t, DB.UpdateWorker(reregister))

	fetched, err := DB.GetWorker(w.Id)
	assert.NoError(t, err)

	// Server-owned: unchanged by the worker's update.
	assert.True(t, fetched.Stopped, "a worker re-registering must not undo a stopWorker")
	assert.Equal(t, stopped.LastHeardTs, fetched.LastHeardTs)

	// Worker-owned: taken from the update.
	assert.Equal(t, 200, fetched.Pid)
	assert.Equal(t, "worker.200.log", fetched.Logfile)
	assert.Equal(t, []string{"exec:bash", "os:unix"}, fetched.Tags)
	assert.Equal(t, 3.0, fetched.CheckInterval)
	assert.Equal(t, reregister.StartedTs, fetched.StartedTs)

	// And the caller's struct reflects what was actually stored.
	assert.True(t, reregister.Stopped)
}

// TestStartWorker_ClearsStopped covers the server-side un-stop the merge
// makes necessary: since a worker can no longer clear its own Stopped flag
// by re-registering, PUT /worker/:id/restart has to do it explicitly.
func TestStartWorker_ClearsStopped(t *testing.T) {
	DB, closefn := NewTestDB()
	defer closefn()

	w := &worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}}
	assert.NoError(t, DB.UpdateWorker(w))
	_, err := DB.StopWorker(w.Id)
	assert.NoError(t, err)

	started, err := DB.StartWorker(w.Id)
	assert.NoError(t, err)
	assert.False(t, started.Stopped)
	assert.NotZero(t, started.LastHeardTs)

	fetched, err := DB.GetWorker(w.Id)
	assert.NoError(t, err)
	assert.False(t, fetched.Stopped)
}

// TestRunTask_IdempotentAndFenced exercises the RunId fencing token at the
// database layer, where the HTTP handler's status mapping isn't in the way.
func TestRunTask_IdempotentAndFenced(t *testing.T) {
	DB, closefn := NewTestDB()
	defer closefn()

	tsk := &tasks.Task{Id: objectid.NewObjectId(), State: "CLAIMED"}
	assert.NoError(t, DB.SaveTask(tsk))

	assert.NoError(t, DB.RunTask(tsk.Id, &database.TaskRunConfig{RunId: "R1", Pid: 7, Timeout: 10}))

	// Replay of the same transition: accepted, changes nothing.
	assert.NoError(t, DB.RunTask(tsk.Id, &database.TaskRunConfig{RunId: "R1", Pid: 7, Timeout: 10}))

	// A second runner: refused.
	err := DB.RunTask(tsk.Id, &database.TaskRunConfig{RunId: "R2", Pid: 8, Timeout: 10})
	assert.ErrorIs(t, err, database.ErrRunIdMismatch)

	// A legacy worker with no token at all: permitted.
	assert.NoError(t, DB.RunTask(tsk.Id, &database.TaskRunConfig{Pid: 7, Timeout: 10}))

	got, err := DB.GetTask(tsk.Id)
	assert.NoError(t, err)
	assert.Equal(t, "RUNNING", got.State)
	assert.Equal(t, "R1", got.RunId)
	assert.Equal(t, 7, got.Pid)
}

// TestFinishTask_FirstTerminalStateWins pins the rule the worker's retry
// loop depends on: once a task is terminal, a later finish is accepted as a
// no-op instead of rejected, and the state already recorded is kept.
func TestFinishTask_FirstTerminalStateWins(t *testing.T) {
	DB, closefn := NewTestDB()
	defer closefn()

	tsk := &tasks.Task{Id: objectid.NewObjectId(), State: "CLAIMED"}
	assert.NoError(t, DB.SaveTask(tsk))
	assert.NoError(t, DB.RunTask(tsk.Id, &database.TaskRunConfig{RunId: "R1", Timeout: 10}))

	// A user stops it.
	assert.NoError(t, DB.FinishTask(tsk.Id, &database.TaskFinishConfig{State: "STOPPED"}))

	// The worker's killed child then reports ERROR for the same run.
	assert.NoError(t, DB.FinishTask(tsk.Id, &database.TaskFinishConfig{State: "ERROR", RunId: "R1"}))

	got, err := DB.GetTask(tsk.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", got.State)

	// But a different run still can't touch it.
	err = DB.FinishTask(tsk.Id, &database.TaskFinishConfig{State: "SUCCESS", RunId: "R2"})
	assert.ErrorIs(t, err, database.ErrRunIdMismatch)
}

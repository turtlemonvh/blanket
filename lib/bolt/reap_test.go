package bolt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

// The reaper decision table (turtlemonvh/blanket#23 phase 3).
//
// Every row here is a case where getting it wrong destroys real work, so
// each is pinned individually rather than covered by a couple of
// end-to-end runs. The options struct is what makes that cheap: no
// processes are spawned, no journals are written to disk, nothing sleeps,
// and "now" is a constant.
//
// Keep in sync with the table in docs/task_flow.md ("The reaper").

// fakeNow is the fixed clock every test below runs against.
var fakeNow = time.Unix(1700000000, 0)

// liveness is a fake proclive: a pid not in the map answers "inconclusive",
// which is the same thing the real one does when it cannot tell.
type liveness map[int][2]bool // pid -> {alive, conclusive}

func (l liveness) isAlive(pid int, pidStartTs int64) (bool, bool) {
	v, ok := l[pid]
	if !ok {
		return false, false
	}
	return v[0], v[1]
}

// journals is a fake journal reader keyed by result directory.
type journals map[string]*worker.OutcomeJournal

func (j journals) read(resultDir string) (*worker.OutcomeJournal, error) {
	if got, ok := j[resultDir]; ok {
		return got, nil
	}
	return nil, database.ErrNoJournal
}

// reapFixture wires a database, a queue, and a set of fakes together.
type reapFixture struct {
	DB       database.BlanketDB
	Q        interface{ AddTask(*tasks.Task) error }
	live     liveness
	journals journals
	requeued []objectid.ObjectId
	closer   func()
}

func newReapFixture(t *testing.T) *reapFixture {
	t.Helper()
	DB, Q, closer := NewTestDBAndQueue()
	f := &reapFixture{
		DB:       DB,
		Q:        Q,
		live:     liveness{},
		journals: journals{},
		closer:   closer,
	}
	t.Cleanup(closer)
	return f
}

// opts builds the pass inputs. Thresholds are round numbers so the ages in
// each test read as obviously-inside or obviously-outside.
func (f *reapFixture) opts() *database.ReapOptions {
	return &database.ReapOptions{
		Now:              func() time.Time { return fakeNow },
		IsAlive:          f.live.isAlive,
		ReadJournal:      f.journals.read,
		WorkerStaleAfter: 2 * time.Minute,
		WorkerDeadAfter:  10 * time.Minute,
		TaskStaleAfter:   5 * time.Minute,
		MaxRequeues:      3,
		Requeue: func(t *tasks.Task) error {
			f.requeued = append(f.requeued, t.Id)
			return f.Q.AddTask(t)
		},
	}
}

// agoTs is a unix timestamp d before the fake clock.
func agoTs(d time.Duration) int64 { return fakeNow.Add(-d).Unix() }

func (f *reapFixture) addWorker(t *testing.T, w worker.WorkerConf) worker.WorkerConf {
	t.Helper()
	if w.Id.IsZero() {
		w.Id = objectid.NewObjectId()
	}
	require.NoError(t, f.DB.UpdateWorker(&w))
	return w
}

func (f *reapFixture) getWorker(t *testing.T, id objectid.ObjectId) worker.WorkerConf {
	t.Helper()
	w, err := f.DB.GetWorker(id)
	require.NoError(t, err)
	return w
}

func (f *reapFixture) addTask(t *testing.T, task tasks.Task) tasks.Task {
	t.Helper()
	if task.Id.IsZero() {
		task.Id = objectid.NewObjectId()
	}
	if task.ResultDir == "" {
		task.ResultDir = "/results/" + task.Id.Hex()
	}
	require.NoError(t, f.DB.SaveTask(&task))
	return task
}

func (f *reapFixture) getTask(t *testing.T, id objectid.ObjectId) tasks.Task {
	t.Helper()
	task, err := f.DB.GetTask(id)
	require.NoError(t, err)
	return task
}

// --- workers ---

func TestCleanupStalledWorkers_DecisionTable(t *testing.T) {
	const (
		alivePid   = 100
		deadPid    = 200
		unknownPid = 300
	)

	cases := []struct {
		name        string
		worker      worker.WorkerConf
		wantLost    bool
		wantStopped bool
	}{
		{
			name:   "a worker that heartbeated recently is left alone",
			worker: worker.WorkerConf{Pid: deadPid, LastHeardTs: agoTs(30 * time.Second)},
		},
		{
			name: "a live pid with a stale heartbeat is only marked lost",
			// The worker is demonstrably running. Stopping it (and
			// freeing its tasks) could orphan a live child process.
			worker:   worker.WorkerConf{Pid: alivePid, PidStartTs: 5, LastHeardTs: agoTs(5 * time.Minute)},
			wantLost: true,
		},
		{
			name:        "a conclusively dead pid is stopped",
			worker:      worker.WorkerConf{Pid: deadPid, PidStartTs: 5, LastHeardTs: agoTs(5 * time.Minute)},
			wantLost:    true,
			wantStopped: true,
		},
		{
			name: "inconclusive liveness below the longer threshold is only marked lost",
			// This is the same-host fallback: with no usable pid answer,
			// heartbeat silence is all there is, and it has to be much
			// louder before anything happens.
			worker:   worker.WorkerConf{Pid: unknownPid, LastHeardTs: agoTs(5 * time.Minute)},
			wantLost: true,
		},
		{
			name:        "inconclusive liveness past the longer threshold is stopped",
			worker:      worker.WorkerConf{Pid: unknownPid, LastHeardTs: agoTs(30 * time.Minute)},
			wantLost:    true,
			wantStopped: true,
		},
		{
			name:        "an already stopped worker is not touched",
			worker:      worker.WorkerConf{Pid: deadPid, PidStartTs: 5, Stopped: true, LastHeardTs: agoTs(time.Hour)},
			wantStopped: true, // it was already
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReapFixture(t)
			f.live[alivePid] = [2]bool{true, true}
			f.live[deadPid] = [2]bool{false, true}
			// unknownPid is deliberately absent: inconclusive.

			w := f.addWorker(t, tc.worker)

			report, err := f.DB.CleanupStalledWorkers(f.opts())
			require.NoError(t, err)

			got := f.getWorker(t, w.Id)
			assert.Equal(t, tc.wantLost, got.Lost, "lost")
			assert.Equal(t, tc.wantStopped, got.Stopped, "stopped")

			if tc.wantStopped && !tc.worker.Stopped {
				assert.Equal(t, 1, report.WorkersStopped)
				assert.NotEmpty(t, got.StoppedReason, "a reaper stop must say why")
			}
		})
	}
}

// TestCleanupStalledWorkers_BaselineProtectsARestart is grace layer 1. A
// server that has just come up has heard from nobody, and without the
// baseline the very first pass after a restart would mark every worker on
// the machine lost at once.
func TestCleanupStalledWorkers_BaselineProtectsARestart(t *testing.T) {
	f := newReapFixture(t)
	f.live[200] = [2]bool{false, true}

	w := f.addWorker(t, worker.WorkerConf{Pid: 200, PidStartTs: 5, LastHeardTs: agoTs(time.Hour)})

	opts := f.opts()
	// The server started ten seconds ago.
	opts.BaselineTs = agoTs(10 * time.Second)

	report, err := f.DB.CleanupStalledWorkers(opts)
	require.NoError(t, err)
	assert.True(t, report.Empty())
	assert.False(t, f.getWorker(t, w.Id).Lost)

	// Once the window has actually elapsed since the restart, the same
	// worker is judged normally.
	opts.BaselineTs = agoTs(time.Hour)
	_, err = f.DB.CleanupStalledWorkers(opts)
	require.NoError(t, err)
	assert.True(t, f.getWorker(t, w.Id).Stopped)
}

// TestCleanupStalledWorkers_IsIdempotent: a worker already marked lost is
// not rewritten (and not double-counted) on every subsequent pass.
func TestCleanupStalledWorkers_IsIdempotent(t *testing.T) {
	f := newReapFixture(t)
	f.live[100] = [2]bool{true, true}
	f.addWorker(t, worker.WorkerConf{Pid: 100, PidStartTs: 5, LastHeardTs: agoTs(5 * time.Minute)})

	first, err := f.DB.CleanupStalledWorkers(f.opts())
	require.NoError(t, err)
	assert.Equal(t, 1, first.WorkersMarkedLost)

	second, err := f.DB.CleanupStalledWorkers(f.opts())
	require.NoError(t, err)
	assert.True(t, second.Empty(), "a second pass over an unchanged world must do nothing")
}

// TestHeartbeatClearsLost closes the loop with the heartbeat endpoint: a
// worker the reaper gave up on, which then checks in, is no longer lost.
func TestHeartbeatClearsLost(t *testing.T) {
	f := newReapFixture(t)
	f.live[100] = [2]bool{true, true}
	w := f.addWorker(t, worker.WorkerConf{Pid: 100, PidStartTs: 5, LastHeardTs: agoTs(5 * time.Minute)})

	_, err := f.DB.CleanupStalledWorkers(f.opts())
	require.NoError(t, err)
	require.True(t, f.getWorker(t, w.Id).Lost)

	_, err = f.DB.HeartbeatWorker(w.Id)
	require.NoError(t, err)
	assert.False(t, f.getWorker(t, w.Id).Lost)
}

// --- tasks ---

func TestCleanupStalledTasks_RecoversFromTheJournal(t *testing.T) {
	cases := []struct {
		name         string
		journalState string
		exitCode     int
		wantState    string
		wantExitCode *int
	}{
		{
			name:         "a clean exit becomes SUCCESS",
			journalState: worker.OutcomeStateExited,
			exitCode:     0,
			wantState:    "SUCCESS",
			wantExitCode: intPtr(0),
		},
		{
			name:         "a non-zero exit becomes ERROR, carrying the real code",
			journalState: worker.OutcomeStateExited,
			exitCode:     7,
			wantState:    "ERROR",
			wantExitCode: intPtr(7),
		},
		{
			// The journal writes -1 for "killed by a signal"; the task
			// record spells the same thing as a null exitCode, because a
			// task's exitCode has to keep "unknown" distinguishable from a
			// genuine exit 0.
			name:         "a signal death becomes ERROR with no exit code",
			journalState: worker.OutcomeStateExited,
			exitCode:     -1,
			wantState:    "ERROR",
			wantExitCode: nil,
		},
		{
			// The worker died between the server's ack and the unlink.
			// The outcome is still known, so recording it is not a guess.
			name:         "a reported journal is applied too",
			journalState: worker.OutcomeStateReported,
			exitCode:     0,
			wantState:    "SUCCESS",
			wantExitCode: intPtr(0),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReapFixture(t)
			task := f.addTask(t, tasks.Task{
				State:         "RUNNING",
				RunId:         "RUN1",
				Pid:           4242,
				LastUpdatedTs: agoTs(30 * time.Minute),
			})
			f.journals[task.ResultDir] = &worker.OutcomeJournal{
				State:    tc.journalState,
				RunId:    "RUN1",
				Pid:      4242,
				ExitCode: tc.exitCode,
			}

			report, err := f.DB.CleanupStalledTasks(f.opts())
			require.NoError(t, err)
			assert.Equal(t, 1, report.TasksRecovered)

			got := f.getTask(t, task.Id)
			assert.Equal(t, tc.wantState, got.State)
			if tc.wantExitCode == nil {
				assert.Nil(t, got.ExitCode)
			} else if assert.NotNil(t, got.ExitCode) {
				assert.Equal(t, *tc.wantExitCode, *got.ExitCode)
			}
		})
	}
}

func TestCleanupStalledTasks_LeavesTasksAloneWithoutEvidence(t *testing.T) {
	const (
		liveChild = 100
		deadChild = 200
	)

	cases := []struct {
		name    string
		task    tasks.Task
		journal *worker.OutcomeJournal
	}{
		{
			// The headline rule: a task's child can outlive its worker.
			name:    "a running journal with a live child",
			task:    tasks.Task{State: "RUNNING", RunId: "RUN1", Pid: liveChild, LastUpdatedTs: agoTs(time.Hour)},
			journal: &worker.OutcomeJournal{State: worker.OutcomeStateRunning, RunId: "RUN1", Pid: liveChild, PidStartTs: 5},
		},
		{
			// The child died without recording an outcome, so there is no
			// outcome to recover. Anything written here would be invented.
			name:    "a running journal with a dead child",
			task:    tasks.Task{State: "RUNNING", RunId: "RUN1", Pid: deadChild, LastUpdatedTs: agoTs(time.Hour)},
			journal: &worker.OutcomeJournal{State: worker.OutcomeStateRunning, RunId: "RUN1", Pid: deadChild, PidStartTs: 5},
		},
		{
			// The journal describes an earlier attempt, so it says nothing
			// about this one.
			name:    "a journal from a different run",
			task:    tasks.Task{State: "RUNNING", RunId: "RUN2", Pid: deadChild, LastUpdatedTs: agoTs(time.Hour)},
			journal: &worker.OutcomeJournal{State: worker.OutcomeStateExited, RunId: "RUN1", ExitCode: 0},
		},
		{
			// The journal write failed open (a full disk, a read-only
			// results dir). Without it there is nothing to go on.
			name: "a running task with no journal at all",
			task: tasks.Task{State: "RUNNING", RunId: "RUN1", Pid: deadChild, LastUpdatedTs: agoTs(time.Hour)},
		},
		{
			// Claimed, but the task recorded a pid, so it may well have
			// started; requeueing could duplicate side effects.
			name: "a claimed task that recorded a pid",
			task: tasks.Task{State: "CLAIMED", Pid: deadChild, LastUpdatedTs: agoTs(time.Hour)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReapFixture(t)
			f.live[liveChild] = [2]bool{true, true}
			f.live[deadChild] = [2]bool{false, true}

			task := f.addTask(t, tc.task)
			if tc.journal != nil {
				f.journals[task.ResultDir] = tc.journal
			}

			report, err := f.DB.CleanupStalledTasks(f.opts())
			require.NoError(t, err)
			assert.Equal(t, 1, report.LeftAlone)
			assert.Zero(t, report.TasksRecovered)
			assert.Zero(t, report.TasksRequeued)

			got := f.getTask(t, task.Id)
			assert.Equal(t, tc.task.State, got.State, "the task must be exactly as it was")
		})
	}
}

func TestCleanupStalledTasks_FreshTasksAreNotExamined(t *testing.T) {
	f := newReapFixture(t)
	task := f.addTask(t, tasks.Task{State: "RUNNING", RunId: "RUN1", LastUpdatedTs: agoTs(time.Minute)})
	f.journals[task.ResultDir] = &worker.OutcomeJournal{
		State: worker.OutcomeStateExited, RunId: "RUN1", ExitCode: 0,
	}

	report, err := f.DB.CleanupStalledTasks(f.opts())
	require.NoError(t, err)
	assert.True(t, report.Empty(), "a task updated a minute ago is not stale")
	assert.Equal(t, "RUNNING", f.getTask(t, task.Id).State)
}

func TestCleanupStalledTasks_RequeuesTasksThatNeverStarted(t *testing.T) {
	f := newReapFixture(t)
	f.live[200] = [2]bool{false, true}
	w := f.addWorker(t, worker.WorkerConf{Pid: 200, PidStartTs: 5, LastHeardTs: agoTs(time.Hour)})

	task := f.addTask(t, tasks.Task{
		State:         "CLAIMED",
		WorkerId:      w.Id,
		RunId:         "RUN1",
		LastUpdatedTs: agoTs(time.Hour),
	})

	opts := f.opts()
	report, err := f.DB.CleanupStalledTasks(opts)
	require.NoError(t, err)
	assert.Equal(t, 1, report.TasksRequeued)
	assert.Equal(t, []objectid.ObjectId{task.Id}, f.requeued)

	got := f.getTask(t, task.Id)
	assert.Equal(t, "WAITING", got.State)
	assert.True(t, got.WorkerId.IsZero(), "the dead worker's claim is released")
	assert.Equal(t, 1, got.RequeueCount)
	assert.Empty(t, got.RunId, "a fresh attempt gets a fresh fencing token")
}

func TestCleanupStalledTasks_RequeuesWhenTheWorkerRecordIsGone(t *testing.T) {
	f := newReapFixture(t)
	task := f.addTask(t, tasks.Task{
		State:         "CLAIMED",
		WorkerId:      objectid.NewObjectId(), // never registered / deleted
		LastUpdatedTs: agoTs(time.Hour),
	})

	report, err := f.DB.CleanupStalledTasks(f.opts())
	require.NoError(t, err)
	assert.Equal(t, 1, report.TasksRequeued)
	assert.Equal(t, "WAITING", f.getTask(t, task.Id).State)
}

func TestCleanupStalledTasks_LeavesClaimsOfLiveWorkersAlone(t *testing.T) {
	f := newReapFixture(t)
	f.live[100] = [2]bool{true, true}
	w := f.addWorker(t, worker.WorkerConf{Pid: 100, PidStartTs: 5, LastHeardTs: agoTs(time.Hour)})

	task := f.addTask(t, tasks.Task{
		State:         "CLAIMED",
		WorkerId:      w.Id,
		LastUpdatedTs: agoTs(time.Hour),
	})

	report, err := f.DB.CleanupStalledTasks(f.opts())
	require.NoError(t, err)
	assert.Zero(t, report.TasksRequeued)
	assert.Equal(t, "CLAIMED", f.getTask(t, task.Id).State)
}

// TestCleanupStalledTasks_RequeueCapFailsAPoisonTask: a task that keeps
// killing whichever worker claims it is failed rather than requeued
// forever. Without the cap it would take out every worker in turn, and the
// only trace would be the requeues themselves.
func TestCleanupStalledTasks_RequeueCapFailsAPoisonTask(t *testing.T) {
	f := newReapFixture(t)
	f.live[200] = [2]bool{false, true}
	w := f.addWorker(t, worker.WorkerConf{Pid: 200, PidStartTs: 5, LastHeardTs: agoTs(time.Hour)})

	task := f.addTask(t, tasks.Task{
		State:         "CLAIMED",
		WorkerId:      w.Id,
		RequeueCount:  3, // == MaxRequeues
		LastUpdatedTs: agoTs(time.Hour),
	})

	report, err := f.DB.CleanupStalledTasks(f.opts())
	require.NoError(t, err)
	assert.Equal(t, 1, report.TasksFailed)
	assert.Zero(t, report.TasksRequeued)
	assert.Equal(t, "ERROR", f.getTask(t, task.Id).State)
}

// --- the queue ---

func TestCleanupUnclaimedTasks_DecisionTable(t *testing.T) {
	cases := []struct {
		name string
		// state of the task in the *database*; "" means no record at all
		dbState     string
		queuedAge   time.Duration
		claimed     bool
		wantDropped bool
		wantFreed   bool
	}{
		{
			// The claim landed and only the ack was lost. Re-running would
			// duplicate work that may already have had side effects.
			name:        "the task is CLAIMED in the database",
			dbState:     "CLAIMED",
			queuedAge:   time.Hour,
			claimed:     true,
			wantDropped: true,
		},
		{
			name:        "the task is RUNNING in the database",
			dbState:     "RUNNING",
			queuedAge:   time.Hour,
			claimed:     true,
			wantDropped: true,
		},
		{
			// The claim never completed, so nobody is working on it.
			name:      "the task is still WAITING in the database",
			dbState:   "WAITING",
			queuedAge: time.Hour,
			claimed:   true,
			wantFreed: true,
		},
		{
			name:      "the task has no database record at all",
			dbState:   "",
			queuedAge: time.Hour,
			claimed:   true,
			wantFreed: true,
		},
		{
			name:      "a recently claimed entry is left alone",
			dbState:   "WAITING",
			queuedAge: time.Minute,
			claimed:   true,
		},
		{
			name:      "an unclaimed entry is left alone",
			dbState:   "WAITING",
			queuedAge: time.Hour,
			claimed:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			DB, Q, closer := NewTestDBAndQueue()
			defer closer()

			id := objectid.NewObjectId()
			queued := tasks.Task{
				Id:            id,
				TypeId:        "echo_task",
				State:         "WAITING",
				LastUpdatedTs: agoTs(tc.queuedAge),
			}
			if tc.claimed {
				queued.WorkerId = objectid.NewObjectId()
			}
			require.NoError(t, Q.AddTask(&queued))

			if tc.dbState != "" {
				stored := queued
				stored.State = tc.dbState
				require.NoError(t, DB.SaveTask(&stored))
			}

			opts := &database.ReapOptions{
				Now:            func() time.Time { return fakeNow },
				TaskStaleAfter: 5 * time.Minute,
			}
			report, err := Q.CleanupUnclaimedTasks(opts)
			require.NoError(t, err)

			assert.Equal(t, tc.wantDropped, report.QueueEntriesDropped == 1, "dropped")
			assert.Equal(t, tc.wantFreed, report.QueueEntriesFreed == 1, "freed")

			// A freed entry must be claimable again; a dropped one must be
			// gone from the queue entirely.
			claimant := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{}}
			claimedTask, _, _, err := Q.ClaimTask(&claimant)
			switch {
			case tc.wantDropped:
				assert.Error(t, err, "the queue should be empty")
			case tc.wantFreed:
				require.NoError(t, err)
				assert.Equal(t, id, claimedTask.Id)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

package worker

import (
	"encoding/json"
	"os"
	"path"
)

// The outcome journal (turtlemonvh/blanket#23 phase 1).
//
// A worker writes `<ResultDir>/blanket.outcome.json` for the task it is
// running, so that the real outcome of a run survives the worker
// process — a crash, an OOM kill, a machine losing power mid-task. The
// file is written when the child process starts, overwritten when it
// exits, and deleted once the server has acknowledged the finish.
//
// The worker never reads it back. Its consumer is the reaper added in
// phase 3, which uses it to recover what actually happened to a task whose
// worker vanished, instead of guessing from the task record alone.
// Misclassifying a finished three-hour render as ERROR is precisely the
// failure docs/design.md says not to have. That one-way relationship is
// what keeps the two decoupled: nothing in the running path depends on the
// journal being readable, so every write here fails open.
//
// Schema is documented for that consumer in docs/task_flow.md ("Outcome
// journal"). Keep the two in sync.

const (
	// OutcomeJournalFilename is the journal's name inside a task's result
	// directory, alongside blanket.stdout.log / blanket.stderr.log.
	OutcomeJournalFilename = "blanket.outcome.json"

	// OutcomeJournalVersion is bumped if the schema below changes
	// incompatibly, so a reaper reading a file written by an older worker
	// can tell.
	OutcomeJournalVersion = 1

	// Journal states. The state field exists so a file left behind by a
	// crash is self-explanatory rather than requiring the reader to infer
	// intent from which fields happen to be populated.
	//
	//   running  — the child was started; nothing is known about its
	//              outcome yet. Found later, this means the worker died
	//              while the task was in flight.
	//   exited   — the child exited and ExitCode/ExitedTs are meaningful,
	//              but the server has not acknowledged the finish. Found
	//              later, this is the case worth recovering: the outcome is
	//              known and simply never got reported.
	//   reported — the server acknowledged the finish and the file is about
	//              to be deleted. Found later, it means the worker died in
	//              the window between the ack and the unlink; there is
	//              nothing to recover and the file can just be removed.
	OutcomeStateRunning  = "running"
	OutcomeStateExited   = "exited"
	OutcomeStateReported = "reported"
)

// OutcomeJournal is the on-disk record of one execution attempt.
type OutcomeJournal struct {
	Version int    `json:"version"`
	State   string `json:"state"`

	// RunId is the fencing token for this attempt (tasks.Task.RunId). It's
	// what lets a reaper tell whether the journal describes the run the
	// task record is currently about, or a stale earlier attempt.
	RunId    string `json:"runId"`
	TaskId   string `json:"taskId"`
	WorkerId string `json:"workerId"`

	// Pid is the child process's pid. PidStartTs is its start time, used to
	// guard against pid reuse when checking liveness; it stays 0 until
	// phase 3 adds the per-platform `proclive` package that can read it —
	// there's no cheap portable way to get it here today. A zero
	// PidStartTs means "unknown", and a reader must treat liveness as
	// inconclusive rather than assuming the pid is this process.
	Pid        int   `json:"pid"`
	PidStartTs int64 `json:"pidStartTs"`

	StartedTs int64 `json:"startedTs"`
	ExitedTs  int64 `json:"exitedTs"`

	// ExitCode is the child's exit status once State is "exited": 0 for
	// success, the process's code for a non-zero exit, and -1 when it was
	// killed by a signal (a timeout kill, or a user cancel).
	ExitCode int `json:"exitCode"`
}

// OutcomeJournalPath returns the journal path for a task result directory.
func OutcomeJournalPath(resultDir string) string {
	return path.Join(resultDir, OutcomeJournalFilename)
}

// WriteOutcomeJournal writes j atomically: to a temp file in the same
// directory, fsync'd, then renamed over the destination, then the directory
// itself fsync'd.
//
// The whole point of the journal is to be readable after a crash, so a
// half-written or not-yet-flushed file would defeat it. Rename within a
// directory is atomic on POSIX and on Windows (os.Rename uses
// MoveFileEx with MOVEFILE_REPLACE_EXISTING), so a reader sees either the
// previous contents or the new ones, never a mix.
func WriteOutcomeJournal(resultDir string, j *OutcomeJournal) error {
	j.Version = OutcomeJournalVersion

	bts, err := json.Marshal(j)
	if err != nil {
		return err
	}

	dst := OutcomeJournalPath(resultDir)
	tmp, err := os.CreateTemp(resultDir, OutcomeJournalFilename+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(bts); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}

	// Fsync the directory so the rename itself is durable. Not all
	// platforms allow opening a directory for this (Windows doesn't), so a
	// failure here is not treated as a write failure — the file contents
	// are already synced.
	if d, err := os.Open(resultDir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// ReadOutcomeJournal loads the journal for a result directory. Returns
// os.ErrNotExist (wrapped) if there is none, which is the normal case for a
// task that finished cleanly. Provided for the phase 3 reaper; the worker
// itself never calls it.
func ReadOutcomeJournal(resultDir string) (*OutcomeJournal, error) {
	bts, err := os.ReadFile(OutcomeJournalPath(resultDir))
	if err != nil {
		return nil, err
	}
	j := &OutcomeJournal{}
	if err := json.Unmarshal(bts, j); err != nil {
		return nil, err
	}
	return j, nil
}

// RemoveOutcomeJournal deletes the journal. A missing file is not an error:
// the only caller deletes it after a successful finish, and a retried
// delete must stay harmless.
func RemoveOutcomeJournal(resultDir string) error {
	err := os.Remove(OutcomeJournalPath(resultDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

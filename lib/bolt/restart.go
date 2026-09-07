package bolt

/*

The restart state machine's storage (turtlemonvh/blanket#23 phase 5).

Everything here exists to satisfy one invariant, stated in
lib/database/meta.go and worth repeating where it is implemented:

	every transition is a single transaction, and two facts that must
	agree are never written in two of them.

bolt makes that cheap — the `meta` bucket and the `workers` bucket live in
the same file, so one `db.Update` can touch both — and the cost of not
doing it is not theoretical. The drain writes "a restart is at DRAINING"
and "these six workers are stopped and are to be brought back". Split
across two transactions, a crash in between leaves either six workers
stopped that nothing will ever restart, or a promise to restart six
workers that are still happily claiming tasks. Neither is recoverable
from the outside, because nothing on disk records which of the two
happened.

The respawn bookkeeping (ClaimWorkerRespawn / ClearWorkerRespawn) is the
same shape one worker at a time. The ordering there is deliberately
*not* symmetric: the claim — clear Stopped, count the attempt, stamp the
clock — commits before the spawn, and the intent is cleared only after
the spawn succeeded. That makes respawn at-least-once. See
worker.WorkerConf.RespawnIntent for why that is the direction to fail in.

*/

import (
	"encoding/json"
	"time"

	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/worker"
	bolt "go.etcd.io/bbolt"
)

// readRestartRecord is the in-transaction form of RestartRecord. A missing
// key decodes to the zero value, whose State is "" — which
// database.RestartStateRank reads as IDLE, so an old database and a
// settled one are indistinguishable, as intended.
func readRestartRecord(tx *bolt.Tx) (database.RestartRecord, error) {
	var rr database.RestartRecord
	_, err := getMetaJSON(tx, MetaKeyRestartRecord, &rr)
	return rr, err
}

// writeRestartRecord stores rr, or deletes the key when rr has settled
// back to IDLE.
//
// Deleting rather than storing `{"state":"IDLE"}` keeps "is a restart in
// flight?" answerable by the presence of the key, which is the form the
// question takes for anything reading the file without this package's
// help.
func writeRestartRecord(tx *bolt.Tx, rr database.RestartRecord) error {
	if !rr.Active() {
		return deleteMeta(tx, MetaKeyRestartRecord)
	}
	return putMetaJSON(tx, MetaKeyRestartRecord, rr)
}

// UpdateRestartRecord applies fn to the stored record and writes the
// result back, all inside one transaction. This is the only way a state
// transition is made; see the file header.
//
// fn returning an error aborts the transaction, so a rejected transition
// (out of order, or a deadline that has already passed) leaves the record
// exactly as it was rather than half-applied.
func (DB *BlanketBoltDB) UpdateRestartRecord(fn func(*database.RestartRecord) error) (database.RestartRecord, error) {
	var out database.RestartRecord
	err := DB.db.Update(func(tx *bolt.Tx) error {
		rr, err := readRestartRecord(tx)
		if err != nil {
			return err
		}
		if err := fn(&rr); err != nil {
			return err
		}
		rr.UpdatedTs = time.Now().Unix()
		out = rr
		return writeRestartRecord(tx, rr)
	})
	return out, err
}

// StopWorkersForRestart is the transaction the whole invariant is about:
// it applies fn to the restart record *and* stops every worker that is
// still running, recording a respawn intent on each, in one commit.
//
// Returns the updated record and the workers it stopped. The caller may
// then signal those processes — outside the transaction, because sending a
// signal is not something a database write can be made to agree with, and
// the record is already correct whether or not the signal lands.
//
// A worker that is already stopped is left entirely alone, respawn intent
// included. If it was stopped before the restart began, bringing it back
// afterwards would be the restart inventing a worker the operator had
// deliberately taken down.
func (DB *BlanketBoltDB) StopWorkersForRestart(reason string, fn func(*database.RestartRecord) error) (database.RestartRecord, []worker.WorkerConf, error) {
	var out database.RestartRecord
	var stopped []worker.WorkerConf

	err := DB.db.Update(func(tx *bolt.Tx) error {
		// Reset per attempt: bolt retries nothing, but a caller reusing
		// this method on a handle that returned an error should not see
		// leftovers from the failed attempt.
		stopped = nil

		rr, err := readRestartRecord(tx)
		if err != nil {
			return err
		}
		if err := fn(&rr); err != nil {
			return err
		}

		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return MakeBucketDNEError(BOLTDB_WORKER_BUCKET)
		}

		now := time.Now().Unix()
		cur := b.Cursor()
		for k, v := cur.First(); k != nil; k, v = cur.Next() {
			var w worker.WorkerConf
			if err := json.Unmarshal(v, &w); err != nil {
				return err
			}
			if w.Stopped {
				continue
			}
			w.Stopped = true
			w.StoppedReason = reason
			w.LastHeardTs = now
			w.RespawnIntent = true
			// Attempts belong to the respawn, not to the stop: a fresh
			// drain gives every worker its full generation budget again.
			w.RespawnAttempts = 0

			bts, merr := json.Marshal(&w)
			if merr != nil {
				return merr
			}
			if err := b.Put(k, bts); err != nil {
				return err
			}
			stopped = append(stopped, w)
		}

		rr.DrainedWorkers = len(stopped)
		rr.UpdatedTs = now
		out = rr
		return writeRestartRecord(tx, rr)
	})
	if err != nil {
		return database.RestartRecord{}, nil, err
	}
	return out, stopped, nil
}

// ClaimWorkerRespawn takes ownership of one worker's respawn intent
// immediately before the server spawns it: Stopped is cleared, the attempt
// is counted, and the clock is stamped — one transaction, before the fork.
//
// Counting *before* the attempt rather than after is what makes the
// generation cap survive the failure it exists for. A worker whose spawn
// kills the server every time would otherwise never have its counter
// incremented, and every boot would try again forever.
//
// Returns database.ItemNotFoundError if the worker is gone, and leaves the
// record untouched (returning it unchanged) if the intent has since been
// cleared — an operator stop that landed between the decision and the
// claim wins, which is the point of routing an explicit stop through
// StopWorker with no reason.
func (DB *BlanketBoltDB) ClaimWorkerRespawn(workerId objectid.ObjectId) (worker.WorkerConf, error) {
	return ModifyWorkerInBoltTransaction(DB.db, &workerId, func(w *worker.WorkerConf) error {
		if !w.RespawnIntent {
			return nil
		}
		w.Stopped = false
		w.StoppedReason = ""
		w.Lost = false
		w.RespawnAttempts++
		w.LastRespawnTs = time.Now().Unix()
		w.LastHeardTs = time.Now().Unix()
		// The recorded pid belongs to a process that is gone (the caller
		// checked before claiming). Clearing it is not tidiness: the
		// server waits for the new process by polling this record for a
		// nonzero Pid, and a stale one would make that wait return
		// instantly and declare a spawn successful that had not happened.
		w.Pid = 0
		w.PidStartTs = 0
		return nil
	})
}

// ClearWorkerRespawn drops a worker's respawn intent, with an optional
// reason recorded on the way out.
//
// Two callers, and the reason tells them apart in the record afterwards:
// a spawn that succeeded (empty reason — the worker is running and owes
// nobody an explanation) and a decision not to try any more (the
// generation cap, whose whole value is that the operator can see why the
// worker did not come back).
func (DB *BlanketBoltDB) ClearWorkerRespawn(workerId objectid.ObjectId, reason string) (worker.WorkerConf, error) {
	return ModifyWorkerInBoltTransaction(DB.db, &workerId, func(w *worker.WorkerConf) error {
		w.RespawnIntent = false
		w.RespawnAttempts = 0
		if reason != "" {
			// Only set alongside a stop: the reason for *not* respawning
			// is only meaningful on a worker that is staying down.
			w.Stopped = true
			w.StoppedReason = reason
		}
		return nil
	})
}

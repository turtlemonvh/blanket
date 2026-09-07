package server

/*

The two meta-bucket writes the server lifecycle owns
(turtlemonvh/blanket#23 phase 4): stamping this process's identity at
startup, and clearing the lock-holder record on a clean shutdown.

The second one is the load-bearing half. A record found on the next open
means "the previous blanket did not get to shut down", and that inference
is only sound if the clean path really does clear it.

*/

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/lib/database"
	bboltlib "go.etcd.io/bbolt"
)

func TestServePersistsTheServerInstance(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	stored, err := s.DB.ServerInstance()
	require.NoError(t, err)
	assert.Equal(t, database.ServerInstance{}, stored, "nothing persisted before Serve")

	bs := s.Serve()
	bs.stopTailers = func() {}
	bs.closeStorage = func() {}
	defer bs.Shutdown(context.Background())

	stored, err = s.DB.ServerInstance()
	require.NoError(t, err)
	assert.Equal(t, s.InstanceId(), stored.InstanceId)
	assert.Equal(t, s.StartedTs(), stored.StartedTs)
	assert.NotEmpty(t, stored.InstanceId)
}

func TestShutdownClearsTheLockHolderRecord(t *testing.T) {
	// Built over a real file through the full open sequence, because that
	// is what stamps the lock holder in the first place — NewTestServer's
	// fixture deliberately skips it (see bolt.NewBlanketBoltDB).
	path := filepath.Join(t.TempDir(), "blanket.db")
	db, err := bboltlib.Open(path, 0666, nil)
	require.NoError(t, err)
	defer db.Close()

	DB, err := bolt.OpenBlanketBoltDB(db, nil)
	require.NoError(t, err)

	s := &ServerConfig{
		DB:           DB,
		Q:            bolt.NewBlanketBoltQueue(db),
		ResultsPath:  t.TempDir(),
		Version:      "blanket (test)",
		TaskEvents:   NewEventHub(),
		WorkerEvents: NewEventHub(),
	}

	held, err := s.DB.LockHolder()
	require.NoError(t, err)
	require.NotZero(t, held.Pid)

	bs := s.Serve()
	bs.stopTailers = func() {}
	bs.stopLoops = func() {}
	bs.closeStorage = func() {}
	require.NoError(t, bs.Shutdown(context.Background()))

	cleared, err := s.DB.LockHolder()
	require.NoError(t, err)
	assert.Zero(t, cleared.Pid, "a clean shutdown must clear the lock holder; a record left behind means a crash")
}

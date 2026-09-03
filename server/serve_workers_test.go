package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/worker"
)

func TestLaunchWorkerAndWait_RejectsLowCheckInterval(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := &worker.WorkerConf{Tags: []string{"exec:bash"}, CheckInterval: 0.1}
	_, err := s.launchWorkerAndWait(context.Background(), w)
	assert.ErrorIs(t, err, worker.ErrCheckIntervalTooLow)
}

func TestStopWorkerById(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	err := s.stopWorkerById(context.Background(), w.Id)
	assert.NoError(t, err)

	updated, err := s.DB.GetWorker(w.Id)
	assert.NoError(t, err)
	assert.True(t, updated.Stopped)
}

func TestDeleteWorkerById(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}, Stopped: true}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	err := s.deleteWorkerById(context.Background(), w.Id)
	assert.NoError(t, err)

	_, err = s.DB.GetWorker(w.Id)
	assert.Error(t, err)
}

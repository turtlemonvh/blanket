package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/worker"
)

func TestLaunchWorkerAndWait_RejectsLowCheckInterval(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := &worker.WorkerConf{Tags: []string{"exec:bash"}, CheckInterval: 0.1}
	_, err := s.launchWorkerAndWait(context.Background(), w)
	assert.ErrorIs(t, err, worker.ErrCheckIntervalTooLow)
}

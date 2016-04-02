package database

import (
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/worker"
	"testing"
)

func TestSaveRetrieve(t *testing.T) {
	DB, closefn := NewTestDB()
	defer closefn()

	var workers []worker.WorkerConf
	var err error

	workers, err = DB.GetWorkers()
	assert.Equal(t, len(workers), 0)
	assert.Equal(t, err, nil)
}

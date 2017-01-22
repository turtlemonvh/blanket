package bolt

import (
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/tasks"
	"testing"
)

func TestSaveRetrieve(t *testing.T) {
	//Q, closefn := NewTestQueue()
	//defer closefn()

	var foundTasks []tasks.Task
	var nfound int
	var err error

	// FIXME: Removed ListTasks
	//foundTasks, nfound, err = Q.ListTasks([]string{}, 100)
	assert.Equal(t, len(foundTasks), 0)
	assert.Equal(t, nfound, 0)
	assert.Equal(t, err, nil)
}

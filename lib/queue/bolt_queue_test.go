package queue

import (
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/tasks"
	"io/ioutil"
	"os"
	"testing"
	"time"
)

func NewTestQueue() (BlanketQueue, func()) {
	// Retrieve a temporary path.
	f, err := ioutil.TempFile("", "")
	if err != nil {
		panic(fmt.Sprintf("temp file: %s", err))
	}
	path := f.Name()
	f.Close()
	os.Remove(path)

	// Open the database.
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		panic(fmt.Sprintf("open: %s", err))
	}

	Q := NewBlanketBoltQueue(db)
	return Q, func() {
		db.Close()
	}
}

func TestSaveRetrieve(t *testing.T) {
	Q, closefn := NewTestQueue()
	defer closefn()

	var foundTasks []tasks.Task
	var nfound int
	var err error

	foundTasks, nfound, err = Q.ListTasks([]string{}, 100)
	assert.Equal(t, len(foundTasks), 0)
	assert.Equal(t, nfound, 0)
	assert.Equal(t, err, nil)
}

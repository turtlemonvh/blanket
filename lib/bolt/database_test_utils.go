package bolt

import (
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/turtlemonvh/blanket/lib/database"
	"io/ioutil"
	"os"
	"time"
)

func NewTestDB() (database.BlanketDB, func()) {
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

	DB := NewBlanketBoltDB(db)
	return DB, func() {
		db.Close()
	}
}

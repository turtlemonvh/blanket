package bolt

import (
	"bytes"
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/boltdb/bolt"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/tasks"
	"gopkg.in/mgo.v2/bson"
	"time"
)

var (
	BucketDNEError error
)

func MakeBucketDNEError(bucketName string) error {
	return fmt.Errorf("Database format error: Bucket '%s' does not exist.", bucketName)
}

// https://blog.golang.org/error-handling-and-go
func MustOpenBoltDatabase() *bolt.DB {
	db, err := bolt.Open(viper.GetString("database"), 0666, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	return db
}

// TASKS

func fetchTaskBucket(tx *bolt.Tx) (b *bolt.Bucket, err error) {
	b = tx.Bucket([]byte(BOLTDB_TASK_BUCKET))
	if b == nil {
		err = MakeBucketDNEError(BOLTDB_TASK_BUCKET)
	}
	return
}

func fetchTaskFromBucket(taskId *bson.ObjectId, b *bolt.Bucket) (t tasks.Task, err error) {
	result := b.Get(IdBytes(*taskId))
	if result == nil {
		err = database.NotFoundError(fmt.Sprintf("No item for id %v", taskId))
		return
	}
	err = json.Unmarshal(result, &t)
	return
}

// Returns a list of tasks, the number found, and any error
// FIXME: Move FindTasksInBoltDB and ModifyTaskInBoltTransaction to their own helper library
// FIXME: Return task objects in a slice instead of a string; may actually want to send on a channel for streaming
func FindTasksInBoltDB(db *bolt.DB, bucketName string, tc *database.TaskSearchConf) ([]tasks.Task, int, error) {
	var err error

	result := []tasks.Task{}
	nfound := 0
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return MakeBucketDNEError(bucketName)
		}

		c := b.Cursor()

		// Sort order
		var (
			checkFunction func(bts []byte) bool
			k             []byte
			v             []byte
			iterFunction  func() ([]byte, []byte)
			endBytes      []byte
		)
		if tc.ReverseSort {
			// Have to just jump to the end, since seeking to a far future key goes to the end
			// Seek only goes in 1 order
			// Seek manually to the highest value
			for k, v = c.Last(); k != nil && bytes.Compare(k, IdBytes(tc.LargestId)) >= 0; k, v = c.Prev() {
				continue
			}
			iterFunction = c.Prev
			endBytes = IdBytes(tc.SmallestId)
			checkFunction = func(bts []byte) bool {
				return k != nil && bytes.Compare(k, endBytes) >= 0
			}
		} else {
			// Normal case
			k, v = c.Seek(IdBytes(tc.SmallestId))
			iterFunction = c.Next
			endBytes = IdBytes(tc.LargestId)
			checkFunction = func(bts []byte) bool {
				return k != nil && bytes.Compare(k, endBytes) <= 0
			}
		}

		for ; checkFunction(k); k, v = iterFunction() {
			// e.g. 50-40 == 10
			if nfound-tc.Offset == tc.Limit {
				break
			}

			// Create an object from bytes
			t := tasks.Task{}
			json.Unmarshal(v, &t)

			// Filter results
			if tc.JustUnclaimed && t.WorkerId.Valid() {
				continue
			}

			if len(tc.AllowedTaskTypes) != 0 && !tc.AllowedTaskTypes[t.TypeId] {
				continue
			}
			if len(tc.AllowedTaskStates) != 0 && !tc.AllowedTaskStates[t.State] {
				continue
			}

			// All tags in tc.requiredTags must be present on every task
			if len(tc.RequiredTags) > 0 {
				hasTags := true
				for _, requestedTag := range tc.RequiredTags {
					found := false
					for _, existingTag := range t.Tags {
						if requestedTag == existingTag {
							found = true
						}
					}
					if !found {
						hasTags = false
						break
					}
				}
				if !hasTags {
					continue
				}
			}

			// All tags on each task must be present in tc.maxTags
			if len(tc.MaxTags) > 0 {
				taskHasExtraTags := false
				for _, existingTag := range t.Tags {
					found := false
					for _, allowedTag := range tc.MaxTags {
						if allowedTag == existingTag {
							found = true
						}
					}
					if !found {
						taskHasExtraTags = true
						break
					}
				}
				if taskHasExtraTags {
					continue
				}
			}

			// Keep track of found items, and build string that will be returned
			nfound += 1
			if nfound > tc.Offset {
				if !tc.JustCounts {
					result = append(result, t)
				}
			}
		}

		return nil
	})

	return result, nfound, err
}

func saveTaskToBucket(t *tasks.Task, b *bolt.Bucket) (err error) {
	bts, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return b.Put(IdBytes(t.Id), bts)
}

func ModifyTaskInBoltTransaction(db *bolt.DB, taskId *bson.ObjectId, f func(t *tasks.Task) error) error {
	err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := fetchTaskBucket(tx)
		if err != nil {
			return err
		}
		t, err := fetchTaskFromBucket(taskId, bucket)
		if err != nil {
			return err
		}

		// Main function; accepts a task object and can perform checks and modify it
		err = f(&t)
		if err != nil {
			return err
		} else {
			t.LastUpdatedTs = time.Now().Unix()
		}

		return saveTaskToBucket(&t, bucket)
	})
	return err
}

package bolt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

// TestMaxTags_EnforceOnlyWhenAsked: an empty MaxTags means "no filter"
// for list and search queries, and "untagged tasks only" when
// EnforceMaxTags is set, which is how ClaimTask applies the superset rule
// to a tagless worker (turtlemonvh/blanket#259).
func TestMaxTags_EnforceOnlyWhenAsked(t *testing.T) {
	db, closer := newTestBoltFile()
	defer closer()
	DB := NewBlanketBoltDB(db)

	untagged := tasks.Task{Id: objectid.NewObjectId(), State: "WAITING"}
	tagged := tasks.Task{Id: objectid.NewObjectId(), State: "WAITING", Tags: []string{"a"}}
	both := tasks.Task{Id: objectid.NewObjectId(), State: "WAITING", Tags: []string{"a", "b"}}
	for _, tsk := range []*tasks.Task{&untagged, &tagged, &both} {
		require.NoError(t, DB.SaveTask(tsk))
	}

	ids := func(tc *database.TaskSearchConf) []objectid.ObjectId {
		tc.Limit = 100
		tc.SmallestId = objectid.NewObjectIdWithTime(time.Unix(0, 0))
		tc.LargestId = objectid.NewObjectIdWithTime(time.Unix(database.FAR_FUTURE_SECONDS, 0))
		found, _, err := FindTasksInBoltDB(db, BOLTDB_TASK_BUCKET, tc)
		require.NoError(t, err)
		out := []objectid.ObjectId{}
		for _, f := range found {
			out = append(out, f.Id)
		}
		return out
	}

	assert.ElementsMatch(t, []objectid.ObjectId{untagged.Id, tagged.Id, both.Id},
		ids(&database.TaskSearchConf{}), "no MaxTags: no filter")
	assert.ElementsMatch(t, []objectid.ObjectId{untagged.Id, tagged.Id, both.Id},
		ids(&database.TaskSearchConf{MaxTags: []string{}}), "empty MaxTags: no filter")
	assert.ElementsMatch(t, []objectid.ObjectId{untagged.Id},
		ids(&database.TaskSearchConf{EnforceMaxTags: true}), "enforced, no tags: untagged only")
	assert.ElementsMatch(t, []objectid.ObjectId{untagged.Id},
		ids(&database.TaskSearchConf{EnforceMaxTags: true, MaxTags: []string{""}}), "enforced, [\"\"]: untagged only")
	assert.ElementsMatch(t, []objectid.ObjectId{untagged.Id, tagged.Id},
		ids(&database.TaskSearchConf{MaxTags: []string{"a"}}), "MaxTags [a], unchanged")
	assert.ElementsMatch(t, []objectid.ObjectId{untagged.Id, tagged.Id},
		ids(&database.TaskSearchConf{EnforceMaxTags: true, MaxTags: []string{"a"}}), "enforced [a], same as unenforced")
}

// TestClaimTask_TaglessWorker claims through the queue directly, for each
// shape a tagless worker's tags can take.
func TestClaimTask_TaglessWorker(t *testing.T) {
	for name, tags := range map[string][]string{
		"nil":          nil,
		"empty":        {},
		"empty string": {""},
	} {
		t.Run(name, func(t *testing.T) {
			_, Q, closer := NewTestDBAndQueue()
			defer closer()

			tagged := tasks.Task{Id: objectid.NewObjectId(), State: "WAITING", Tags: []string{"a"}}
			untagged := tasks.Task{Id: objectid.NewObjectId(), State: "WAITING"}
			require.NoError(t, Q.AddTask(&tagged))
			require.NoError(t, Q.AddTask(&untagged))

			w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: tags}
			got, _, _, err := Q.ClaimTask(&w)
			require.NoError(t, err)
			assert.Equal(t, untagged.Id, got.Id)

			_, _, _, err = Q.ClaimTask(&w)
			assert.Error(t, err, "a tagless worker must not claim a tagged task")
		})
	}
}

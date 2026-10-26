package server

// A worker claims a task only if the worker's tags are a superset of the
// task's, so a worker with no tags claims only untagged tasks
// (turtlemonvh/blanket#259). These tests launch a tagless worker through
// each path -- the CLI, POST /worker/, the UI form and
// blanket_launch_worker -- and check what it claims through the real
// claim handler.
//
// The REST, UI and MCP paths fork the binary, which under `go test` is
// the test binary, so startDaemonFn stands in for the fork. It does what
// the daemonized child does: receive the tags as a joined --tags string,
// parse them the way `blanket worker` does, and register.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

const untaggedTypeToml = `
command = "echo untagged"
executor = "bash"
`

const taggedTypeToml = `
tags = ["exec:bash"]
command = "echo tagged"
executor = "bash"
`

const extraTaggedTypeToml = `
tags = ["exec:bash", "gpu"]
command = "echo gpu"
executor = "bash"
`

// queueTaskFromToml saves and enqueues one task of the given inline type.
func queueTaskFromToml(t *testing.T, s *ServerConfig, toml string) tasks.Task {
	t.Helper()
	tt, err := tasks.ReadTaskType(strings.NewReader(toml))
	require.NoError(t, err)
	tsk, err := tt.NewTask(map[string]string{})
	require.NoError(t, err)
	require.NoError(t, s.DB.SaveTask(&tsk))
	require.NoError(t, s.Q.AddTask(&tsk))
	return tsk
}

// claimFor runs POST /task/claim/:id and returns the claimed task id, or
// the zero id on 204.
func claimFor(t *testing.T, r http.Handler, id objectid.ObjectId) objectid.ObjectId {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", fmt.Sprintf("/task/claim/%s", id.Hex()), nil))
	if w.Code == http.StatusNoContent {
		var none objectid.ObjectId
		return none
	}
	require.Equal(t, http.StatusOK, w.Code, "claim body: %s", w.Body.String())
	var got tasks.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	return got.Id
}

// fakeDaemon installs a startDaemonFn that registers the worker the way
// the forked `blanket worker --tags <joined>` child would, and records the
// ids it registered.
func fakeDaemon(s *ServerConfig) *[]objectid.ObjectId {
	var ids []objectid.ObjectId
	s.startDaemonFn = func(w *worker.WorkerConf) error {
		child := worker.WorkerConf{
			Id:            w.Id,
			Tags:          worker.ParseTags(strings.Join(w.Tags, ",")),
			Pid:           4242,
			CheckInterval: w.CheckInterval,
		}
		if child.Id.IsZero() {
			child.Id = objectid.NewObjectId()
			w.Id = child.Id
		}
		ids = append(ids, child.Id)
		return s.DB.UpdateWorker(&child)
	}
	return &ids
}

// assertClaimsOnlyUntagged queues one untagged and one tagged task, and
// checks the worker claims the untagged one and then nothing.
func assertClaimsOnlyUntagged(t *testing.T, s *ServerConfig, r http.Handler, workerId objectid.ObjectId) {
	t.Helper()
	untagged := queueTaskFromToml(t, s, untaggedTypeToml)
	tagged := queueTaskFromToml(t, s, taggedTypeToml)

	assert.Equal(t, untagged.Id, claimFor(t, r, workerId), "tagless worker should claim the untagged task")
	assert.True(t, claimFor(t, r, workerId).IsZero(), "tagless worker must not claim a tagged task")

	still, err := s.DB.GetTask(tagged.Id)
	require.NoError(t, err)
	assert.Equal(t, "WAITING", still.State)
}

func TestUntaggedWorker_CLI(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	// `blanket worker` with no -t: the flag's default is "".
	tags := worker.ParseTags("")
	assert.Equal(t, []string{}, tags, "an empty -t must parse to no tags, not [\"\"]")

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: tags, Pid: 4242}
	require.NoError(t, s.DB.UpdateWorker(&w))
	assertClaimsOnlyUntagged(t, s, r, w.Id)
}

// TestUntaggedWorker_LegacyEmptyStringTag: workers registered before the
// CLI fix carry [""], and must behave the same as an empty set.
func TestUntaggedWorker_LegacyEmptyStringTag(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{""}, Pid: 4242}
	require.NoError(t, s.DB.UpdateWorker(&w))
	assertClaimsOnlyUntagged(t, s, r, w.Id)
}

func TestUntaggedWorker_REST(t *testing.T) {
	for name, body := range map[string]string{
		"no tags field": `{}`,
		"empty tags":    `{"tags": []}`,
		"blank tag":     `{"tags": ["", " "]}`,
	} {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.ReleaseMode)
			s, cleanup := NewTestServer()
			defer cleanup()
			r := s.GetRouter()
			fakeDaemon(s)

			req := httptest.NewRequest("POST", "/worker/", bytes.NewReader([]byte(body)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

			var registered worker.WorkerConf
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &registered))
			assert.Empty(t, registered.Tags)
			assertClaimsOnlyUntagged(t, s, r, registered.Id)
		})
	}
}

func TestUntaggedWorker_UIForm(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()
	ids := fakeDaemon(s)

	form := url.Values{"tags": {" , "}}
	req := httptest.NewRequest("POST", "/ui/workers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Len(t, *ids, 1, "status %d body: %s", w.Code, w.Body.String())

	assertClaimsOnlyUntagged(t, s, r, (*ids)[0])
}

func TestUntaggedWorker_MCP(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()
	ids := fakeDaemon(s)

	_, _, err := s.mcpLaunchWorker(context.Background(), nil, blanketLaunchWorkerArgs{})
	require.NoError(t, err, "blanket_launch_worker with no tags starts an untagged-only worker")
	require.Len(t, *ids, 1)

	registered, err := s.DB.GetWorker((*ids)[0])
	require.NoError(t, err)
	assert.Empty(t, registered.Tags)
	assertClaimsOnlyUntagged(t, s, r, registered.Id)
}

// TestTaggedWorker_SupersetRuleUnchanged: a tagged worker still claims
// untagged tasks and tasks whose tags it covers, and skips a task with a
// tag it lacks.
func TestTaggedWorker_SupersetRuleUnchanged(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	untagged := queueTaskFromToml(t, s, untaggedTypeToml)
	tagged := queueTaskFromToml(t, s, taggedTypeToml)
	extra := queueTaskFromToml(t, s, extraTaggedTypeToml)

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}, Pid: 4242}
	require.NoError(t, s.DB.UpdateWorker(&w))

	claimed := map[objectid.ObjectId]bool{}
	for i := 0; i < 2; i++ {
		claimed[claimFor(t, r, w.Id)] = true
	}
	assert.True(t, claimed[untagged.Id], "tagged worker claims untagged tasks")
	assert.True(t, claimed[tagged.Id], "tagged worker claims tasks its tags cover")
	assert.True(t, claimFor(t, r, w.Id).IsZero(), "tagged worker must not claim a task with a tag it lacks")

	still, err := s.DB.GetTask(extra.Id)
	require.NoError(t, err)
	assert.Equal(t, "WAITING", still.State)
}

// TestListTasks_NoTagFilterReturnsAll: the claim path's enforcement must
// not leak into listing, where an absent or empty maxTags means no filter.
func TestListTasks_NoTagFilterReturnsAll(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	queueTaskFromToml(t, s, untaggedTypeToml)
	queueTaskFromToml(t, s, taggedTypeToml)
	queueTaskFromToml(t, s, extraTaggedTypeToml)

	for _, path := range []string{"/task/", "/task/?maxTags=", "/task/?maxTags=,"} {
		req := httptest.NewRequest("GET", path, nil)
		assertResponseLength(t, r, req, 3)
	}
	req := httptest.NewRequest("GET", "/task/?maxTags=exec:bash", nil)
	assertResponseLength(t, r, req, 2)
}

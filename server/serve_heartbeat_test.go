package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/worker"
)

// Heartbeat handler tests (turtlemonvh/blanket#23 phase 3).
//
// The property worth defending is that LastHeardTs is the *server's*
// reading of the clock. Everything the reaper does is arithmetic on that
// number, and it deletes and rewrites state on the result, so a
// worker-supplied timestamp would turn clock skew (or a malicious client)
// into a reaper that never fires, or one that fires on healthy workers.

func registerTestWorker(t *testing.T, r http.Handler, w worker.WorkerConf) {
	t.Helper()
	bts, err := json.Marshal(&w)
	require.NoError(t, err)
	req, _ := http.NewRequest("PUT", "/worker/"+w.Id.Hex(), strings.NewReader(string(bts)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func heartbeat(t *testing.T, r http.Handler, id objectid.ObjectId) (*httptest.ResponseRecorder, worker.HeartbeatResponse) {
	t.Helper()
	req, _ := http.NewRequest("PUT", "/worker/"+id.Hex()+"/heartbeat", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var hb worker.HeartbeatResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &hb), rec.Body.String())
	}
	return rec, hb
}

func TestHeartbeat_ServerClockWins(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	id := objectid.NewObjectId()
	// A worker claiming an absurd lastHeardTs: far in the future, which
	// would make it look permanently fresh to the reaper if it were
	// believed.
	registerTestWorker(t, r, worker.WorkerConf{
		Id:          id,
		Pid:         4242,
		LastHeardTs: time.Now().Add(400 * 24 * time.Hour).Unix(),
	})

	before := time.Now().Unix()
	rec, hb := heartbeat(t, r, id)
	after := time.Now().Unix()
	require.Equal(t, http.StatusOK, rec.Code)

	stored, err := s.DB.GetWorker(id)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, stored.LastHeardTs, before)
	assert.LessOrEqual(t, stored.LastHeardTs, after,
		"lastHeardTs must come from the server's clock, not the worker's claim")
	assert.Equal(t, stored.LastHeardTs, hb.LastHeardTs, "the response echoes what was stored")
}

func TestHeartbeat_CarriesStoppedAndInstanceId(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	id := objectid.NewObjectId()
	registerTestWorker(t, r, worker.WorkerConf{Id: id, Pid: 11})

	_, hb := heartbeat(t, r, id)
	assert.False(t, hb.Stopped)
	assert.Equal(t, s.InstanceId(), hb.ServerInstanceId)
	assert.NotEmpty(t, hb.ServerInstanceId, "a worker has to be able to tell one server process from another")
	assert.Equal(t, s.StartedTs(), hb.ServerStartedTs)

	// A drain must be visible on the very next heartbeat -- that is what
	// keeps drain latency to one check interval.
	req, _ := http.NewRequest("PUT", "/worker/"+id.Hex()+"/stop", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	_, hb = heartbeat(t, r, id)
	assert.True(t, hb.Stopped)
}

func TestHeartbeat_InstanceIdIsStableAcrossCalls(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	id := objectid.NewObjectId()
	registerTestWorker(t, r, worker.WorkerConf{Id: id})

	_, first := heartbeat(t, r, id)
	_, second := heartbeat(t, r, id)
	assert.Equal(t, first.ServerInstanceId, second.ServerInstanceId,
		"the instance id identifies the process; a change means a restart")

	// A different server process gets a different id, which is the signal
	// a worker keys off.
	other, otherCleanup := NewTestServer()
	defer otherCleanup()
	assert.NotEqual(t, s.InstanceId(), other.InstanceId())
}

func TestHeartbeat_UnknownWorkerIs404(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	// Deleting a worker whose process is still winding down is a normal
	// operator action; the stray heartbeats that follow are not server
	// errors.
	rec, _ := heartbeat(t, r, objectid.NewObjectId())
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestUpdateWorker_KeepsServerOwnedFields is the phase-1 merge, extended
// with the fields phase 3 adds. A worker re-registering (which it always
// does with stopped:false) must not undo a stop, erase the reaper's note,
// or roll lastHeardTs back to whatever it last saw -- while the fields it
// genuinely owns, including the new pidStartTs, must land.
func TestUpdateWorker_KeepsServerOwnedFields(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	id := objectid.NewObjectId()
	registerTestWorker(t, r, worker.WorkerConf{Id: id, Pid: 1, PidStartTs: 100})

	// Server-side state the worker knows nothing about. Set through the
	// server's own atomic transitions rather than by writing a struct:
	// UpdateWorker deliberately refuses to take server-owned fields from
	// its caller, which is the property under test.
	stopped, err := s.DB.StopWorker(id, "")
	require.NoError(t, err)
	require.True(t, stopped.Stopped)
	require.NotZero(t, stopped.LastHeardTs)

	// The worker re-registers with its own view of the world: a fresh pid,
	// stopped:false (which MustRegister always sends), and a lastHeardTs
	// of its own invention.
	registerTestWorker(t, r, worker.WorkerConf{
		Id: id, Pid: 2, PidStartTs: 200, Stopped: false, LastHeardTs: 1,
	})

	after, err := s.DB.GetWorker(id)
	require.NoError(t, err)
	assert.True(t, after.Stopped, "a re-registration must not un-stop a worker")
	assert.Equal(t, stopped.LastHeardTs, after.LastHeardTs, "lastHeardTs is the server's to write")
	assert.Equal(t, 2, after.Pid, "pid is the worker's own")
	assert.Equal(t, int64(200), after.PidStartTs, "pidStartTs is the worker's own")
	assert.False(t, after.Lost, "a worker that re-registers is evidence of life")
}

func TestConfigEndpoint_ExposesInstanceIdentity(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	req, _ := http.NewRequest("GET", "/config/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var conf map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &conf))
	assert.Equal(t, s.InstanceId(), conf["instanceId"])
	assert.Equal(t, fmt.Sprintf("%d", s.StartedTs()),
		fmt.Sprintf("%.0f", conf["serverStartedTs"].(float64)))
}

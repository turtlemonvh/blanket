package worker

import (
	"context"
	"encoding/json"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/httpx"
)

// The heartbeat (turtlemonvh/blanket#23 phase 3).
//
// Before this, a worker had no liveness signal at all: `docs/task_flow.md`
// documented a lastHeardTs field that nothing ever wrote, and the server
// could not tell a worker that was idle from one that had been killed
// hours ago. The reaper needs that distinction to exist before it can act
// on anything.
//
// It is one PUT per claim-loop iteration, on the worker's own
// CheckInterval, and it does two jobs beyond "I am alive":
//
//   - the response carries `stopped`, so a drain lands within one check
//     interval rather than waiting on some other poll;
//   - it carries the server's instance id, so a worker notices that the
//     server restarted underneath it — the process it registered with is
//     gone, and anything it believed about its own state was established
//     against that process. Today that is logged; phase 5 acts on it.
//
// The call goes through lib/httpx like every other worker→server call, so
// it inherits the timeouts and the full-jitter retry from phase 1. Its
// retry budget is short on purpose: a heartbeat that arrives late is worth
// less than the next one, and the loop will send another shortly anyway.

// HeartbeatRetryDeadline bounds one heartbeat's retries. Deliberately
// shorter than the worker's other budgets: unlike a registration or a
// terminal-state report, a heartbeat has no lasting value once the next
// one is due. Unscaled — timeMultiplier is applied inside lib/httpx.
var HeartbeatRetryDeadline = 5 * time.Second

// HeartbeatResponse is what PUT /worker/:id/heartbeat answers with.
type HeartbeatResponse struct {
	// Stopped is the worker's server-side Stopped flag, as of this
	// heartbeat. True means shut down after the current task.
	Stopped bool `json:"stopped"`

	// LastHeardTs is the timestamp the server just recorded, from its own
	// clock. Echoed back for observability — the worker does not use it,
	// and must not: only the server's reading of it counts.
	LastHeardTs int64 `json:"lastHeardTs"`

	// ServerInstanceId identifies the server *process*. A change means the
	// server restarted since the last heartbeat.
	ServerInstanceId string `json:"serverInstanceId"`

	// ServerStartedTs is when that process started serving, in unix
	// seconds.
	ServerStartedTs int64 `json:"serverStartedTs"`
}

// Heartbeat tells the server this worker is alive and reads back what the
// server thinks of it.
//
// Sends no body: everything the server records here is its own
// (LastHeardTs from its own clock), and the fields the worker owns travel
// on PUT /worker/:id instead. That separation is what keeps the reaper's
// arithmetic honest.
func (c *WorkerConf) Heartbeat() (*HeartbeatResponse, error) {
	res, err := httpx.Do(context.Background(), "PUT", workerURL(c.Id, "/heartbeat"), nil,
		httpx.Policy{Deadline: HeartbeatRetryDeadline})
	if err != nil {
		return nil, err
	}
	hb := &HeartbeatResponse{}
	if err := json.Unmarshal(res.Body, hb); err != nil {
		return nil, err
	}
	return hb, nil
}

// noteServerInstance logs a server restart the first time this worker sees
// evidence of one, and remembers the new instance so it is reported once
// rather than on every heartbeat.
//
// Nothing else happens yet, deliberately. Reacting to a restart is phase
// 5's business, and phase 1's whole premise is that a worker rides out a
// server outage without needing to be told about it — so noticing is a
// diagnostic, not a control path.
func (c *WorkerConf) noteServerInstance(hb *HeartbeatResponse) {
	if hb == nil || hb.ServerInstanceId == "" {
		return
	}
	if c.serverInstanceId == "" {
		c.serverInstanceId = hb.ServerInstanceId
		return
	}
	if c.serverInstanceId == hb.ServerInstanceId {
		return
	}

	log.WithFields(log.Fields{
		"workerId":    c.Id.Hex(),
		"wasInstance": c.serverInstanceId,
		"nowInstance": hb.ServerInstanceId,
		"startedTs":   hb.ServerStartedTs,
	}).Warn("the blanket server restarted since the last heartbeat; continuing")
	c.serverInstanceId = hb.ServerInstanceId
}

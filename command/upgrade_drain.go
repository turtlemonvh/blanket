package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/proclive"
	"github.com/turtlemonvh/blanket/lib/timing"
	"github.com/turtlemonvh/blanket/worker"
)

// drainResponse is the part of POST /ops/restart/drain's answer the CLI
// reads.
type drainResponse struct {
	Drained bool     `json:"drained"`
	Running []string `json:"stillRunning"`
}

// drainWorkers drains the fleet and waits until every drained worker has
// exited, or gives up. It returns the workers still running when it gave
// up (empty means drained) and how long it waited past the first drain.
//
// One drain call waits at most restart.drainTimeout. When that runs out
// with workers still finishing their tasks, the CLI calls again (a repeat
// drain waits on the first rather than conflicting) for up to the
// restart's own watchdog deadline more. Each call refreshes that deadline
// on the server, so the watchdog does not abort a drain the CLI is still
// waiting on.
//
// Giving up is the caller's cue to refuse the restart, never to go ahead:
// exec'ing over a worker mid-task kills the task under any supervisor
// that takes the whole service down on exit.
func drainWorkers(port int) ([]string, time.Duration, error) {
	perCall, keepWaiting := drainBudgets(port)

	var since time.Time
	for {
		called := time.Now()
		body, err := opsCallLong(port, http.MethodPost, "/ops/restart/drain", nil, perCall)
		if err != nil {
			// A server from before repeat drains could be waited on
			// answers a drain of a DRAINING restart with 409. It has
			// still drained; the CLI watches the workers itself instead.
			var oe *opsError
			if errors.As(err, &oe) && oe.Status == http.StatusConflict && restartIsDraining(port) {
				if since.IsZero() {
					since = time.Now()
				}
				still := waitForDrainedWorkers(port, keepWaiting-time.Since(since))
				return still, time.Since(since), nil
			}
			return nil, 0, err
		}
		var dr drainResponse
		if err := json.Unmarshal(body, &dr); err != nil {
			return nil, 0, fmt.Errorf("could not read the drain response: %w", err)
		}
		if dr.Drained {
			return nil, waitedSince(since), nil
		}
		if since.IsZero() {
			since = time.Now()
			if !upgradeConf.JSON {
				fmt.Fprintf(os.Stderr,
					"waiting for %d worker(s) to finish their current task (up to %s more); interrupting is safe, `blanket upgrade --resume --yes` picks the wait back up\n",
					len(dr.Running), keepWaiting.Round(time.Second))
			}
		}
		if time.Since(since) >= keepWaiting {
			return dr.Running, time.Since(since), nil
		}
		// A server that answers "not drained" without having waited
		// (a short drainTimeout, say) must not be polled in a hot loop.
		if time.Since(called) < timing.Scale(time.Second) {
			time.Sleep(timing.Scale(time.Second))
		}
	}
}

func waitedSince(since time.Time) time.Duration {
	if since.IsZero() {
		return 0
	}
	return time.Since(since)
}

// drainBudgets returns how long one drain call may take and how long the
// CLI keeps re-calling after the first one times out. The server's own
// values win, since they are the ones that bound its wait and its
// watchdog; the CLI's config is the fallback for a server that does not
// report them.
func drainBudgets(port int) (perCall, keepWaiting time.Duration) {
	drainTimeout := timing.Scale(viper.GetDuration("restart.drainTimeout"))
	keepWaiting = timing.Scale(viper.GetDuration("restart.deadline"))
	if st, err := fetchRestartStatus(port); err == nil {
		if st.DrainTimeoutMs > 0 {
			drainTimeout = time.Duration(st.DrainTimeoutMs) * time.Millisecond
		}
		if st.Restart.DeadlineSeconds > 0 {
			keepWaiting = time.Duration(st.Restart.DeadlineSeconds) * time.Second
		}
	}
	if drainTimeout <= 0 {
		drainTimeout = timing.Scale(60 * time.Second)
	}
	if keepWaiting <= 0 {
		keepWaiting = timing.Scale(5 * time.Minute)
	}
	// The slack covers the request itself; the server answers when its
	// own wait ends.
	return drainTimeout + timing.Scale(30*time.Second), keepWaiting
}

func restartIsDraining(port int) bool {
	st, err := fetchRestartStatus(port)
	return err == nil && st.Restart.State == "DRAINING"
}

// waitForDrainedWorkers is the CLI-side wait for a server too old to wait
// on a drain twice. It applies the server's own rule (see
// server.workerHasExited) to the worker records: a drained worker has
// exited once it reported its own shutdown, or once its pid is
// conclusively gone. The ops endpoints are loopback-only, so the CLI is on
// the same host and pid liveness means the same thing here as there.
//
// It returns the ids still running when budget ran out.
func waitForDrainedWorkers(port int, budget time.Duration) []string {
	deadline := time.Now().Add(budget)
	for {
		still, err := drainedWorkersStillRunning(port)
		if err == nil && len(still) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return still
		}
		time.Sleep(timing.Scale(time.Second))
	}
}

func drainedWorkersStillRunning(port int) ([]string, error) {
	body, err := opsCallTimeout(port, http.MethodGet, "/worker/", nil, timing.Scale(15*time.Second))
	if err != nil {
		return nil, err
	}
	var ws []worker.WorkerConf
	if err := json.Unmarshal(body, &ws); err != nil {
		return nil, err
	}
	var still []string
	for _, w := range ws {
		if !w.Stopped || !w.RespawnIntent || w.StoppedReason == worker.StopReasonSelf {
			continue
		}
		if alive, conclusive := proclive.IsAlive(w.Pid, w.PidStartTs); !alive && conclusive {
			continue
		}
		still = append(still, w.Id.Hex())
	}
	return still, nil
}

// Tests for the synchronous / streaming submit client
// (turtlemonvh/blanket#27). These run against a real blanket server on a
// real socket rather than a stub: the thing worth testing is that the
// client and the server agree on the wire format, and a hand-written
// fixture would agree with itself no matter what the server did.

package client

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/server"
	"github.com/turtlemonvh/blanket/tasks"
)

const testTaskTypeToml = `
tags = ["bash", "unix"]
timeout = 10
command = "echo 'hello from blanket'"
executor = "bash"
`

// newTestServer stands up a blanket server on a loopback port and
// returns it plus that port, which is all the client functions take.
func newTestServer(t *testing.T) (*server.ServerConfig, int, func()) {
	t.Helper()

	typesDir := t.TempDir()
	resultsDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(typesDir, "echo_task.toml"), []byte(testTaskTypeToml), 0644))
	viper.Set("tasks.typesPaths", []string{typesDir})
	viper.Set("tasks.resultsPath", resultsDir)

	DB, dbCloser := bolt.NewTestDB()
	Q, qCloser := bolt.NewTestQueue()
	s := &server.ServerConfig{
		DB:           DB,
		Q:            Q,
		ResultsPath:  resultsDir,
		Version:      "blanket (client test)",
		TaskEvents:   server.NewEventHub(),
		WorkerEvents: server.NewEventHub(),
	}

	httpSrv := httptest.NewServer(s.GetRouter())
	u, err := url.Parse(httpSrv.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	return s, port, func() {
		httpSrv.Close()
		qCloser()
		dbCloser()
		viper.Set("tasks.typesPaths", nil)
		viper.Set("tasks.resultsPath", "")
	}
}

// awaitTask waits for the submit under test to have saved its task --
// the client generates no id, so a test that wants to act like the
// worker has to go find it.
func awaitTask(t *testing.T, s *server.ServerConfig) tasks.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		found, _, err := s.DB.GetTasks(&database.TaskSearchConf{
			Limit:      10,
			SmallestId: objectid.NewObjectIdWithTime(time.Unix(0, 0)),
			LargestId:  objectid.NewObjectIdWithTime(time.Unix(database.FAR_FUTURE_SECONDS, 0)),
		})
		require.NoError(t, err)
		if len(found) > 0 {
			return found[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("submit never saved a task")
	return tasks.Task{}
}

// finishLikeAWorker writes the log files a real run would leave behind
// and moves the task to its terminal state.
func finishLikeAWorker(t *testing.T, s *server.ServerConfig, tsk tasks.Task, state string, exitCode int, stdout, stderr string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(tsk.ResultDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tsk.ResultDir, "blanket.stdout.log"), []byte(stdout), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tsk.ResultDir, "blanket.stderr.log"), []byte(stderr), 0644))
	code := exitCode
	require.NoError(t, s.DB.FinishTask(tsk.Id, &database.TaskFinishConfig{NewState: state, ExitCode: &code}))
	s.TaskEvents.Notify()
}

func TestSubmitTaskAndWait_Completes(t *testing.T) {
	s, port, cleanup := newTestServer(t)
	defer cleanup()

	go func() {
		finishLikeAWorker(t, s, awaitTask(t, s), "ERROR", 3, "hello world\n", "a warning\n")
	}()

	res, err := SubmitTaskAndWait("echo_task", nil, port, WaitOptions{Wait: "20s"})
	require.NoError(t, err)
	require.NotNil(t, res.Payload)

	assert.Equal(t, 200, res.Status)
	assert.False(t, res.TimedOut)
	assert.Equal(t, server.WaitOutcomeCompleted, res.Payload.WaitOutcome)
	assert.Equal(t, "ERROR", res.Payload.Task.State)
	require.NotNil(t, res.Payload.Task.ExitCode)
	assert.Equal(t, 3, *res.Payload.Task.ExitCode)
	assert.Equal(t, "hello world\n", res.Payload.Stdout)
	assert.Equal(t, "a warning\n", res.Payload.Stderr)
	assert.Equal(t, res.Payload.Task.Id.Hex(), res.TaskId())
}

// A wait that expires is not an error -- it is an outcome the caller has
// to be able to tell apart, because the CLI turns it into its own exit
// code.
func TestSubmitTaskAndWait_TimesOut(t *testing.T) {
	_, port, cleanup := newTestServer(t)
	defer cleanup()

	res, err := SubmitTaskAndWait("echo_task", nil, port, WaitOptions{Wait: "300ms"})
	require.NoError(t, err)
	assert.True(t, res.TimedOut)
	require.NotNil(t, res.Timeout)
	assert.Equal(t, server.WaitOutcomeTimeout, res.Timeout.WaitOutcome)
	assert.NotEmpty(t, res.TaskId())
}

func TestSubmitTaskAndWait_UnknownType(t *testing.T) {
	_, port, cleanup := newTestServer(t)
	defer cleanup()

	_, err := SubmitTaskAndWait("nope", nil, port, WaitOptions{Wait: "5s"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "{", "the server's error message should be unwrapped, not raw JSON")
}

// TestSubmitTaskAndStream_SplitsStreams is the CLI's --follow behaviour
// in miniature: log events arrive tagged with the stream they came from,
// so `blanket submit --follow 2>/dev/null` can keep working, and the
// terminal result event carries the exit code the process exits with.
func TestSubmitTaskAndStream_SplitsStreams(t *testing.T) {
	s, port, cleanup := newTestServer(t)
	defer cleanup()

	go func() {
		tsk := awaitTask(t, s)
		finishLikeAWorker(t, s, tsk, "ERROR", 3, "out one\nout two\n", "err one\n")
	}()

	var stdout, stderr []string
	var states []string
	resultSeen := 0

	res, err := SubmitTaskAndStream("echo_task", nil, port, WaitOptions{Wait: "20s"}, StreamCallbacks{
		OnState: func(ev server.StateEvent) error {
			states = append(states, ev.State)
			return nil
		},
		OnLog: func(ev server.LogEvent) error {
			if ev.Stream == server.LogStreamStderr {
				stderr = append(stderr, ev.Line)
			} else {
				stdout = append(stdout, ev.Line)
			}
			return nil
		},
		OnResult: func(ev server.ResultEvent) error {
			resultSeen++
			return nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, res.Payload)

	assert.Equal(t, 1, resultSeen, "exactly one terminal event ends the stream")
	assert.NotEmpty(t, states)
	assert.Contains(t, states, "ERROR")
	assert.Equal(t, "ERROR", res.Payload.Task.State)
	require.NotNil(t, res.Payload.Task.ExitCode)
	assert.Equal(t, 3, *res.Payload.Task.ExitCode)

	// The streamed lines were split by their origin, and the terminal
	// payload repeats both tails whether or not the live lines arrived.
	assert.Equal(t, "out one\nout two\n", res.Payload.Stdout)
	assert.Equal(t, "err one\n", res.Payload.Stderr)
	assert.NotContains(t, stdout, "err one", "a stderr line must never be delivered as stdout")
	assert.NotContains(t, stderr, "out one", "a stdout line must never be delivered as stderr")
}

// The terminal event of a stream and the body of a blocking submit are
// meant to be the same object; this compares them field for field on two
// runs of the same task type.
func TestStreamResultMatchesBlockingPayload(t *testing.T) {
	s, port, cleanup := newTestServer(t)
	defer cleanup()

	run := func(stream bool) *server.CompletionPayload {
		go func() {
			finishLikeAWorker(t, s, awaitTask(t, s), "SUCCESS", 0, "same output\n", "")
		}()
		var res WaitResult
		var err error
		if stream {
			res, err = SubmitTaskAndStream("echo_task", nil, port, WaitOptions{Wait: "20s"}, StreamCallbacks{})
		} else {
			res, err = SubmitTaskAndWait("echo_task", nil, port, WaitOptions{Wait: "20s"})
		}
		require.NoError(t, err)
		require.NotNil(t, res.Payload)
		return res.Payload
	}

	streamed := run(true)
	// Clear the first task out so awaitTask finds the second one.
	require.NoError(t, s.DB.DeleteTask(streamed.Task.Id))
	blocking := run(false)

	// Everything but the identity of the task itself must match.
	normalize := func(p *server.CompletionPayload) map[string]interface{} {
		encoded, err := json.Marshal(p)
		require.NoError(t, err)
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal(encoded, &m))
		delete(m, "task")
		return m
	}
	assert.Equal(t, normalize(blocking), normalize(streamed))
	assert.Equal(t, blocking.Task.State, streamed.Task.State)
	assert.Equal(t, blocking.Task.ExitCode, streamed.Task.ExitCode)
}

// External test package to avoid the import cycle:
//   worker → lib/bolt → worker
//
// TODO: additional integration tests to write
//
// Covered:
//   - normal case (single task): TestProcessOne — submit 1 task, claim, run,
//     assert SUCCESS + stdout contents + result dir
//
// Not yet covered:
//   - normal case, 2 tasks: both finish successfully, results dirs created
//   - timeout case: task 1 runs over its timeout, gets killed, task 2 succeeds
//   - worker shutdown: SIGTERM to worker stops it before task 2 runs
//   - stopped-task state: task is api-stopped mid-flight → ends in STOPPED,
//     task 2 still succeeds
//   - log production: both worker log and task stdout/stderr are written
//
// Cross-cutting considerations for these tests:
//   - use accelerated time (TimeMultiplier) to keep wall-clock short
//   - assert goroutine count is stable across the run (no leaks); the metrics
//     API can expose this.
package worker_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/server"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
	"gopkg.in/mgo.v2/bson"
)

// testTaskTypeToml is a minimal bash task with no required env vars.
const testTaskTypeToml = `
tags = ["bash", "unix"]
timeout = 10
command = "echo 'hello from blanket integration test'"
executor = "bash"
`

// TestProcessOne is a full integration test of the task execution pipeline.
//
// It:
//  1. Starts a real HTTP server backed by in-memory BoltDB
//  2. Configures viper so that task/worker HTTP calls resolve to that server
//  3. Submits an "echo_task" via the API
//  4. Registers a worker and claims the task via the API
//  5. Calls ProcessOne (the same function the worker loop calls)
//  6. Asserts the task ends in SUCCESS with the expected output on disk
func TestProcessOne(t *testing.T) {
	// Set up a single temp workspace for task types and results
	workDir, err := os.MkdirTemp("", "blanket-integration-*")
	if err != nil {
		t.Fatalf("failed to create work dir: %v", err)
	}
	defer os.RemoveAll(workDir)

	typesDir := filepath.Join(workDir, "types")
	resultsDir := filepath.Join(workDir, "results")
	for _, d := range []string{typesDir, resultsDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", d, err)
		}
	}

	// Write a simple task type
	err = os.WriteFile(
		filepath.Join(typesDir, "echo_task.toml"),
		[]byte(testTaskTypeToml),
		0644,
	)
	if err != nil {
		t.Fatalf("failed to write task type: %v", err)
	}

	// Start an in-memory server
	db, dbCleanup := bolt.NewTestDB()
	defer dbCleanup()
	q, qCleanup := bolt.NewTestQueue()
	defer qCleanup()

	sc := &server.ServerConfig{
		DB:             db,
		Q:              q,
		ResultsPath:    resultsDir,
		TimeMultiplier: 1.0,
	}
	httpSrv := httptest.NewServer(sc.GetRouter())
	defer httpSrv.Close()

	// Point viper at our test server and directories.
	// The tasks package and worker package both read from viper directly.
	u, _ := url.Parse(httpSrv.URL)
	port, _ := strconv.Atoi(u.Port())
	viper.Set("port", port)
	viper.Set("tasks.typesPaths", []string{typesDir})
	viper.Set("tasks.resultsPath", resultsDir)
	viper.Set("timeMultiplier", 1.0)
	defer func() {
		viper.Set("port", 0)
		viper.Set("tasks.typesPaths", nil)
		viper.Set("tasks.resultsPath", "")
	}()

	// Register a worker via the API (worker.UpdateInDatabase uses PUT /worker/:id)
	workerID := bson.NewObjectId()
	wConf := worker.WorkerConf{
		Id:            workerID,
		Tags:          []string{"bash", "unix"},
		Stopped:       false,
		CheckInterval: 0.5,
		Logfile:       filepath.Join(workDir, "worker.log"),
	}
	workerBytes, _ := json.Marshal(wConf)

	req, _ := http.NewRequest(
		"PUT",
		fmt.Sprintf("%s/worker/%s", httpSrv.URL, workerID.Hex()),
		bytes.NewReader(workerBytes),
	)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if !assert.NoError(t, err, "register worker") {
		return
	}
	resp.Body.Close()
	if !assert.Equal(t, http.StatusOK, resp.StatusCode, "register worker status") {
		return
	}

	// Submit a task via POST /task/
	resp, err = http.Post(
		fmt.Sprintf("%s/task/", httpSrv.URL),
		"application/json",
		bytes.NewReader([]byte(`{"type": "echo_task"}`)),
	)
	if !assert.NoError(t, err, "submit task") {
		return
	}
	if !assert.Equal(t, http.StatusCreated, resp.StatusCode, "submit task status") {
		resp.Body.Close()
		return
	}
	var submittedTask tasks.Task
	json.NewDecoder(resp.Body).Decode(&submittedTask)
	resp.Body.Close()
	if !assert.NotEmpty(t, submittedTask.Id, "submitted task ID") {
		return
	}

	// Claim the task via POST /task/claim/:workerid
	resp, err = http.Post(
		fmt.Sprintf("%s/task/claim/%s", httpSrv.URL, workerID.Hex()),
		"application/json",
		nil,
	)
	if !assert.NoError(t, err, "claim task") {
		return
	}
	if !assert.Equal(t, http.StatusOK, resp.StatusCode, "claim task status") {
		resp.Body.Close()
		return
	}
	var claimedTask tasks.Task
	json.NewDecoder(resp.Body).Decode(&claimedTask)
	resp.Body.Close()
	assert.Equal(t, submittedTask.Id, claimedTask.Id, "claimed task should match submitted")
	assert.Equal(t, "CLAIMED", claimedTask.State)

	// Execute the task
	err = wConf.ProcessOne(&claimedTask)
	assert.NoError(t, err, "ProcessOne should complete without error")

	// Verify final state via the API
	resp, err = http.Get(fmt.Sprintf("%s/task/%s", httpSrv.URL, submittedTask.Id.Hex()))
	if !assert.NoError(t, err, "fetch finished task") {
		return
	}
	var finalTask tasks.Task
	json.NewDecoder(resp.Body).Decode(&finalTask)
	resp.Body.Close()

	assert.Equal(t, "SUCCESS", finalTask.State, "task should be SUCCESS after ProcessOne")
	assert.Equal(t, 100, finalTask.Progress)

	// Verify stdout log exists and contains the expected output
	stdoutPath := filepath.Join(finalTask.ResultDir, "blanket.stdout.log")
	content, err := os.ReadFile(stdoutPath)
	assert.NoError(t, err, "stdout log should exist at %s", stdoutPath)
	assert.Contains(t, string(content), "hello from blanket integration test")
}

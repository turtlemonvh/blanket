package server

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cast"
	"github.com/turtlemonvh/blanket/worker"
	"net/http"
)

// Search in the database for all items
// For each item in the db, check that a process exists that has the right name
func getWorkers(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	result, err := DB.GetWorkers()
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}
	c.String(http.StatusOK, result)
}

// Get just the configuration for this worker as json
func getWorker(c *gin.Context) {
	workerId := c.Param("id")

	c.Header("Content-Type", "application/json")

	result, err := DB.GetWorker(workerId)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}
	c.String(http.StatusOK, result)
}

// Register with pid
// Continue to write to old log via append
func updateWorker(c *gin.Context) {
	var err error
	var workerPid int

	c.Header("Content-Type", "application/json")

	workerPid, err = cast.ToIntE(c.Param("id"))
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "The 'id' parameter must be an integer, not %s."}`, workerPid)
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	w := worker.WorkerConf{}
	err = c.BindJSON(&w)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// Validate worker conf before saving
	if workerPid != w.Pid {
		errMsg := fmt.Sprintf(`{"error": "Problem parsing conf. Pid does not equal the expected value ('%d' != '%d')"}`, w.Pid, workerPid)
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	err = DB.UpdateWorker(&w)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}
	c.String(http.StatusOK, "{}")
}

// Send SigTerm to the worker's pid
// Allow the user to pass an option to not signal; this would be used if the process is already exiting
// If the worker is already down but didn't remove itself from the database, calling this
// function will remove the worker entry from the database too.
func shutDownWorker(c *gin.Context) {
	workerId := c.Param("id")
	c.Header("Content-Type", "application/json")

	// FIXME: Return bytes or string?
	workerStr, err := DB.GetWorker(workerId)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// Turn into a worker conf object
	w := worker.WorkerConf{}
	err = json.Unmarshal([]byte(workerStr), &w)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// FIXME: Allow the worker to poll for state instead of doing this
	// Send SIGTERM
	err = w.Stop()
	if err != nil && err.Error() == "os: process already finished" {
		err = DB.DeleteWorker(workerId)
	}
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// FIXME: Send SIGKILL if still not finished

	c.String(http.StatusOK, `{"status": "ok"}`)
}

// Remove the worker's record from the db if it exists
// Should only be called by the worker itself as it is shutting down
func deleteWorker(c *gin.Context) {
	workerId := c.Param("id")
	c.Header("Content-Type", "application/json")

	err := DB.DeleteWorker(workerId)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, fmt.Sprintf(`{"id": "%s"}`, workerId))
}

// FIXME: Wait until worker registers itself in the database to return, up to X seconds
func launchWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error

	w := worker.WorkerConf{}
	err = c.BindJSON(&w)
	if err != nil {
		return
	}

	// Always a daemon; default check interval is 2 seconds
	w.Daemon = true
	if w.CheckInterval == 0 {
		w.CheckInterval = 2
	}

	err = w.Run()
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, "{}")
}

// FIXME: Stream file contents
func getWorkerLogfile(c *gin.Context) {
	c.Header("Content-Type", "text/plain")

	workerId := c.Param("id")

	// FIXME: Return bytes or string?
	workerStr, err := DB.GetWorker(workerId)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf(`Error: "%s"`, err.Error()))
		return
	}

	// Turn into a worker conf object
	w := worker.WorkerConf{}
	err = json.Unmarshal([]byte(workerStr), &w)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf(`Error: "%s"`, err.Error()))
		return
	}

	if w.Pid == 0 {
		c.String(http.StatusNotFound, fmt.Sprintf(`Error: Worker with id %s not found`, workerId))
		return
	}

	// Open file and send all contents
	// https://godoc.org/github.com/gin-gonic/gin#Context.File
	c.File(w.Logfile)
}

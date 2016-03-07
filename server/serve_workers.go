package server

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/turtlemonvh/blanket/worker"
	"net/http"
)

// Search in the database for all items
// For each item in the db, check that a process exists that has the right name
func getWorkers(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	ws, err := DB.GetWorkers()
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.JSON(http.StatusOK, ws)
}

// Get just the configuration for this worker as json
func getWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	worker, err := DB.GetWorker(workerId)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.JSON(http.StatusOK, worker)
}

// Register with Id
// Continue to write to old log via append
func updateWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	w := worker.WorkerConf{}
	err = c.BindJSON(&w)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	// Validate worker conf before saving
	if workerId != w.Id {
		errMsg := fmt.Sprintf(`{"error": "Problem parsing conf. Id does not equal the expected value ('%d' != '%d')"}`, w.Id, workerId)
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	err = DB.UpdateWorker(&w)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.String(http.StatusOK, "{}")
}

// Send SigTerm to the worker's pid
// Allow the user to pass an option to not signal; this would be used if the process is already exiting
// If the worker is already down but didn't remove itself from the database, calling this
// function will remove the worker entry from the database too.
func shutDownWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	w, err := DB.GetWorker(workerId)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	// FIXME: Allow the worker to poll for state instead of doing this
	// Send SIGTERM
	err = w.Stop()
	if err != nil && err.Error() == "os: process already finished" {
		err = DB.DeleteWorker(workerId)
	}
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	// FIXME: Send SIGKILL if still not finished

	c.String(http.StatusOK, `{}`)
}

// Remove the worker's record from the db if it exists
// Should only be called by the worker itself as it is shutting down
func deleteWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	err = DB.DeleteWorker(workerId)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
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
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	c.String(http.StatusOK, "{}")
}

// FIXME: Stream file contents
func getWorkerLogfile(c *gin.Context) {
	c.Header("Content-Type", "text/plain")

	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	// FIXME: Return bytes or string?
	w, err := DB.GetWorker(workerId)
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

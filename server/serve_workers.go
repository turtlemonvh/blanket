package server

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/gin-gonic/gin"
	"github.com/turtlemonvh/blanket/lib/tailed_file"
	"github.com/turtlemonvh/blanket/worker"
	"gopkg.in/mgo.v2/bson"
	"net/http"
	"time"
)

// Search in the database for all items
// For each item in the db, check that a process exists that has the right name
func (s *ServerConfig) getWorkers(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	ws, err := s.DB.GetWorkers()
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.JSON(http.StatusOK, ws)
}

// Get just the configuration for this worker as json
func (s *ServerConfig) getWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	worker, err := s.DB.GetWorker(workerId)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.JSON(http.StatusOK, worker)
}

// Register with Id
// Continue to write to old log via append
func (s *ServerConfig) updateWorker(c *gin.Context) {
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

	err = s.DB.UpdateWorker(&w)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.String(http.StatusOK, "{}")
}

// Put the worker in the "stopped" state
// The worker will poll for this state
// FIXME: Make this worker update atomic
// FIXME: Update lastHeardTs too
// FIXME: Allow force option that sends signals (on platforms That support that)
func (s *ServerConfig) stopWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	w, err := s.DB.GetWorker(workerId)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	w.Stopped = true
	err = s.DB.UpdateWorker(&w)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	c.String(http.StatusOK, `{}`)
}

// Find an existing worker in the database and change its status
// Start it on the command line
func (s *ServerConfig) restartWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	w, err := s.DB.GetWorker(workerId)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	s.launchWorker(c, &w)
}

// Remove the worker's record from the db if it exists
// Should only be called by the worker itself as it is shutting down
func (s *ServerConfig) deleteWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	// FIXME: Check that worker is stopped
	w := worker.WorkerConf{}
	err = c.BindJSON(&w)
	if err == nil && w.Stopped != true {
		c.String(http.StatusBadRequest, `{"error": "Cannot delete a worker that has not been stopped"}`)
	}

	err = s.DB.DeleteWorker(workerId)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.String(http.StatusOK, fmt.Sprintf(`{"id": "%s"}`, workerId.Hex()))
}

func (s *ServerConfig) launchNewWorker(c *gin.Context) {
	var err error
	w := worker.WorkerConf{}
	err = c.BindJSON(&w)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
	}
	s.launchWorker(c, &w)
}

// Called by other request handlers
func (s *ServerConfig) launchWorker(c *gin.Context, w *worker.WorkerConf) {
	c.Header("Content-Type", "application/json")

	var err error

	// Always a daemon; default check interval is 2 seconds
	w.Daemon = true
	if w.CheckInterval == 0 {
		w.CheckInterval = worker.DEFAULT_CHECK_INTERVAL_SECONDS
	}

	err = w.Run()
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	// Poll database until worker is found
	maxRequestTime := time.NewTimer(time.Duration(MAX_REQUEST_TIME_SECONDS*s.TimeMultiplier) * time.Second)
	loopWaitTime := time.Duration(500*s.TimeMultiplier) * time.Millisecond
	for {
		w, _ := s.DB.GetWorker(w.Id)
		if w.Pid != 0 {
			c.JSON(http.StatusOK, w)
			return
		}

		log.WithFields(log.Fields{
			"workerConf": w,
		}).Info("Looping while waiting for worker to show up in database")

		select {
		case <-maxRequestTime.C:
			err = fmt.Errorf("Worker was not found after %d seconds", MAX_REQUEST_TIME_SECONDS)
			c.String(http.StatusRequestTimeout, MakeErrorString(err.Error()))
			return
		case <-time.After(loopWaitTime):
			continue
		}
	}
	c.String(http.StatusOK, `{}`)
}

// FIXME: Stream file contents
func (s *ServerConfig) getWorkerLogfile(c *gin.Context) {
	c.Header("Content-Type", "text/plain")

	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	// FIXME: Return bytes or string?
	w, err := s.DB.GetWorker(workerId)
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

// Stream out worker log
func (s *ServerConfig) streamWorkerLog(c *gin.Context) {
	var err error
	var workerId bson.ObjectId

	workerId, err = SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	// FIXME: Return bytes or string?
	w, err := s.DB.GetWorker(workerId)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf(`Error: "%s"`, err.Error()))
		return
	}

	stdoutPath := w.Logfile
	sub, err := tailed_file.Follow(stdoutPath)
	if err != nil {
		c.String(http.StatusInternalServerError, "Error opening logfile stream")
		return
	}
	defer sub.Stop()

	// Worker is done if it is stopped
	isComplete := func() bool {
		return true
	}
	s.streamLog(c, sub, isComplete)
}

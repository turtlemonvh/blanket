package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/tailed_file"
	"github.com/turtlemonvh/blanket/worker"
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
		errMsg := fmt.Sprintf(`{"error": "Problem parsing conf. Id does not equal the expected value ('%s' != '%s')"}`, w.Id, workerId)
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

	s.WorkerEvents.Notify()
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
	s.WorkerEvents.Notify()
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

// ErrWorkerNotRegistered is returned by launchWorkerAndWait if the worker
// doesn't show up in the database with a nonzero Pid within
// MAX_REQUEST_TIME_SECONDS of being started.
var ErrWorkerNotRegistered = errors.New("worker was not found in the database within the expected time")

// launchWorkerAndWait starts w as a daemon and polls the database until
// its Pid is registered or the request-time budget elapses. Returns the
// registered worker config.
func (s *ServerConfig) launchWorkerAndWait(ctx context.Context, w *worker.WorkerConf) (worker.WorkerConf, error) {
	w.Daemon = true
	if w.CheckInterval == 0 {
		w.CheckInterval = worker.DEFAULT_CHECK_INTERVAL_SECONDS
	}
	if w.CheckInterval < worker.MIN_CHECK_INTERVAL_SECONDS {
		return worker.WorkerConf{}, worker.ErrCheckIntervalTooLow
	}

	if err := w.Run(); err != nil {
		return worker.WorkerConf{}, err
	}

	deadline := time.NewTimer(time.Duration(MAX_REQUEST_TIME_SECONDS*s.TimeMultiplier) * time.Second)
	defer deadline.Stop()
	loopWait := time.Duration(500*s.TimeMultiplier) * time.Millisecond

	for {
		registered, _ := s.DB.GetWorker(w.Id)
		if registered.Pid != 0 {
			s.WorkerEvents.Notify()
			return registered, nil
		}

		select {
		case <-deadline.C:
			return worker.WorkerConf{}, fmt.Errorf("%w after %d seconds", ErrWorkerNotRegistered, MAX_REQUEST_TIME_SECONDS)
		case <-time.After(loopWait):
			continue
		}
	}
}

// Called by other request handlers
func (s *ServerConfig) launchWorker(c *gin.Context, w *worker.WorkerConf) {
	c.Header("Content-Type", "application/json")

	registered, err := s.launchWorkerAndWait(c.Request.Context(), w)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, registered)
	case errors.Is(err, worker.ErrCheckIntervalTooLow):
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
	case errors.Is(err, ErrWorkerNotRegistered):
		c.String(http.StatusRequestTimeout, MakeErrorString(err.Error()))
	default:
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
	}
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
	var workerId objectid.ObjectId

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

	isComplete := func() bool {
		w, err = s.DB.GetWorker(workerId)
		if err != nil {
			log.WithFields(log.Fields{
				"workerId":       workerId,
				"subscriptionId": sub.Id,
				"tailedFile":     sub.TailedFile.Filepath,
			}).Error("error refreshing worker state while processing logstreaming request")
			return true
		}
		if w.Stopped {
			log.WithFields(log.Fields{
				"workerId":       workerId,
				"subscriptionId": sub.Id,
				"tailedFile":     sub.TailedFile.Filepath,
			}).Info("stopping logstreaming request because worker is stopped")
			return true
		}
		return false
	}
	s.streamLog(c, sub, isComplete)
}

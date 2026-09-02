package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cast"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/queue"
	"github.com/turtlemonvh/blanket/lib/tailed_file"
	"github.com/turtlemonvh/blanket/tasks"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

/*
 * Utility functions
 */

// Either gets the task id from a context object or returns an error
// Will also set the response for the request if there was a problem
func (s *ServerConfig) getTaskId(c *gin.Context) (objectid.ObjectId, error) {
	var err error
	var tid objectid.ObjectId

	taskIdStr := c.Param("id")
	if !objectid.IsObjectIdHex(taskIdStr) {
		err = fmt.Errorf("'%s' is not not a valid objectid", taskIdStr)
		c.String(http.StatusInternalServerError, fmt.Sprintf(`{"error": "%s"}`, err.Error()))
	} else {
		tid = objectid.ObjectIdHex(taskIdStr)
	}

	return tid, err
}

// ErrTaskNotCancelable is returned by cancelTaskById when a task exists
// but isn't in a state that can be canceled (only RUNNING and WAITING
// are). Kept as a sentinel rather than a formatted error so callers (the
// REST handler, MCP) can each decide how to present it.
var ErrTaskNotCancelable = errors.New("task is not in a cancelable state (must be RUNNING or WAITING)")

// cancelTaskById transitions a RUNNING or WAITING task to STOPPED.
func (s *ServerConfig) cancelTaskById(ctx context.Context, taskId objectid.ObjectId) error {
	task, err := s.DB.GetTask(taskId)
	if err != nil {
		return err
	}
	if task.State != "RUNNING" && task.State != "WAITING" {
		return ErrTaskNotCancelable
	}
	if err := s.DB.FinishTask(taskId, "STOPPED"); err != nil {
		return err
	}
	s.TaskEvents.Notify()
	return nil
}

// removeTaskById deletes a task from the database and its result
// directory. Mirrors the historical removeTask semantics: deleting a
// nonexistent task's result directory is not an error.
func (s *ServerConfig) removeTaskById(ctx context.Context, taskId objectid.ObjectId) error {
	if err := s.DB.DeleteTask(taskId); err != nil {
		return err
	}
	if err := os.RemoveAll(path.Join(s.ResultsPath, taskId.Hex())); err != nil {
		return err
	}
	s.TaskEvents.Notify()
	return nil
}

/*
 * Request handlers
 */

// Get all tasks
// Only looks in the database
func (s *ServerConfig) getTasks(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	tc := database.TaskSearchConfFromContext(c)
	log.WithFields(log.Fields{
		"requiredTaskTags": tc.RequiredTags,
		"maxTaskTags":      tc.MaxTags,
		"taskTypes":        MapKeys(tc.AllowedTaskTypes),
		"taskStates":       MapKeys(tc.AllowedTaskStates),
		"limit":            tc.Limit,
		"smallestId":       tc.SmallestId.Hex(),
		"largestId":        tc.LargestId.Hex(),
		"justCounts":       tc.JustCounts,
	}).Debug("Task request params")

	result, nfounddb, err := s.DB.GetTasks(tc)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	if tc.JustCounts {
		c.String(http.StatusOK, cast.ToString(nfounddb))
	} else {
		c.JSON(http.StatusOK, result)
	}
}

func (s *ServerConfig) getTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId objectid.ObjectId

	taskId, err = s.getTaskId(c)
	if err != nil {
		return
	}

	var task tasks.Task
	task, err = s.DB.GetTask(taskId)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	c.JSON(http.StatusOK, task)
}

// Fetch from queue, moves to database, sets fields
// FIXME: Add logging
func (s *ServerConfig) claimTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	errMsg := ""

	workerId, err := SafeObjectId(c.Param("workerid"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	// Fetch worker config from DB
	w, err := s.DB.GetWorker(workerId)
	if err != nil {
		errMsg = "Error fetching worker config from database; possible registration error or corrupt worker configuration"
		log.WithFields(log.Fields{
			"err":      err.Error(),
			"workerId": workerId,
		}).Debug(errMsg)
		errMsg = MakeErrorString(fmt.Sprintf("%s :: %s", errMsg, err.Error()))
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// Claim from queue
	var t tasks.Task
	var ackCb func() error
	var nackCb func() error
	t, ackCb, nackCb, err = s.Q.ClaimTask(&w)
	if err != nil {
		if errors.Is(err, queue.ErrQueueEmpty) {
			// Normal polling state — no task for this worker right now.
			c.Status(http.StatusNoContent)
			return
		}
		errMsg = fmt.Sprintf("Problem claiming task :: %s", err.Error())
		c.String(http.StatusNotFound, MakeErrorString(errMsg))
		return
	}

	// Fetch from database to make sure it wasn't STOPPED
	dbt, err := s.DB.GetTask(t.Id)
	if err != nil {
		if _, ok := err.(database.ItemNotFoundError); ok {
			status := http.StatusNotFound
			errMsg = "Could not find task in database, it was likely stopped and deleted"
			if ackErr := ackCb(); ackErr != nil {
				errMsg = fmt.Sprintf("%s: Encountered error while trying to ack task :: %s", errMsg, ackErr.Error())
				status = http.StatusInternalServerError
			}
			c.String(status, MakeErrorString(errMsg))
			return
		}

		errMsg = fmt.Sprintf("Could not fetch task from database to ensure it was not stopped :: %s", err.Error())
		c.String(http.StatusInternalServerError, MakeErrorString(errMsg))
		return
	}

	// Handle tasks that have been canceled when queued
	if dbt.State == "STOPPED" {
		// Need to grab a new one
		errMsg = fmt.Sprintf("Task was stopped")
		if err = ackCb(); err != nil {
			errMsg = fmt.Sprintf("Encountered another error while handling stopped task :: %s", err.Error())
		}
		// FIXME: Maybe return a status code that indicates the worker should try again immediately?
		// Or can actually just fetch again
		// For now we just return a 404 - the worker will try again
		c.String(http.StatusNotFound, MakeErrorString(errMsg))
		return
	}

	// Add fields
	t.State = "CLAIMED"
	t.Progress = 0
	t.LastUpdatedTs = time.Now().Unix()
	t.StartedTs = time.Now().Unix()
	t.WorkerId = workerId
	// Just nil values for these
	// Will be set when transitioning to state RUN
	t.TypeDigest = ""
	t.Pid = 0
	t.Timeout = 0

	// Save to database
	err = s.DB.SaveTask(&t)
	if err != nil {
		errMsg = fmt.Sprintf("Error saving to database :: %s", err.Error())
		err = nackCb()
		if err != nil {
			errMsg += fmt.Sprintf("; Subsequent error returning to queue :: %s", err.Error())
		}
		c.String(http.StatusInternalServerError, MakeErrorString(errMsg))
	} else {
		err = ackCb()
		if err != nil {
			errMsg = fmt.Sprintf("Error acking task in queue after saving to database; task run may be duplicated :: %s", err.Error())
			c.String(http.StatusInternalServerError, MakeErrorString(errMsg))
		} else {
			// Everything is fine
			s.TaskEvents.Notify()
			c.JSON(http.StatusOK, t)
		}
	}
	return
}

// Transition to RUNNING state
// FIXME: Should we set ExecEnv and Tags here?
// - tags should already be set at creation time
// - execEnv should be more dynamic than it is now
func (s *ServerConfig) markTaskAsRunning(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId objectid.ObjectId
	taskId, err = s.getTaskId(c)
	if err != nil {
		return
	}

	// Set fields:
	// state = RUNNING
	// Progress = 0
	// Timeout
	// LastUpdatedTs
	// Pid
	// TypeDigest
	tc := &database.TaskRunConfig{
		Timeout:       cast.ToInt(c.Query("timeout")),
		LastUpdatedTs: time.Now().Unix(),
		Pid:           cast.ToInt(c.Query("pid")),
		TypeDigest:    c.Query("typeDigest"),
	}
	err = s.DB.RunTask(taskId, tc)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	s.TaskEvents.Notify()
	c.JSON(http.StatusOK, "{}")
}

// Called for stopping
func (s *ServerConfig) cancelTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	taskId, err := s.getTaskId(c)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}

	err = s.cancelTaskById(c.Request.Context(), taskId)
	switch {
	case err == nil:
		c.String(http.StatusOK, `{}`)
	case errors.Is(err, ErrTaskNotCancelable):
		// Preserves the historical (non-404, non-2xx) response for a task
		// that exists but can't be canceled from its current state — see
		// docs/next_up.md "Normalize task-handler error status codes".
		c.JSON(http.StatusNotImplemented, `{"error": "Functionality not implemented"}`)
	default:
		c.String(http.StatusNotFound, MakeErrorString(err.Error()))
	}
}

// Set the task to a terminal state like: STOPPING,
func (s *ServerConfig) markTaskAsFinished(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId objectid.ObjectId
	taskId, err = s.getTaskId(c)
	if err != nil {
		return
	}

	// Check that it is a valid task state
	newState := c.Query("state")
	isvalid := false
	for _, s := range tasks.ValidTerminalTaskStates {
		if newState == s {
			isvalid = true
			break
		}
	}
	if !isvalid {
		errMsg := fmt.Sprintf("Invalid task state '%s'; must be one of: %v", newState, tasks.ValidTerminalTaskStates)
		c.String(http.StatusBadRequest, MakeErrorString(errMsg))
		return
	}

	err = s.DB.FinishTask(taskId, newState)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}

	s.TaskEvents.Notify()
	c.JSON(http.StatusOK, "{}")
}

func (s *ServerConfig) updateTaskProgress(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId objectid.ObjectId
	taskId, err = s.getTaskId(c)
	if err != nil {
		return
	}

	// FIXME: Ensure it is in the running state

	progress, err := cast.ToIntE(c.Query("progress"))
	if err != nil || progress > 100 || progress < 0 {
		c.String(http.StatusBadRequest, MakeErrorString("The required parameter 'progress' is not a valid integer between 0 and 100."))
		return
	}

	err = s.DB.UpdateTaskProgress(taskId, progress)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.String(http.StatusOK, "{}")
}

// newTaskForType loads typeName, validates env against its required
// variables, and builds a Task ready to save — but does not save or queue
// it. Split from enqueueTask so postTask can write uploaded files into the
// task's ResultDir before the task becomes visible to workers: calling
// enqueueTask first would let a worker claim and start running before the
// upload finishes.
func (s *ServerConfig) newTaskForType(typeName string, env map[string]string) (tasks.Task, error) {
	tt, err := tasks.FetchTaskType(typeName)
	if err != nil {
		return tasks.Task{}, err
	}

	if len(env) > 0 {
		var missingVars []string
		for varName := range tt.RequiredEnv() {
			if env[varName] == "" {
				missingVars = append(missingVars, varName)
			}
		}
		if len(missingVars) > 0 {
			return tasks.Task{}, fmt.Errorf("missing environment variables required for this task type: %v", missingVars)
		}
	} else if tt.HasRequiredEnv() {
		return tasks.Task{}, fmt.Errorf("the task type %q has required environment variables; 'environment' must be set and contain these values", tt.GetName())
	}

	return tt.NewTask(env)
}

// enqueueTask saves t to the database and pushes it onto the queue, making
// it visible to workers. Call after any pre-run setup (e.g. writing
// uploaded files into t.ResultDir) is complete.
func (s *ServerConfig) enqueueTask(ctx context.Context, t *tasks.Task) error {
	if err := s.DB.SaveTask(t); err != nil {
		return fmt.Errorf("error saving to database: %w", err)
	}
	if err := s.Q.AddTask(t); err != nil {
		return err
	}
	s.TaskEvents.Notify()
	return nil
}

// createTask is newTaskForType + enqueueTask, for callers with no files to
// write in between.
func (s *ServerConfig) createTask(ctx context.Context, typeName string, env map[string]string) (tasks.Task, error) {
	t, err := s.newTaskForType(typeName, env)
	if err != nil {
		return tasks.Task{}, err
	}
	if err := s.enqueueTask(ctx, &t); err != nil {
		return tasks.Task{}, err
	}
	return t, nil
}

// TESTME
// FIXME: Also grab extra tags, e.g. machine specific tag
func (s *ServerConfig) postTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var req map[string]interface{}
	var taskData io.ReadCloser
	var err error

	// FIXME: Save location of these files, will need to move to whatever worker executes this
	// Try to get content from: file, then form value, then body
	// We assume json if not explicitly using a form
	if !strings.Contains(c.Request.Header.Get("Content-Type"), "multipart/form-data") {
		c.Request.Header.Set("Content-Type", "application/json")
	}

	// FIXME: This looks wrong...
	taskData, _, err = c.Request.FormFile("data")
	if err != nil {
		dv := c.Request.FormValue("data")
		if dv != "" {
			taskData = ioutil.NopCloser(strings.NewReader(dv))
		} else {
			taskData = c.Request.Body
		}
	}

	// FIXME: Decode directly to object instead of to map[string]interface{}
	decoder := json.NewDecoder(taskData)
	err = decoder.Decode(&req)
	if err != nil {
		// Getting EOF error unless application/json
		c.String(http.StatusBadRequest, MakeErrorString("Error decoding JSON in request body / form field."))
		return
	}

	// Check required fields
	if req["type"] == nil {
		c.String(http.StatusBadRequest, MakeErrorString("Request is missing required field 'type'."))
		return
	} else if _, ok := req["type"].(string); !ok {
		c.String(http.StatusBadRequest, `{"error": "Required field 'type' is not of expected type 'string'."}`)
		return
	}

	typeName := cast.ToString(req["type"])

	envVars := make(map[string]string)
	if req["environment"] != nil {
		envVars = cast.ToStringMapString(req["environment"])
		if len(envVars) == 0 {
			c.String(http.StatusBadRequest, MakeErrorString("The 'environment' parameter must be a map of string keys to string values."))
			return
		}
	}

	t, err := s.newTaskForType(typeName, envVars)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}

	// Read any uploaded files
	if c.Request.MultipartForm != nil {
		err = os.MkdirAll(t.ResultDir, os.ModePerm)
		if err != nil {
			c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
			return
		}

		for filename := range c.Request.MultipartForm.File {
			if filename == "data" {
				continue
			}

			uploadedFile, _, err := c.Request.FormFile(filename)
			if err != nil {
				c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
				return
			}
			defer uploadedFile.Close()

			writtenUploadedFile, err := os.Create(path.Join(t.ResultDir, filename))
			if err != nil {
				c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
				return
			}
			defer writtenUploadedFile.Close()
			io.Copy(writtenUploadedFile, uploadedFile)
		}
	}

	if err := s.enqueueTask(c.Request.Context(), &t); err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	c.JSON(http.StatusCreated, t)
}

// Always returns 200, even if item doesn't exist
// FIXME: Don't remove task if currently running unless ?force=True
func (s *ServerConfig) removeTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	taskId, err := s.getTaskId(c)
	if err != nil {
		return
	}

	if err := s.removeTaskById(c.Request.Context(), taskId); err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, fmt.Sprintf(`{"id": "%s"}`, taskId.Hex()))
}

// Stream out task log
func (s *ServerConfig) streamTaskLog(c *gin.Context) {
	var err error
	var taskId objectid.ObjectId

	taskId, err = s.getTaskId(c)
	if err != nil {
		return
	}

	var task tasks.Task
	task, err = s.DB.GetTask(taskId)
	if err != nil {
		c.String(http.StatusInternalServerError, "Error fetching information about task while preparing to open logfile stream")
		return
	}

	stdoutPath := path.Join(task.ResultDir, fmt.Sprintf("blanket.stdout.log"))
	sub, err := tailed_file.Follow(stdoutPath)
	if err != nil {
		c.String(http.StatusInternalServerError, "Error opening logfile stream")
		return
	}
	defer sub.Stop()

	// Task is stopped when it is in a terminal state or we get an error fetching its information
	isComplete := func() bool {
		task, err = s.DB.GetTask(taskId)
		if err != nil {
			log.WithFields(log.Fields{
				"taskId":         taskId,
				"subscriptionId": sub.Id,
				"tailedFile":     sub.TailedFile.Filepath,
			}).Error("error refreshing worker state while processing logstreaming request")
			return true
		} else {
			if task.State != "RUNNING" {
				log.WithFields(log.Fields{
					"taskId":         taskId,
					"taskState":      task.State,
					"subscriptionId": sub.Id,
					"tailedFile":     sub.TailedFile.Filepath,
				}).Info("stopping logstreaming request because task is no longer running")
				return true
			}
		}
		return true
	}
	s.streamLog(c, sub, isComplete)
}

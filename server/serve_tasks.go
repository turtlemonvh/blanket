package server

import (
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/gin-gonic/gin"
	"github.com/manucorporat/sse"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/tailed_file"
	"github.com/turtlemonvh/blanket/tasks"
	"gopkg.in/mgo.v2/bson"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

/*
 * Utility functions
 */

// Either gets the task id from a context object or returns an error
// Will also set the response for the request if there was a problem
func getTaskId(c *gin.Context) (bson.ObjectId, error) {
	var err error
	var tid bson.ObjectId

	taskIdStr := c.Param("id")
	if !bson.IsObjectIdHex(taskIdStr) {
		err = fmt.Errorf("'%s' is not not a valid objectid", taskIdStr)
		c.String(http.StatusInternalServerError, fmt.Sprintf(`{"error": "%s"}`, err.Error()))
	} else {
		tid = bson.ObjectIdHex(taskIdStr)
	}

	return tid, err
}

/*
 * Request handlers
 */

func getTasks(c *gin.Context) {
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

	result := []tasks.Task{}
	nfound := 0
	var err error

	// FIXME: Cleanup, parallelize, merge
	// Need to decide how we want to handle the display of queued tasks
	searchDB := true

	ntt := len(tc.AllowedTaskTypes)
	if ntt == 0 || tc.AllowedTaskTypes["WAITING"] {
		var qresult []tasks.Task
		var nfoundq int
		searchDB = ntt == 0 || ntt > 1
		qresult, nfoundq, err = Q.ListTasks(tc.RequiredTags, tc.Limit)
		if err != nil {
			c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
			return
		}
		result = append(result, qresult...)
		nfound += nfoundq
	}

	if searchDB {
		var dbresult []tasks.Task
		var nfounddb int
		dbresult, nfounddb, err = DB.GetTasks(tc)
		if err != nil {
			c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
			return
		}
		result = append(result, dbresult...)
		nfound += nfounddb
	}

	if tc.JustCounts {
		c.String(http.StatusOK, cast.ToString(nfound))
	} else {
		c.JSON(http.StatusOK, result)
	}
}

func getTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId bson.ObjectId

	taskId, err = getTaskId(c)
	if err != nil {
		return
	}

	var task tasks.Task
	task, err = DB.GetTask(taskId)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	c.JSON(http.StatusOK, task)
}

// Fetch from queue, moves to database, sets fields
// FIXME: Add logging
func claimTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	errMsg := ""

	workerId, err := SafeObjectId(c.Param("workerid"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	// Fetch worker config from DB
	// Problem: worker id is a pid now
	w, err := DB.GetWorker(workerId)
	if err != nil {
		errMsg = "Error fetching worker config from database; possible registration error or corrupt worker configuration"
		log.WithFields(log.Fields{
			"err":      err.Error(),
			"workerId": workerId,
		}).Debug(errMsg)
		errMsg = MakeErrorString(errMsg + fmt.Sprintf(":: %s", err.Error()))
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// Claim from queue
	var t tasks.Task
	var ackCb func() error
	var nackCb func() error
	t, ackCb, nackCb, err = Q.ClaimTask(&w)
	if err != nil {
		// FIXME: Return 404 if a not found error, 400 for other errors
		// Task could not be found, probably
		errMsg = fmt.Sprintf("Problem claiming task :: %s", err.Error())
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

	// FIXME: Check if task has been canceled
	// http://stackoverflow.com/questions/3758576/how-to-retract-a-message-in-rabbitmq
	// There will be a tombstone in the database marking that task as canceled
	// We will just want to merge details in the database, because all we'll have when canceling is a taskId
	// - we want to do this in the db inside a transaction
	// - we do not want to nack the task if this happens, so we need to distinguish between a error to save and a 'stopped' error

	// Save to database
	err = DB.StartTask(&t)
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
			c.JSON(http.StatusOK, t)
		}
	}
	return
}

// Transition to RUNNING state
// FIXME: Should we set ExecEnv and Tags here?
// - tags should already be set at creation time
// - execEnv should be more dynamic than it is now
func runTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId bson.ObjectId
	taskId, err = getTaskId(c)
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
	err = DB.RunTask(taskId, tc)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	c.JSON(http.StatusOK, "{}")
}

// FIXME: Implement this
// Called for stopping
func cancelTask(c *gin.Context) {
	// Upsert in database, setting any item that has that Id to STOPPED state
	// If it doesn't exist, the 'tombstone' will just have the taskId and state=STOPPED
	c.JSON(http.StatusInternalServerError, "{}")
}

// Set the task to a terminal state like: STOPPING,
func finishTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId bson.ObjectId
	taskId, err = getTaskId(c)
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

	err = DB.FinishTask(taskId, newState)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}

	c.JSON(http.StatusOK, "{}")
}

func updateTaskProgress(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId bson.ObjectId
	taskId, err = getTaskId(c)
	if err != nil {
		return
	}

	// FIXME: Ensure it is in the running state

	progress, err := cast.ToIntE(c.Query("progress"))
	if err != nil || progress > 100 || progress < 0 {
		c.String(http.StatusBadRequest, MakeErrorString("The required parameter 'progress' is not a valid integer between 0 and 100."))
		return
	}

	err = DB.UpdateTaskProgress(taskId, progress)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.String(http.StatusOK, "{}")
}

// TESTME
// FIXME: Also grab extra tags, e.g. machine specific tag
func postTask(c *gin.Context) {
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

	// Load task type
	tt, err := tasks.FetchTaskType(cast.ToString(req["type"]))
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}

	// Load environment variables
	envVars := make(map[string]string)
	if req["environment"] != nil {
		envVars = cast.ToStringMapString(req["environment"])
		if len(envVars) == 0 {
			c.String(http.StatusBadRequest, MakeErrorString("The 'environment' parameter must be a map of string keys to string values."))
			return
		}

		// Check that required variables are set
		var missingVars []string
		for varName, _ := range tt.RequiredEnv() {
			if envVars[varName] == "" {
				missingVars = append(missingVars, varName)
			}

			// FIXME: Check types of variables, maybe by checking that they can be cast to that type then back to string with no loss
		}
		if len(missingVars) > 0 {
			errMsg := fmt.Sprintf("Missing environment variables required for this task type: %s", missingVars)
			c.String(http.StatusBadRequest, MakeErrorString(errMsg))
			return
		}

	} else if tt.HasRequiredEnv() {
		// Environment not set but we have required fields
		errMsg := fmt.Sprintf("The task type '%s' has required environment variables. The 'environment' parameter must be set and contain these values.", tt.GetName())
		c.String(http.StatusBadRequest, MakeErrorString(errMsg))
		return
	}

	// Create task object
	t, err := tt.NewTask(envVars)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}

	// Read any uploaded files
	if c.Request.MultipartForm != nil {
		// Create output dir to put files in
		err = os.MkdirAll(t.ResultDir, os.ModePerm)
		if err != nil {
			c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
			return
		}

		for filename, _ := range c.Request.MultipartForm.File {
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
			defer writtenUploadedFile.Close()
			io.Copy(writtenUploadedFile, uploadedFile)
		}
	}

	// Add to queue
	err = Q.AddTask(&t)
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	c.JSON(http.StatusCreated, t)
}

// Always returns 200, even if item doesn't exist
// FIXME: Don't remove task if currently running unless ?force=True
func removeTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId bson.ObjectId

	taskId, err = getTaskId(c)
	if err != nil {
		return
	}

	err = DB.DeleteTask(taskId)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// Remove result directory
	// FIXME: Grab from json instead
	err = os.RemoveAll(path.Join(viper.GetString("tasks.resultsPath"), taskId.Hex()))
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, fmt.Sprintf(`{"id": "%s"}`, taskId.Hex()))
}

func streamTaskLog(c *gin.Context) {
	var err error
	var taskId bson.ObjectId

	taskId, err = getTaskId(c)
	if err != nil {
		return
	}

	var task tasks.Task
	task, err = DB.GetTask(taskId)
	if err != nil {
		c.String(http.StatusInternalServerError, "Error opening logfile stream")
		return
	}

	stdoutPath := path.Join(task.ResultDir, fmt.Sprintf("blanket.stdout.log"))
	sub, err := tailed_file.Follow(stdoutPath)
	if err != nil {
		c.String(http.StatusInternalServerError, "Error opening logfile stream")
		return
	}
	defer sub.Stop()

	loglineChannelIsEmpty := false
	lineno := 1
	c.Stream(func(w io.Writer) bool {
		// This function returns a boolean indicating whether the stream should stay open
		// Every time this is called, also checks if client has left

		// Wait up to 5 seconds for a new value
		select {
		case logline := <-sub.NewLines:
			// Send event with message content
			c.Render(-1, sse.Event{
				Id:    strconv.Itoa(lineno),
				Event: "message",
				Data:  logline,
			})
			lineno++
			loglineChannelIsEmpty = false
		case <-time.After(5 * time.Second):
			loglineChannelIsEmpty = true
		}

		// If we have emptied the channel, decide whether to stop sending data
		if loglineChannelIsEmpty {
			// Check whether the process is complete
			// If so, return false so we quit streaming
			task, err = DB.GetTask(taskId)
			if err != nil {
				log.WithFields(log.Fields{
					"taskId":         taskId,
					"subscriptionId": sub.Id,
					"tailedFile":     sub.TailedFile.Filepath,
				}).Error("error refreshing worker state while processing logstreaming request")
			} else {
				if task.State != "RUNNING" {
					log.WithFields(log.Fields{
						"taskId":         taskId,
						"taskState":      task.State,
						"subscriptionId": sub.Id,
						"tailedFile":     sub.TailedFile.Filepath,
					}).Info("stopping logstreaming request because task is no longer running")
					return false
				}
			}
		}

		return true
	})

}

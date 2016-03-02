package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/boltdb/bolt"
	"github.com/gin-gonic/gin"
	"github.com/manucorporat/sse"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
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

const (
	FAR_FUTURE_SECONDS = int64(60 * 60 * 24 * 365 * 100)
)

/*
 * Utility functions
 */

// Gets the full byte representation of the objectid
// Errors are ignored because just casting a string object to a byte slice will never result in an error
func IdBytes(id bson.ObjectId) []byte {
	bts, _ := id.MarshalJSON()
	return bts
}

func fetchTaskBucket(tx *bolt.Tx) (b *bolt.Bucket, err error) {
	b = tx.Bucket([]byte("tasks"))
	if b == nil {
		err = fmt.Errorf("Database format error: Bucket 'tasks' does not exist.")
	}
	return
}

func fetchTaskFromBucket(taskId *bson.ObjectId, b *bolt.Bucket) (t tasks.Task, err error) {
	result := b.Get(IdBytes(*taskId))
	err = json.Unmarshal(result, &t)
	return
}

func fetchTaskById(taskId bson.ObjectId) (tasks.Task, error) {
	var err error
	task := tasks.Task{}
	err = DB.View(func(tx *bolt.Tx) error {
		b, err := fetchTaskBucket(tx)
		if err != nil {
			return err
		}
		task, err = fetchTaskFromBucket(&taskId, b)
		return nil
	})
	return task, err
}

func saveTaskToBucket(t tasks.Task, b *bolt.Bucket) (err error) {
	js, err := t.ToJSON()
	if err != nil {
		return err
	}

	err = b.Put(IdBytes(t.Id), []byte(js))
	if err != nil {
		return err
	}
	return nil
}

func modifyTaskInTransaction(taskId *bson.ObjectId, f func(t *tasks.Task) error) error {
	err := DB.Update(func(tx *bolt.Tx) error {
		bucket, err := fetchTaskBucket(tx)
		if err != nil {
			return err
		}
		t, err := fetchTaskFromBucket(taskId, bucket)
		if err != nil {
			return err
		}

		// Main function; accepts a task object and can perform checks and modify it
		err = f(&t)
		if err != nil {
			return err
		} else {
			t.LastUpdatedTs = time.Now().Unix()
		}

		err = saveTaskToBucket(t, bucket)
		if err != nil {
			return err
		}
		return nil
	})
	return err
}

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

// Return just the keys for a map
func MapKeys(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

/*
 * Request handlers
 */

type TaskSearchConf struct {
	limit             int
	offset            int
	reverseSort       bool
	requiredTags      []string
	maxTags           []string
	smallestId        bson.ObjectId
	largestId         bson.ObjectId
	allowedTaskStates map[string]bool
	allowedTaskTypes  map[string]bool
}

func TaskSearchConfFromContext(c *gin.Context) *TaskSearchConf {
	tc := &TaskSearchConf{}

	tc.limit = cast.ToInt(c.Query("limit"))
	tc.offset = cast.ToInt(c.Query("offset"))

	// Default values for limit and offset
	if tc.limit < 1 {
		tc.limit = 500
	}
	if tc.offset < 0 {
		tc.offset = 0
	}

	tc.reverseSort = c.Query("reverseSort") == "true"

	// Should be unix timestamps, in seconds
	startTimeSent := c.Query("createdAfter")
	endTimeSent := c.Query("createdBefore")

	startTime := time.Unix(0, 0)
	endTime := time.Unix(FAR_FUTURE_SECONDS, 0)
	startTimeSentInt, err := strconv.ParseInt(startTimeSent, 10, 64)
	if err == nil {
		startTime = time.Unix(startTimeSentInt, 0)
	}
	endTimeSentInt, err := strconv.ParseInt(endTimeSent, 10, 64)
	if err == nil {
		endTime = time.Unix(endTimeSentInt, 0)
	}
	tc.smallestId = bson.NewObjectIdWithTime(startTime)
	tc.largestId = bson.NewObjectIdWithTime(endTime)

	// Filtering based on tags, states, types
	tags := c.Query("requiredTags")
	if tags != "" {
		tc.requiredTags = strings.Split(tags, ",")
	}

	maxTags := c.Query("maxTags")
	if maxTags != "" {
		tc.maxTags = strings.Split(maxTags, ",")
	}

	sentAllowedStates := c.Query("states")
	tc.allowedTaskStates = make(map[string]bool)
	if sentAllowedStates != "" {
		for _, tstate := range strings.Split(sentAllowedStates, ",") {
			tc.allowedTaskStates[tstate] = true
		}
	}

	sentAllowedTypes := c.Query("types")
	tc.allowedTaskTypes = make(map[string]bool)
	if sentAllowedTypes != "" {
		for _, ttype := range strings.Split(sentAllowedTypes, ",") {
			tc.allowedTaskTypes[ttype] = true
		}
	}

	return tc
}

func getTasks(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	tc := TaskSearchConfFromContext(c)

	justCounts := c.Query("count") == "true"

	log.WithFields(log.Fields{
		"requiredTaskTags": tc.requiredTags,
		"maxTaskTags":      tc.maxTags,
		"taskTypes":        MapKeys(tc.allowedTaskTypes),
		"taskStates":       MapKeys(tc.allowedTaskStates),
		"limit":            tc.limit,
		"smallestId":       tc.smallestId.Hex(),
		"largestId":        tc.largestId.Hex(),
		"justCounts":       justCounts,
	}).Debug("Task request params")

	result := "["
	nfound := 0
	if err := DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			errorString := "Database format error: Bucket 'tasks' does not exist."
			return fmt.Errorf(errorString)
		}

		c := b.Cursor()
		isFirst := true

		// Sort order
		var (
			checkFunction func(bts []byte) bool
			k             []byte
			v             []byte
			iterFunction  func() ([]byte, []byte)
			endBytes      []byte
		)
		if tc.reverseSort {
			// Have to just jump to the end, since seeking to a far future key goes to the end
			// Seek only goes in 1 order
			// Seek manually to the highest value
			for k, v = c.Last(); k != nil && bytes.Compare(k, IdBytes(tc.largestId)) >= 0; k, v = c.Prev() {
				continue
			}
			iterFunction = c.Prev
			endBytes = IdBytes(tc.smallestId)
			checkFunction = func(bts []byte) bool {
				return k != nil && bytes.Compare(k, endBytes) >= 0
			}
		} else {
			// Normal case
			k, v = c.Seek(IdBytes(tc.smallestId))
			iterFunction = c.Next
			endBytes = IdBytes(tc.largestId)
			checkFunction = func(bts []byte) bool {
				return k != nil && bytes.Compare(k, endBytes) <= 0
			}
		}

		for ; checkFunction(k); k, v = iterFunction() {
			// e.g. 50-40 == 10
			if nfound-tc.offset == tc.limit {
				break
			}

			// Create an object from bytes
			t := tasks.Task{}
			json.Unmarshal(v, &t)

			// Filter results
			if len(tc.allowedTaskTypes) != 0 && !tc.allowedTaskTypes[t.TypeId] {
				continue
			}
			if len(tc.allowedTaskStates) != 0 && !tc.allowedTaskStates[t.State] {
				continue
			}

			// All tags in tc.requiredTags must be present on every task
			if len(tc.requiredTags) > 0 {
				hasTags := true
				for _, requestedTag := range tc.requiredTags {
					found := false
					for _, existingTag := range t.Tags {
						if requestedTag == existingTag {
							found = true
						}
					}
					if !found {
						hasTags = false
						break
					}
				}
				if !hasTags {
					continue
				}
			}

			// All tags on each task must be present in tc.maxTags
			if len(tc.maxTags) > 0 {
				taskHasExtraTags := false
				for _, existingTag := range t.Tags {
					found := false
					for _, allowedTag := range tc.maxTags {
						if allowedTag == existingTag {
							found = true
						}
					}
					if !found {
						taskHasExtraTags = true
						break
					}
				}
				if taskHasExtraTags {
					continue
				}
			}

			// Keep track of found items, and build string that will be returned
			nfound += 1
			if nfound > tc.offset {
				if !justCounts {
					if !isFirst {
						result += ","
					}
					// FIXME: Return this in chunks
					result += string(v)
				}
				isFirst = false
			}
		}

		return nil
	}); err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}
	result += "]"

	if justCounts {
		c.String(http.StatusOK, cast.ToString(nfound))
	} else {
		c.String(http.StatusOK, result)
	}
}

// Doesn't read task in; assumes value is valid JSON for speed
func getTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId bson.ObjectId

	taskId, err = getTaskId(c)
	if err != nil {
		return
	}

	result := ""
	err = DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			errorString := "Database format error: Bucket 'tasks' does not exist."
			return fmt.Errorf(errorString)
		}
		result += string(b.Get(IdBytes(taskId)))
		return nil
	})
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, result)
}

/*
All updates must happen in a single transaction
*/
func updateTaskState(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId bson.ObjectId

	taskId, err = getTaskId(c)
	if err != nil {
		return
	}

	newState := c.Query("state")
	typeDigest := c.Query("typeDigest")
	pid := c.Query("pid")
	workerId := c.Query("workerId")
	timeout := c.Query("timeout")

	if _, err = cast.ToIntE(timeout); err != nil {
		timeout = cast.ToString(tasks.DEFAULT_TIMEOUT)
	}

	validState := false
	for _, state := range tasks.ValidTaskStates {
		if state == newState {
			validState = true
			break
		}
	}
	if !validState {
		errMsg := fmt.Sprintf(`{"error": "'%s' is not a valid task state"}`, newState)
		c.String(http.StatusBadRequest, errMsg)
		return
	}

	err = modifyTaskInTransaction(&taskId, func(t *tasks.Task) error {
		// Perform some checks that this is a valid transition
		switch newState {
		case "START":
			if t.State != "WAIT" {
				return fmt.Errorf("Cannot transition to START state from state %s", t.State)
			}
			t.StartedTs = time.Now().Unix()
			t.TypeDigest = typeDigest
			t.Progress = 0
			t.WorkerId = workerId
			t.Timeout = int64(cast.ToInt(timeout))
		case "WAIT":
			// FIXME: Can go back to WAIT after START or RUNNING if requeued
			return fmt.Errorf("Cannot transition to WAIT state from any other state")
		case "RUNNING":
			if t.State != "START" {
				return fmt.Errorf("Cannot transition to RUNNING state from state %s", t.State)
			}
			t.Pid = cast.ToInt(pid)
		case "ERROR":
			if t.State != "RUNNING" {
				return fmt.Errorf("Cannot transition to ERROR state from state %s", t.State)
			}
		case "STOPPED":
			if t.State != "RUNNING" {
				return fmt.Errorf("Cannot transition to STOPPED state from state %s", t.State)
			}
		case "TIMEOUT":
			if t.State != "RUNNING" {
				return fmt.Errorf("Cannot transition to TIMEOUT state from state %s", t.State)
			}
		case "SUCCESS":
			if t.State != "RUNNING" {
				return fmt.Errorf("Cannot transition to SUCCESS state from state %s", t.State)
			}
			t.Progress = 100
		}
		t.State = newState
		return nil
	})
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}
	c.String(http.StatusOK, "{}")
}

func updateTaskProgress(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskId bson.ObjectId

	taskId, err = getTaskId(c)
	if err != nil {
		return
	}

	progress := c.Query("progress")
	iProgress, err := cast.ToIntE(progress)
	if err != nil || iProgress > 100 || iProgress < 0 {
		errMsg := fmt.Sprintf(`{"error": "The required parameter 'progress' is not a valid integer between 0 and 100."}`)
		c.String(http.StatusBadRequest, errMsg)
		return
	}

	err = modifyTaskInTransaction(&taskId, func(t *tasks.Task) error {
		t.Progress = iProgress
		return nil
	})
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}
	c.String(http.StatusOK, "{}")
}

// FIXME: Also grab extra tags
func postTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var req map[string]interface{}
	var taskData io.ReadCloser
	var err error

	// Try to get content from: file, then form value, then body
	// We assume json if not explicitly using a form
	if !strings.Contains(c.Request.Header.Get("Content-Type"), "multipart/form-data") {
		c.Request.Header.Set("Content-Type", "application/json")
	}
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
		c.String(http.StatusBadRequest, `{"error": "Error decoding JSON in request body / form field."}`)
		return
	}

	// Check required fields
	if req["type"] == nil {
		c.String(http.StatusBadRequest, `{"error": "Request is missing required field 'type'."}`)
		return
	} else if _, ok := req["type"].(string); !ok {
		c.String(http.StatusBadRequest, `{"error": "Required field 'type' is not of expected type 'string'."}`)
		return
	}

	// Load task type
	filename := fmt.Sprintf("%s.toml", req["type"])
	fullpath := path.Join(viper.GetString("tasks.typesPath"), filename)
	tt, err := tasks.ReadTaskTypeFromFilepath(fullpath)
	if err != nil {
		c.String(http.StatusBadRequest, fmt.Sprintf(`{"error": "%s"}`, err.Error()))
		return
	}

	// Load environment variables
	envVars := make(map[string]string)
	if req["environment"] != nil {
		envVars = cast.ToStringMapString(req["environment"])
		if len(envVars) == 0 {
			c.String(http.StatusBadRequest, `{"error": "The 'environment' parameter must be a map of string keys to string values."}`)
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
			c.String(http.StatusBadRequest, fmt.Sprintf(`{"error": "Missing environment variables required for this task type: %s"}`, missingVars))
			return
		}

	} else if tt.HasRequiredEnv() {
		// Environment not set but we have required fields
		c.String(http.StatusBadRequest, fmt.Sprintf(`{"error": "The task type '%s' has required environment variables. The 'environment' parameter must be set and contain these values."}`, tt.GetName()))
		return
	}

	// Create task object
	t, err := tt.NewTask(envVars)
	if err != nil {
		c.String(http.StatusBadRequest, fmt.Sprintf(`{"error": "%s"}`, err.Error()))
		return
	}

	// Read any uploaded files
	if c.Request.MultipartForm != nil {
		// Create output dir to put files in
		err = os.MkdirAll(t.ResultDir, os.ModePerm)
		if err != nil {
			c.String(http.StatusBadRequest, fmt.Sprintf(`{"error": "%s"}`, err.Error()))
			return
		}

		for filename, _ := range c.Request.MultipartForm.File {
			if filename == "data" {
				continue
			}

			uploadedFile, _, err := c.Request.FormFile(filename)
			if err != nil {
				c.String(http.StatusBadRequest, fmt.Sprintf(`{"error": "%s"}`, err.Error()))
				return
			}
			defer uploadedFile.Close()

			writtenUploadedFile, err := os.Create(path.Join(t.ResultDir, filename))
			defer writtenUploadedFile.Close()
			io.Copy(writtenUploadedFile, uploadedFile)
		}
	}

	// Save task to database
	if err = DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			errorString := "Database format error: Bucket 'tasks' does not exist."
			return fmt.Errorf(errorString)
		}
		jsn, err := json.Marshal(t)
		if err != nil {
			return err
		}
		b.Put(IdBytes(t.Id), jsn)
		return nil

	}); err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusCreated, fmt.Sprintf(`{"id": "%s"}`, t.Id.Hex()))
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

	err = DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			errorString := "Database format error: Bucket 'tasks' does not exist."
			return fmt.Errorf(errorString)
		}
		err := b.Delete(IdBytes(taskId))
		return err
	})
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

	task := tasks.Task{}
	task, err = fetchTaskById(taskId)
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

		// FIXME: This has the potential to generate one goroutine per line, maxing out 1 goroutine per line we can read in 5 seconds
		timeout := make(chan bool, 1)
		go func() {
			time.Sleep(5 * time.Second)
			timeout <- true
		}()

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
		case <-timeout:
			loglineChannelIsEmpty = true
		}

		// If we have emptied the channel, decide whether to stop sending data
		if loglineChannelIsEmpty {
			// Check whether the process is complete
			// If so, return false so we quit streaming
			task, err = fetchTaskById(taskId)
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

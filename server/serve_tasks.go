package server

import (
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/boltdb/bolt"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	uuid "github.com/streadway/simpleuuid"
	"github.com/turtlemonvh/blanket/tasks"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

func getTasks(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	// Tags that each task must contain
	var requiredTaskTags []string
	tags := c.Query("requiredTags")
	if tags != "" {
		requiredTaskTags = strings.Split(tags, ",")
	}

	// The total set of tags each task is allowed to contain
	var maxTaskTags []string
	maxTags := c.Query("maxTags")
	if maxTags != "" {
		maxTaskTags = strings.Split(maxTags, ",")
	}

	limit := cast.ToInt(c.Query("limit"))
	offset := cast.ToInt(c.Query("offset"))
	taskType := c.Query("type")
	taskState := c.Query("state")
	reverseSort := c.Query("reverseSort")

	// FIXME: Range queries

	if limit < 1 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}

	log.WithFields(log.Fields{
		"requiredTaskTags": requiredTaskTags,
		"maxTaskTags":      maxTaskTags,
		"taskType":         taskType,
		"taskState":        taskState,
		"limit":            limit,
	}).Debug("Task request params")

	result := "["
	if err := DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			errorString := "Database format error: Bucket 'tasks' does not exist."
			return fmt.Errorf(errorString)
		}

		c := b.Cursor()
		isFirst := true

		// Sort order
		iterFunction := c.Next
		startIterFunction := c.First
		if reverseSort == "true" {
			iterFunction = c.Prev
			startIterFunction = c.Last
		}

		nfound := 0
		for k, v := startIterFunction(); k != nil; k, v = iterFunction() {
			// e.g. 50-40 == 10
			if nfound-offset == limit {
				break
			}

			// Create an object from bytes
			t := tasks.Task{}
			json.Unmarshal(v, &t)

			// Filter results
			if taskType != "" && t.TypeId != taskType {
				continue
			}
			if taskState != "" && t.State != taskState {
				continue
			}

			// all tags in requiredTaskTags must be present on every task
			if len(requiredTaskTags) > 0 {
				hasTags := true
				for _, requestedTag := range requiredTaskTags {
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

			// all tags on each task must be present in maxTaskTags
			if len(maxTaskTags) > 0 {
				taskHasExtraTags := false
				for _, existingTag := range t.Tags {
					found := false
					for _, allowedTag := range maxTaskTags {
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
			// FIXME: Return this in chunks
			nfound += 1
			if nfound > offset {
				if !isFirst {
					result += ","
				}
				result += string(v)
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

	c.String(http.StatusOK, result)
}

// Doesn't read task in; assumes value is valid JSON for speed
func getTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskUUID uuid.UUID

	taskId := c.Param("id")
	taskUUID, err = uuid.NewString(taskId)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	result := ""
	err = DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			errorString := "Database format error: Bucket 'tasks' does not exist."
			return fmt.Errorf(errorString)
		}

		// FIXME: May need to unmarshall then remarshall because of id
		result += string(b.Get(taskUUID.Bytes()))

		return nil
	})
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, result)
}

func fetchTaskBucket(tx *bolt.Tx) (b *bolt.Bucket, err error) {
	b = tx.Bucket([]byte("tasks"))
	if b == nil {
		err = fmt.Errorf("Database format error: Bucket 'tasks' does not exist.")
	}
	return
}

func fetchTaskFromBucket(taskId *uuid.UUID, b *bolt.Bucket) (t tasks.Task, err error) {
	result := b.Get(taskId.Bytes())
	err = json.Unmarshal(result, &t)
	return
}

func saveTaskToBucket(t tasks.Task, b *bolt.Bucket) (err error) {
	js, err := t.ToJSON()
	if err != nil {
		return err
	}
	err = b.Put(t.Id.Bytes(), []byte(js))
	if err != nil {
		return err
	}
	return nil
}

func modifyTaskInTransaction(taskId *uuid.UUID, f func(t *tasks.Task) error) error {
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

/*
All updates must happen in a single transaction

e.g. for a worker to start work
- find a valid task
- mark it in progress
- save it
~~~
- that all has to happen in 1 step

For other updates it is less crucial.

*/
func updateTaskState(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskUUID uuid.UUID

	taskId := c.Param("id")
	taskUUID, err = uuid.NewString(taskId)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	newState := c.Query("state")
	typeDigest := c.Query("typeDigest")

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

	err = modifyTaskInTransaction(&taskUUID, func(t *tasks.Task) error {
		// Perform some checks that this is a valid transition
		switch newState {
		case "START":
			if t.State != "WAIT" {
				return fmt.Errorf("Cannot transition to START state from state %s", t.State)
			}
			t.StartedTs = time.Now().Unix()
			t.TypeDigest = typeDigest
			t.Progress = 0
		case "WAIT":
			// FIXME: Can go back to WAIT after START or RUNNING if requeued
			return fmt.Errorf("Cannot transition to WAIT state from any other state")
		case "RUNNING":
			if t.State != "START" {
				return fmt.Errorf("Cannot transition to RUNNING state from state %s", t.State)
			}
		case "ERROR":
			if t.State != "RUNNING" {
				return fmt.Errorf("Cannot transition to ERROR state from state %s", t.State)
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
	var taskUUID uuid.UUID

	taskId := c.Param("id")
	taskUUID, err = uuid.NewString(taskId)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	progress := c.Query("progress")
	iProgress, err := cast.ToIntE(progress)
	if err != nil || iProgress > 100 || iProgress < 0 {
		errMsg := fmt.Sprintf(`{"error": "The required parameter 'progress' is not a valid integer between 0 and 100."}`)
		c.String(http.StatusBadRequest, errMsg)
		return
	}

	err = modifyTaskInTransaction(&taskUUID, func(t *tasks.Task) error {
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
	fullpath := path.Join(viper.GetString("tasks.types_path"), filename)
	tt, err := tasks.ReadTaskTypeFromFilepath(fullpath)
	if err != nil {
		c.String(http.StatusBadRequest, fmt.Sprintf(`{"error": "%s"}`, err.Error()))
		return
	}

	// Load environment variables
	var defaultEnv map[string]string
	if req["environment"] != nil {
		defaultEnv = cast.ToStringMapString(req["environment"])
		if len(defaultEnv) == 0 {
			log.WithFields(log.Fields{
				"environment": req["environment"],
			}).Error("environment is not a map[string]string")
			c.String(http.StatusBadRequest, `{"error": "The 'environment' parameter must be a map of string keys to string values."}`)
			return
		}

		// FIXME: Check that required variables are set
	}

	// Create task object
	t, err := tt.NewTask(defaultEnv)
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
		b.Put(t.Id.Bytes(), jsn)
		return nil
	}); err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusCreated, fmt.Sprintf(`{"id": "%s"}`, t.Id))
}

// Always returns 200, even if item doesn't exist
// FIXME: Remove directory, don't remove if currently running unless ?force=True
func removeTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	var err error
	var taskUUID uuid.UUID

	taskId := c.Param("id")
	taskUUID, err = uuid.NewString(taskId)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	err = DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			errorString := "Database format error: Bucket 'tasks' does not exist."
			return fmt.Errorf(errorString)
		}
		err := b.Delete(taskUUID.Bytes())
		return err
	})
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// Remove result directory
	// FIXME: Grab from json instead
	err = os.RemoveAll(path.Join(viper.GetString("tasks.results_path"), taskId))
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, fmt.Sprintf(`{"id": "%s"}`, taskId))
}

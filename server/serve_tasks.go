package server

import (
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/boltdb/bolt"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/tasks"
	"net/http"
	"path"
	"strings"
)

func getTasks(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	result := "["

	// Get information for limiting response
	// tags, type, state
	var taskTags []string
	tags := c.Query("tags")
	if tags != "" {
		taskTags = strings.Split(tags, ",")
	}
	taskType := c.Query("type")
	taskState := c.Query("state")

	log.WithFields(log.Fields{
		"params":    c.Request.URL.Query(),
		"taskTags":  taskTags,
		"taskType":  taskType,
		"taskState": taskState,
	}).Info("Request params")

	if err := DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			errorString := "Database format error: Bucket 'tasks' does not exist."
			return fmt.Errorf(errorString)
		}

		c := b.Cursor()
		isFirst := true
		for k, v := c.First(); k != nil; k, v = c.Next() {

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

			if len(taskTags) > 0 {
				// all tags must match
				hasTags := true
				for _, requestedTag := range taskTags {
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

			if !isFirst {
				result += ","
			}
			result += string(v)
			isFirst = false
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

func getTask(c *gin.Context) {
	taskId := c.Param("id")
	c.Header("Content-Type", "application/json")

	result := ""
	if err := DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			errorString := "Database format error: Bucket 'tasks' does not exist."
			return fmt.Errorf(errorString)
		}
		result += string(b.Get([]byte(taskId)))
		return nil
	}); err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, result)
}

// FIXME: Also grab tags, files
func postTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	// Read in request body, check for validity, and add to database

	// Look for type and default env
	decoder := json.NewDecoder(c.Request.Body)
	var req map[string]interface{}
	err := decoder.Decode(&req)
	if err != nil {
		c.String(http.StatusBadRequest, `{"error": "Error decoding JSON in request body."}`)
		return
	}

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
	if req["defaultEnv"] != nil {
		defaultEnv = cast.ToStringMapString(req["defaultEnv"])
		if len(defaultEnv) == 0 {
			log.WithFields(log.Fields{"defaultEnv": req["defaultEnv"]}).Info("defaultEnv is not a map[string]string")
			c.String(http.StatusBadRequest, `{"error": "The 'defaultEnv' parameter must be a map of string keys to string values."}`)
			return
		}
	}

	log.WithFields(log.Fields{
		"defaultEnv":     defaultEnv,
		"req.defaultEnv": req["defaultEnv"],
	}).Info("Environment variable mixing in request")

	t, err := tt.NewTask(defaultEnv)
	if err != nil {
		c.String(http.StatusBadRequest, fmt.Sprintf(`{"error": "%s"}`, err.Error()))
		return
	}

	if err = DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			errorString := "Database format error: Bucket 'tasks' does not exist."
			return fmt.Errorf(errorString)
		}
		js, err := t.ToJSON()
		if err != nil {
			return err
		}
		b.Put([]byte(t.Id), []byte(js))
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
	taskId := c.Param("id")
	c.Header("Content-Type", "application/json")

	if err := DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			errorString := "Database format error: Bucket 'tasks' does not exist."
			return fmt.Errorf(errorString)
		}
		err := b.Delete([]byte(taskId))
		return err
	}); err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}
	c.String(http.StatusOK, fmt.Sprintf(`{"id": "%s"}`, taskId))
}

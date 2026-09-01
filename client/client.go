package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/turtlemonvh/blanket/tasks"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

/*
The client package provides utilities for working with a running blanket server over HTTP
*/

type GetTasksConf struct {
	All          bool
	States       string
	Types        string
	RequiredTags string
	MaxTags      string
	Limit        int
	ParsedTags   []string
}

func GetTasks(c *GetTasksConf, port int) ([]map[string]interface{}, error) {
	var tasks []map[string]interface{}

	v := url.Values{}
	if c.States != "" {
		v.Set("states", strings.ToUpper(c.States))
	}
	if c.Types != "" {
		v.Set("types", c.Types)
	}
	if c.RequiredTags != "" {
		v.Set("requiredTags", c.RequiredTags)
	}
	if c.MaxTags != "" {
		v.Set("maxTags", c.MaxTags)
	}
	v.Set("limit", strconv.Itoa(c.Limit))

	paramsString := v.Encode()
	reqURL := fmt.Sprintf("http://localhost:%d/task/", port)
	if paramsString != "" {
		reqURL += "?" + paramsString
	}
	res, err := http.Get(reqURL)
	if err != nil {
		return tasks, err
	}

	defer res.Body.Close()

	// FIXME: Encode as task objects instead
	dec := json.NewDecoder(res.Body)
	dec.Decode(&tasks)

	return tasks, nil
}

// GetActiveWorkerTagSets fetches the tag set of every non-stopped worker
// registered with the server at the given port. Used by
// `blanket task-validate --check-workers` (code 014) to check whether any
// worker could claim a given task type.
func GetActiveWorkerTagSets(port int) ([][]string, error) {
	reqURL := fmt.Sprintf("http://localhost:%d/worker/", port)
	res, err := http.Get(reqURL)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	var workers []struct {
		Tags    []string `json:"tags"`
		Stopped bool     `json:"stopped"`
	}
	if err := json.NewDecoder(res.Body).Decode(&workers); err != nil {
		return nil, err
	}

	sets := make([][]string, 0, len(workers))
	for _, w := range workers {
		if w.Stopped {
			continue
		}
		sets = append(sets, w.Tags)
	}
	return sets, nil
}

func SubmitTask(taskType string, env map[string]interface{}, port int) (tasks.Task, error) {
	var t tasks.Task

	body := make(map[string]interface{})
	body["type"] = taskType
	body["environment"] = env

	bts, err := json.Marshal(body)
	if err != nil {
		return t, err
	}

	reqURL := fmt.Sprintf("http://localhost:%d/task/", port)
	res, err := http.Post(reqURL, "encoding/json", bytes.NewBuffer(bts))
	if err != nil {
		return t, err
	}
	defer res.Body.Close()

	rbts, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return t, err
	}

	// FIXME: Handle non-200s with more obvious error
	err = json.Unmarshal(rbts, &t)
	return t, err
}

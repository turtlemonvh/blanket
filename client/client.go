package client

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/tasks"
	"net/url"
	"strconv"
	"strings"
)

/*
The client package provides utilities for working with a running blanket server over HTTP

Every call goes through lib/httpx's shared client, so the CLI inherits the
same dial and response-header timeouts the worker uses
(turtlemonvh/blanket#23 phase 1) instead of hanging indefinitely against a
wedged server. These are interactive one-shot calls, so they don't retry: a
person is waiting, and the right response to a server that is down is to
say so.
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
	res, err := httpx.DoOnce(context.Background(), "GET", reqURL, nil, httpx.DefaultRequestTimeout)
	if err != nil {
		return tasks, err
	}

	// FIXME: Encode as task objects instead
	json.Unmarshal(res.Body, &tasks)

	return tasks, nil
}

// GetActiveWorkerTagSets fetches the tag set of every non-stopped worker
// registered with the server at the given port. Used by
// `blanket task-validate --check-workers` (code 014) to check whether any
// worker could claim a given task type.
func GetActiveWorkerTagSets(port int) ([][]string, error) {
	reqURL := fmt.Sprintf("http://localhost:%d/worker/", port)
	res, err := httpx.DoOnce(context.Background(), "GET", reqURL, nil, httpx.DefaultRequestTimeout)
	if err != nil {
		return nil, err
	}

	var workers []struct {
		Tags    []string `json:"tags"`
		Stopped bool     `json:"stopped"`
	}
	if err := json.Unmarshal(res.Body, &workers); err != nil {
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

// SubmitTaskOptions carries the optional scheduling fields for SubmitTask.
// Zero value means "run immediately, once" (blanket's original behavior).
type SubmitTaskOptions struct {
	// NotBefore delays a one-shot task: a Go duration ("10m"), an RFC3339
	// timestamp, or a unix-seconds integer. Mutually exclusive with Cron.
	NotBefore string
	// Cron is a standard 5-field cron expression. Setting it makes this a
	// recurring template that spawns a child task at each fire time
	// instead of running itself. Mutually exclusive with NotBefore.
	Cron string
}

func SubmitTask(taskType string, env map[string]interface{}, port int) (tasks.Task, error) {
	return SubmitTaskWithOptions(taskType, env, port, SubmitTaskOptions{})
}

// SubmitTaskWithOptions is SubmitTask plus optional scheduling fields; see
// turtlemonvh/blanket#61 and docs/task_flow.md.
func SubmitTaskWithOptions(taskType string, env map[string]interface{}, port int, opts SubmitTaskOptions) (tasks.Task, error) {
	var t tasks.Task

	body := make(map[string]interface{})
	body["type"] = taskType
	body["environment"] = env
	if opts.NotBefore != "" {
		body["notBefore"] = opts.NotBefore
	}
	if opts.Cron != "" {
		body["cron"] = opts.Cron
	}

	bts, err := json.Marshal(body)
	if err != nil {
		return t, err
	}

	reqURL := fmt.Sprintf("http://localhost:%d/task/", port)
	// A non-2xx now comes back as an error carrying the server's message,
	// which replaces the old "unmarshal whatever came back and hope"
	// handling flagged by the FIXME that used to live here.
	res, err := httpx.DoOnce(context.Background(), "POST", reqURL, bts, httpx.DefaultRequestTimeout)
	if err != nil {
		return t, err
	}

	err = json.Unmarshal(res.Body, &t)
	return t, err
}

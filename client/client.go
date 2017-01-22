package client

import (
	"encoding/json"
	"fmt"
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

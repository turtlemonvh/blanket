package tasks

import (
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/spf13/viper"
	"gopkg.in/mgo.v2/bson"
	"net/http"
	"net/url"
)

// FIXME: Move to client package
// Functions that work over http to transition task state

// Refresh information about this task by pulling from the blanket server
func (t *Task) Refresh() error {
	reqURL := fmt.Sprintf("http://localhost:%d/task/%s", viper.GetInt("port"), t.Id.Hex())
	res, err := http.Get(reqURL)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	dec := json.NewDecoder(res.Body)
	return dec.Decode(t)
}

// FIXME: Should operate on a task object and set the property on it
// Should only be called by worker
func MarkAsRunning(t *Task, extraVars map[string]string) error {
	urlParams := url.Values{}
	urlParams.Set("state", "RUNNING")
	for k, v := range extraVars {
		urlParams.Set(k, v)
	}
	paramsString := urlParams.Encode()
	reqURL := fmt.Sprintf("http://localhost:%d/task/%s/run", viper.GetInt("port"), t.Id.Hex()) + "?" + paramsString
	req, err := http.NewRequest("PUT", reqURL, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return nil
}

// Should only be called by worker
// Set task to one of the following states: ERROR/SUCCESS/TIMEDOUT/STOPPED
func MarkAsFinished(t *Task, state string) error {
	urlParams := url.Values{}
	urlParams.Set("state", state)
	paramsString := urlParams.Encode()
	reqURL := fmt.Sprintf("http://localhost:%d/task/%s/finish", viper.GetInt("port"), t.Id.Hex()) + "?" + paramsString
	req, err := http.NewRequest("PUT", reqURL, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return nil
}

// Find the oldest task we are eligible to run
func MarkAsClaimed(workerId bson.ObjectId) (Task, error) {
	// Call the REST api and get a task with the required tags
	// The worker needs to make sure it has all the tags of whatever task it requests
	reqURL := fmt.Sprintf("http://localhost:%d/task/claim/%s", viper.GetInt("port"), workerId.Hex())
	res, err := http.Post(reqURL, "application/json", nil)
	if err != nil {
		return Task{}, err
	}
	defer res.Body.Close()
	dec := json.NewDecoder(res.Body)

	if res.StatusCode != 200 {
		// FIXME: Get the error content from the JSON response
		errMsg := make(map[string]interface{})
		dec.Decode(&errMsg)
		log.WithFields(log.Fields{
			"resp": errMsg["error"],
		}).Error("Problem claiming task")
		return Task{}, fmt.Errorf("Problem claiming task; status code :: %s", res.Status)
	}

	// Try to marshall this into a task object
	var t Task
	err = dec.Decode(&t)
	if err != nil {
		return Task{}, fmt.Errorf("Error decoding claimed task; possible data corruption or server/worker version mismatch :: %s", err.Error())
	}

	return t, nil
}

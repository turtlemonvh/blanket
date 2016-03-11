package tasks

import (
	"bytes"
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/spf13/viper"
	"gopkg.in/mgo.v2/bson"
	"net/http"
	"os"
	"os/exec"
	"text/template"
)

var (
	ValidTaskStates         = []string{"WAITING", "CLAIMED", "RUNNING", "ERROR", "SUCCESS", "STOPPED", "TIMEDOUT"}
	ValidTerminalTaskStates = []string{"ERROR", "SUCCESS", "STOPPED", "TIMEDOUT"}
)

// FIXME: Reason field for why a task was stopped? audit trail of actions?
type Task struct {
	Id            bson.ObjectId     `json:"id"`            // time sortable id
	Pid           int               `json:"pid"`           // the process id used to run the task on disk
	CreatedTs     int64             `json:"createdTs"`     // when it was first added to the queue
	StartedTs     int64             `json:"startedTs"`     // when it was pulled from the queue
	LastUpdatedTs int64             `json:"lastUpdatedTs"` // last time any information changed
	TypeId        string            `json:"type"`          // String name
	ResultDir     string            `json:"resultDir"`     // Full path
	TypeDigest    string            `json:"typeDigest"`    // version hash of config file
	Timeout       int64             `json:"timeout"`       // The max time the task is allowed to run
	State         string            `json:"state"`         // See ValidTaskStates
	WorkerId      string            `json:"workerId"`      // Id of the worker that processed this task; set when CLAIMED
	Progress      int               `json:"progress"`      // 0-100
	ExecEnv       map[string]string `json:"defaultEnv"`    // Combined with default env
	Tags          []string          `json:"tags"`          // tags for capabilities of workers
}

func (t *Task) String() string {
	return fmt.Sprintf("%s %s [%d]", t.TypeId, t.Id.Hex(), t.CreatedTs)
}

// Get the command object used to run this task
// Task type is passed in so the same config is used for every step
// Maybe task type TOML should be copied when task is added so that it is saved
func (t *Task) GetCmd(tt *TaskType) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	var err error

	// Evaluate template
	tmpl, err := template.New("tasks").Parse(tt.Config.GetString("command"))
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("problem parsing task type's 'command' parameter as go template")
		return cmd, err
	}
	var cmdString bytes.Buffer
	err = tmpl.Execute(&cmdString, t.ExecEnv)
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"taskId": t.Id,
		}).Error("error evaluating template for command")
		return cmd, err
	}

	// FIXME: Don't just use bash; use python, zsh, etc
	cmd = exec.Command("bash", "-c", cmdString.String())

	// Modify execution environment with env variables
	// e.g. http://craigwickesser.com/2015/02/golang-cmd-with-custom-environment/
	env := os.Environ()
	for k, v := range t.ExecEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env

	return cmd, nil
}

func (t *Task) GetTaskType() (*TaskType, error) {
	return FetchTaskType(t.TypeId)
}

// Refresh information about this task by pulling from the blanket server
func (t *Task) Refresh() error {
	reqURL := fmt.Sprintf("http://localhost:%d/task/%s/", viper.GetInt("port"), t.Id.Hex())
	res, err := http.Get(reqURL)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	dec := json.NewDecoder(res.Body)
	return dec.Decode(t)
}

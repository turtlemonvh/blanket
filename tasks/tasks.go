package tasks

import (
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/spf13/viper"
	uuid "github.com/streadway/simpleuuid"
	"github.com/turtlemonvh/blanket/lib"
	"io"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

// Task types are just a load of configuration loaded with viper with a few extra methods
type TaskType struct {
	ConfigFile        string // path to TOML config file on disk
	LoadedTs          int64  // time loaded from disk
	ConfigVersionHash string // md5 hash of the config file
	Config            *viper.Viper
}

var validConfigfileName = regexp.MustCompile(`(\w*).toml`)

// Read types from config directory
func ReadTypes() ([]TaskType, error) {
	typesDir := viper.GetString("tasks.types_path")

	// Grab entries out of directory
	var taskTypes []TaskType
	dirEntries, err := ioutil.ReadDir(typesDir)
	if err != nil {
		// FIXME: Handle this more elegantly
		return nil, err
	}

	for _, dirEntry := range dirEntries {
		filepath := path.Join(typesDir, dirEntry.Name())
		tt, err := ReadTaskTypeFromFilepath(filepath)
		if err != nil {
			log.Error(err.Error())
			continue
		}
		taskTypes = append(taskTypes, tt)
	}

	return taskTypes, nil
}

func ReadTaskTypeFromFilepath(filepath string) (TaskType, error) {
	// Check that the file exists and is a TOML file
	fi, err := os.Stat(filepath)
	if err != nil {
		return TaskType{}, err
	}
	if fi.IsDir() {
		return TaskType{}, fmt.Errorf("Path points to a directory")
	}
	if !validConfigfileName.MatchString(filepath) {
		return TaskType{}, fmt.Errorf("Not a valid TOML file: %s", filepath)
	}

	configFile, err := os.Open(filepath)
	if err != nil {
		// FIXME: Handle this more elegantly
		return TaskType{}, err
	}
	defer configFile.Close()

	tt, err := readTaskType(configFile)
	if err != nil {
		// FIXME: Handle this more elegantly
		return TaskType{}, err
	}
	tt.ConfigFile = filepath
	tt.Config.Set("name", strings.Split(path.Base(filepath), ".toml")[0])

	// Ignore errors in finding checksum for now
	cs, err := lib.Checksum(filepath)
	if err == nil {
		tt.ConfigVersionHash = cs
	}
	return tt, nil
}

func readTaskType(configFile io.Reader) (TaskType, error) {
	tt := TaskType{}
	tt.Config = viper.New()
	tt.Config.SetConfigType("toml")
	tt.Config.SetDefault("timeout", 60)
	tt.Config.SetDefault("merge_stdout_stderr", false)

	tt.Config.ReadConfig(configFile)
	tt.LoadedTs = time.Now().Unix()

	// Check that required fields are set
	if tt.Config.GetString("command") == "" {
		return tt, fmt.Errorf("TaskType config file is missing required field 'command'.")
	}

	return tt, nil
}

func (t *TaskType) String() string {
	return fmt.Sprintf("%s [loaded=%d]", t.Config.GetString("name"), t.LoadedTs)
}

func (t *TaskType) ToJSON() (string, error) {
	ttSettings := t.Config.AllSettings()
	ttSettings["_loaded_ts"] = t.LoadedTs
	ttSettings["_config_file"] = t.ConfigFile
	ttSettings["_version_hash"] = t.ConfigVersionHash
	bts, err := json.Marshal(ttSettings)
	return string(bts), err
}

func (t *TaskType) EnvironmentVars() map[string]string {
	return t.Config.GetStringMapString("environment")
}

// Tasks inherit all the environment params of a tasktype + more
func (t *TaskType) NewTask(childEnv map[string]string) (Task, error) {
	currentTime := time.Now()
	taskType := t.Config.GetString("name")

	taskIdObj, err := uuid.NewTime(currentTime)
	if err != nil {
		log.WithFields(log.Fields{
			"currentTime": currentTime,
			"taskType":    taskType,
		}).Info("Unexpected error getting id for task")
	}
	taskId := taskIdObj.String()

	// Merge environment variables
	// FIXME: Take any files and copy them into directory
	// FIXME: This should happen at execution time
	mixedEnv := t.EnvironmentVars()
	for k, v := range childEnv {
		mixedEnv[k] = v
	}

	log.WithFields(log.Fields{
		"taskId":    taskId,
		"mixedEnv":  mixedEnv,
		"childEnv":  childEnv,
		"parentEnv": t.Config.GetStringMapString("environment"),
	}).Info("Environment variable mixing results for task")

	log.WithFields(log.Fields{
		"taskId": taskId,
		"tags":   t.Config.GetStringSlice("tags"),
	}).Info("Tag mixing results for task")

	return Task{
		Id:            taskId,
		CreatedTs:     time.Now().Unix(),
		LastUpdatedTs: time.Now().Unix(),
		TypeId:        t.Config.GetString("name"),
		TypeDigest:    "",
		ResultDir:     path.Join(viper.GetString("tasks.results_path"), taskId),
		State:         "WAIT",
		Progress:      0,
		ExecEnv:       mixedEnv,
		Tags:          t.Config.GetStringSlice("tags"),
	}, nil
}

var ValidTaskStates = []string{"WAIT", "START", "RUNNING", "ERROR", "SUCCESS"}

type Task struct {
	Id            string            `json:"id"`            // uuid
	CreatedTs     int64             `json:"createdTs"`     // when it was first added to the database
	StartedTs     int64             `json:"startedTs"`     // when it started running
	LastUpdatedTs int64             `json:"lastUpdatedTs"` // last time any information changed
	TypeId        string            `json:"type"`          // String name
	ResultDir     string            `json:"resultDir"`     // Full path
	TypeDigest    string            `json:"typeDigest"`    // version hash of config file
	State         string            `json:"state"`         // WAIT, START, RUN, SUCCESS/ERROR
	Progress      int               `json:"progress"`      // 0-100
	ExecEnv       map[string]string `json:"defaultEnv"`    // Combined with default env
	Tags          []string          `json:"tags"`          // tags for capabilities of workers
}

func (t *Task) String() string {
	return fmt.Sprintf("%d [%d]", t.TypeId, t.CreatedTs)
}

func (t *Task) ToJSON() (string, error) {
	bts, err := json.Marshal(t)
	return string(bts), err
}

// FIXME: Move some of the complexity out of worker and into here
func (t *Task) Execute() error {
	// Fetch the task type for this task
	filepath := path.Join(viper.GetString("tasks.types_path"), fmt.Sprintf("%s.toml", t.TypeId))
	tt, err := ReadTaskTypeFromFilepath(filepath)
	if err != nil {
		return err
	}

	fmt.Println("tt", tt)

	return nil
}

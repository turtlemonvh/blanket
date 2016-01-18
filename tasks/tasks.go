package tasks

import (
	"encoding/json"
	"fmt"
	"github.com/satori/go.uuid"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path"
	"regexp"
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
			log.Printf("ERROR: %s", err.Error())
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
		return TaskType{}, fmt.Errorf("Not a valid TOML file")
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

	// Check that required fields are set

	tt.Config.ReadConfig(configFile)
	tt.LoadedTs = time.Now().Unix()

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

// Tasks inherit all the environment params of a tasktype + more
func (t *TaskType) NewTask(envOverrides map[string]string) (Task, error) {
	// FIXME: Merge environment variables
	// Take any files and copy them into directory
	taskId := uuid.NewV4().String()
	return Task{
		Id:            taskId,
		CreatedTs:     time.Now().Unix(),
		LastUpdatedTs: time.Now().Unix(),
		TypeId:        t.Config.GetString("name"),
		ResultDir:     path.Join(viper.GetString("tasks.results_path"), taskId),
		State:         "WAIT",
		Progress:      0,
		ExecEnv:       envOverrides,
	}, nil
}

type Task struct {
	Id            string            `json:"id"` // uuid; change to id that includes time
	CreatedTs     int64             `json:"createdTs"`
	StartedTs     int64             `json:"startedTs"`     // when it started running
	LastUpdatedTs int64             `json:"lastUpdatedTs"` // last time any information changed
	TypeId        string            `json:"type"`          // String name
	ResultDir     string            `json"resultDir"`      // Full path
	State         string            `json"state"`          // WAIT, START, RUN, SUCCESS/ERROR
	Progress      int               `json"progress"`       // 0-100
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

func (t *Task) Execute() error {
	return nil
}

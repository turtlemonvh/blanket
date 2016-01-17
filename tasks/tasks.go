package tasks

import (
	"encoding/json"
	"fmt"
	"github.com/satori/go.uuid"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib"
	"io"
	"io/ioutil"
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
		// Check that this is a TOML file
		if dirEntry.IsDir() {
			continue
		}
		configFilename := path.Join(typesDir, dirEntry.Name())
		if !validConfigfileName.MatchString(configFilename) {
			continue
		}

		configFile, err := os.Open(configFilename)
		if err != nil {
			// FIXME: Handle this more elegantly
			return nil, err
		}
		defer configFile.Close()

		tt, err := ReadTaskType(configFile)
		if err != nil {
			// FIXME: Handle this more elegantly
			return nil, err
		}
		tt.ConfigFile = configFilename

		// Ignore errors in finding checksum for now
		cs, err := lib.Checksum(configFilename)
		if err == nil {
			tt.ConfigVersionHash = cs
		}

		taskTypes = append(taskTypes, tt)
	}

	return taskTypes, nil
}

func ReadTaskType(configFile io.Reader) (TaskType, error) {
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
	return Task{
		Id:            uuid.NewV4().String(),
		CreatedTs:     time.Now().Unix(),
		LastUpdatedTs: time.Now().Unix(),
		TypeId:        t.Config.GetString("name"),
		ExecEnv:       envOverrides,
	}, nil
}

type Task struct {
	Id            string            `json:"id"`
	CreatedTs     int64             `json:"createdTs"`
	LastUpdatedTs int64             `json:"lastUpdatedTs"`
	TypeId        string            `json:"type"`
	ResultDir     string            `json"resultDir"`
	ExecEnv       map[string]string `json:"defaultEnv"`
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

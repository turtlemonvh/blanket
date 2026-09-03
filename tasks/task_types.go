package tasks

import (
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"io"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	DEFAULT_TIMEOUT = 3600 // default timeout is 1 hour
)

var validConfigfileName = regexp.MustCompile(`^\w+\.toml$`)

// Task types are just a load of configuration loaded with viper with a few extra methods
type TaskType struct {
	ConfigFile        string // path to TOML config file on disk
	LoadedTs          int64  // time loaded from disk
	ConfigVersionHash string // md5 hash of the config file
	Config            *viper.Viper
}

func FetchTaskType(typeName string) (*TaskType, error) {
	var foundType TaskType

	tts, err := ReadTypes()
	if err != nil {
		return &foundType, err
	}

	for _, tt := range tts {
		if tt.GetName() == typeName {
			return &tt, nil
		}
	}

	return &foundType, fmt.Errorf("No task of type '%s' could be located", typeName)
}

// Read types from config directory
func ReadTypes() ([]TaskType, error) {
	typesDirs := viper.GetStringSlice("tasks.typesPaths")

	// Grab entries out of directories
	var taskTypes []TaskType
	for _, typesDir := range typesDirs {
		dirEntries, err := ioutil.ReadDir(typesDir)
		if err != nil {
			// FIXME: Handle this more elegantly
			log.WithFields(log.Fields{
				"error":    err.Error(),
				"filepath": typesDir,
			}).Error("Problem reading task types directory location")
			return taskTypes, err
		}

		for _, dirEntry := range dirEntries {
			filepath := path.Join(typesDir, dirEntry.Name())
			if !validConfigfileName.MatchString(dirEntry.Name()) {
				continue
			}

			tt, err := ReadTaskTypeFromFilepath(filepath)
			if err != nil {
				log.WithFields(log.Fields{
					"error":    err.Error(),
					"filepath": filepath,
				}).Error("Problem loading toml file")
				continue
			}
			taskTypes = append(taskTypes, tt)
		}
	}

	return taskTypes, nil
}

// ReadTaskTypesForValidation loads every *.toml file across typesDirs using
// the lenient (ReadTaskTypeFromFilepathForValidation) path, so task-validate
// can report on files ReadTypes() would silently skip (e.g. a missing
// `command` field). Kept separate from ReadTypes() rather than sharing its
// directory-walk loop: ReadTypes() treats an unreadable typesDir as a hard
// server-startup error, while validation should keep going and report what
// it can — different enough error semantics that merging them risked
// changing ReadTypes()'s existing behavior for a modest DRY gain.
//
// Returns one TaskType per file plus its load error (nil on a clean load),
// in directory order then filename order.
func ReadTaskTypesForValidation(typesDirs []string) ([]TaskType, []error) {
	var types []TaskType
	var errs []error
	for _, typesDir := range typesDirs {
		dirEntries, err := ioutil.ReadDir(typesDir)
		if err != nil {
			log.WithFields(log.Fields{
				"error":    err.Error(),
				"filepath": typesDir,
			}).Warn("Problem reading task types directory location")
			continue
		}
		for _, dirEntry := range dirEntries {
			fp := path.Join(typesDir, dirEntry.Name())
			if !validConfigfileName.MatchString(dirEntry.Name()) {
				continue
			}
			tt, err := ReadTaskTypeFromFilepathForValidation(fp)
			types = append(types, tt)
			errs = append(errs, err)
		}
	}
	return types, errs
}

func ReadTaskTypeFromFilepath(filepath string) (TaskType, error) {
	// Check that the file exists and is a TOML file
	fi, err := os.Stat(filepath)
	if err != nil {
		return TaskType{}, err
	}
	if fi.IsDir() {
		// FIXME: Should be silent error
		return TaskType{}, fmt.Errorf("Path points to a directory")
	}

	configFile, err := os.Open(filepath)
	if err != nil {
		// FIXME: Handle this more elegantly
		return TaskType{}, err
	}
	defer configFile.Close()

	tt, err := ReadTaskType(configFile)
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

// ReadTaskTypeFromFilepathForValidation loads a task type TOML file the way
// ReadTaskTypeFromFilepath does, but tolerates a missing `command` field —
// callers that validate task types (task-validate) need the
// partially-parsed config to report on, not a silently discarded one, the
// way ReadTypes() drops such files to keep them off the serving path.
// Filesystem-level errors (file missing, unreadable, a directory) still
// propagate as-is; ConfigFile and name are set even when readErr != nil.
func ReadTaskTypeFromFilepathForValidation(filepath string) (TaskType, error) {
	fi, err := os.Stat(filepath)
	if err != nil {
		return TaskType{}, err
	}
	if fi.IsDir() {
		return TaskType{}, fmt.Errorf("Path points to a directory")
	}

	configFile, err := os.Open(filepath)
	if err != nil {
		return TaskType{}, err
	}
	defer configFile.Close()

	tt, readErr := ReadTaskType(configFile)
	tt.ConfigFile = filepath
	tt.Config.Set("name", strings.Split(path.Base(filepath), ".toml")[0])
	if cs, err := lib.Checksum(filepath); err == nil {
		tt.ConfigVersionHash = cs
	}
	return tt, readErr
}

func ReadTaskType(configFile io.Reader) (TaskType, error) {
	tt := TaskType{}
	tt.Config = viper.New()
	tt.Config.SetConfigType("toml")
	tt.Config.SetDefault("timeout", DEFAULT_TIMEOUT)

	err := tt.Config.ReadConfig(configFile)
	if err != nil {
		tt.Config.Set("validationError", err.Error())
	}

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

func (t *TaskType) GetName() string {
	return t.Config.GetString("name")
}

// GetDescription returns the one-line summary of what this task type does.
func (t *TaskType) GetDescription() string {
	return t.Config.GetString("description")
}

// GetDocumentation returns the long-form setup/prerequisites/gotchas text
// for this task type.
func (t *TaskType) GetDocumentation() string {
	return t.Config.GetString("documentation")
}

// Implement Marshaler
func (t *TaskType) MarshalJSON() ([]byte, error) {
	ttSettings := t.Config.AllSettings()
	ttSettings["loadedTs"] = t.LoadedTs
	ttSettings["configFile"] = t.ConfigFile
	ttSettings["versionHash"] = t.ConfigVersionHash
	return json.Marshal(ttSettings)
}

// Return a map of default values, {name => value}
func (t *TaskType) DefaultEnv() map[string]string {
	defaultEnv := cast.ToSlice(t.Config.Get("environment.default"))
	env := make(map[string]string)
	for _, envVar := range defaultEnv {
		ev := cast.ToStringMap(envVar)
		evName := cast.ToString(ev["name"])
		evValue := cast.ToString(ev["value"])
		env[evName] = evValue
	}
	return env
}

// EnvNames returns the input names declared under
// environment.<section> ("default", "required", or "optional"), in file
// order, skipping any entry missing a name.
func (t *TaskType) EnvNames(section string) []string {
	raw, ok := t.Config.Get("environment." + section).([]interface{})
	if !ok {
		return nil
	}
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if name, ok := m["name"].(string); ok && name != "" {
			names = append(names, name)
		}
	}
	return names
}

func (t *TaskType) HasRequiredEnv() bool {
	defaultEnv := cast.ToSlice(t.Config.Get("environment.required"))
	return len(defaultEnv) != 0
}

// Return a map of default values, {name => type(string)}
func (t *TaskType) RequiredEnv() map[string]string {
	defaultEnv := cast.ToSlice(t.Config.Get("environment.required"))
	env := make(map[string]string)
	for _, envVar := range defaultEnv {
		ev := cast.ToStringMap(envVar)
		evName := cast.ToString(ev["name"])
		evType := cast.ToString(ev["type"])
		env[evName] = evType
	}
	return env
}

// Tasks inherit all the environment params of a tasktype + more
func (t *TaskType) NewTask(childEnv map[string]string) (Task, error) {
	taskType := t.Config.GetString("name")
	taskId := objectid.NewObjectId()

	// Merge environment variables
	// FIXME: Take any files and copy them into directory
	// FIXME: This should happen at execution time
	mixedEnv := t.DefaultEnv()
	for k, v := range childEnv {
		mixedEnv[k] = v
	}

	log.WithFields(log.Fields{
		"taskId":    taskId.Hex(),
		"mixedEnv":  mixedEnv,
		"childEnv":  childEnv,
		"parentEnv": t.Config.GetStringMapString("environment"),
	}).Info("Environment variable mixing results for task")

	log.WithFields(log.Fields{
		"taskId":   taskId.Hex(),
		"taskType": taskType,
		"tags":     t.Config.GetStringSlice("tags"),
	}).Info("Tag mixing results for task")

	return Task{
		Id:            taskId,
		CreatedTs:     time.Now().Unix(),
		LastUpdatedTs: time.Now().Unix(),
		TypeId:        t.Config.GetString("name"),
		TypeDigest:    "",
		ResultDir:     path.Join(viper.GetString("tasks.resultsPath"), taskId.Hex()),
		State:         "WAITING",
		Progress:      0,
		ExecEnv:       mixedEnv,
		Tags:          t.Config.GetStringSlice("tags"),
	}, nil
}

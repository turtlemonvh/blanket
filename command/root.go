package command

/*

Imports the commands folder
Directs to relevant command line option

*/

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"os"
	"path/filepath"
	"runtime"
)

var blanketCmdV *cobra.Command
var (
	CfgFile  string
	LogLevel string
	Version  string
)

func Run(VERSION string, BRANCH string, COMMIT string, BUILD_DATE string) {
	if VERSION != "" {
		Version = fmt.Sprintf("blanket %s (built %s)", VERSION, BUILD_DATE)
	} else {
		Version = fmt.Sprintf("blanket (dev) branch=%s commit=%s (built %s)", BRANCH, COMMIT, BUILD_DATE)
	}
	RootCmd.Execute()
}

func init() {
	//cobra.OnInitialize(initConfig)
	RootCmd.PersistentFlags().Int32P("port", "p", 8773, "Port the server will run on")
	RootCmd.PersistentFlags().StringVar(&LogLevel, "logLevel", "info", "the logging level to use")
	RootCmd.PersistentFlags().StringVarP(&CfgFile, "config", "c", "", "config file (default is config.json|yaml|toml in the blanket config dir)")
	RootCmd.AddCommand(versionCmd)
	RootCmd.AddCommand(taskValidateCmd)
	blanketCmdV = RootCmd

	// FIXME: Add support for multiple outputs and handling log levels via command line or env variable
	// https://golang.org/src/io/multi.go?s=1355:1397#L47
	log.SetOutput(os.Stdout)
	log.SetLevel(log.WarnLevel)
}

// SetConfigDefaults registers every built-in default.
//
// Split out of InitializeConfig so it can be exercised without a config
// file on disk (InitializeConfig log.Fatals when it can't find one), which
// is what lets command/root_test.go assert that the defaults don't shadow
// each other -- see the note on the storage.* keys below.
func SetConfigDefaults() {
	// Add reloads for select config values
	// https://github.com/spf13/viper#watching-and-re-reading-config-files
	viper.SetDefault("port", 8773)
	viper.SetDefault("database", "blanket.db")
	viper.SetDefault("tasks.typesPaths", []string{"types"})
	// FIXME: Why is this a slice? It makes sending a target result dir to a client pretty tough.
	viper.SetDefault("tasks.resultsPath", []string{"results"})
	viper.SetDefault("workers.logfileNameTemplate", "worker.{{.Id.Hex}}.log")

	// Whether a worker records how a task's two output streams
	// interleaved, as blanket.combined.ndjson in the result dir
	// (turtlemonvh/blanket#104). On by default: it is what lets the UI's
	// combined log view show a finished task's output in the order it
	// was produced rather than grouped by stream.
	//
	// Turning it off restores the pre-#104 execution shape exactly --
	// the child writes straight into blanket.stdout.log /
	// blanket.stderr.log with no pipe and no worker in between. The
	// reason to want that: with a pipe, a task that backgrounds a
	// process and exits has its output closed a couple of seconds later
	// (worker.childWaitDelay), where before the orphan could keep
	// writing into the log file indefinitely.
	viper.SetDefault("workers.combinedLog", true)
	viper.SetDefault("mcp.enabled", true)
	viper.SetDefault("mcp.mode", "all")
	viper.SetDefault("mcp.writeTypesPath", "")
	viper.SetDefault("mcp.validateStrict", false)
	viper.SetDefault("mcp.maxLogLines", 200)

	// Synchronous ("blocking") task submission -- POST /task/?wait
	// (turtlemonvh/blanket#27). defaultWait applies to a bare ?wait;
	// maxWait is a hard cap (a larger ?wait is a 400, not a clamp) and is
	// the only control on how long an unauthenticated caller can hold a
	// connection and a goroutine open, so it is deliberately conservative.
	// maxLogLines bounds the stdout/stderr tails in the completion
	// payload; maxResultBytes bounds the declared result_file that gets
	// parsed into it.
	viper.SetDefault("tasks.sync.defaultWait", "30s")
	viper.SetDefault("tasks.sync.maxWait", "300s")
	viper.SetDefault("tasks.sync.maxLogLines", 200)
	viper.SetDefault("tasks.sync.maxResultBytes", 1048576)

	// How often the scheduler loop checks for due SCHEDULED tasks and
	// RECURRING task templates (turtlemonvh/blanket#61). Accepts anything
	// time.ParseDuration understands, e.g. "2s", "500ms".
	viper.SetDefault("scheduler.interval", "2s")

	// Upper bound on how many SCHEDULED+RECURRING+PAUSED tasks may be
	// live at once. POST /task/ returns 429 once a new notBefore-future or
	// cron submission would reach this many; it also bounds how many rows
	// a single scheduler tick will scan (server.DefaultSchedulerMaxScheduled's
	// doc comment explains why the same number serves both purposes).
	viper.SetDefault("scheduler.maxScheduled", 10000)

	// The reaper (turtlemonvh/blanket#23 phase 3): the background loop
	// that reconciles what a crash leaves behind -- workers that stopped
	// heartbeating, tasks whose worker died mid-run, queue entries whose
	// claim was never acked.
	//
	// It ships enabled. The switch exists because the failure this code
	// guards against (destroyed task state) is also the failure it could
	// itself cause, and an operator debugging a suspected false positive
	// should be able to stop it in one config line rather than by
	// downgrading.
	//
	// Every duration accepts anything time.ParseDuration understands, and
	// is scaled by timeMultiplier at use. The thresholds are deliberately
	// generous next to a worker's 2s check interval: waiting costs a stale
	// row in the UI, acting early costs real work. See docs/task_flow.md
	// ("The reaper") for the decision table these feed.
	viper.SetDefault("reaper.enabled", true)
	viper.SetDefault("reaper.interval", "30s")
	// When a silent worker is marked LOST in the UI. Nothing is stopped or
	// rewritten at this threshold.
	viper.SetDefault("reaper.workerStaleAfter", "2m")
	// The only threshold that can stop a worker on heartbeat silence alone
	// -- i.e. when pid liveness could not answer (an unsupported platform,
	// a record with no pidStartTs). Much longer, because that is much
	// weaker evidence than a conclusively dead process.
	viper.SetDefault("reaper.workerDeadAfter", "10m")
	// How long a CLAIMED/RUNNING task may go without an update before the
	// reaper looks at it. Looking is not acting: what happens next depends
	// on the task's outcome journal.
	viper.SetDefault("reaper.taskStaleAfter", "5m")
	// Poison-task guard: how many times one task may be requeued after the
	// worker that claimed it died before starting it.
	viper.SetDefault("reaper.maxRequeues", 3)

	// Database schema, backups, and the file lock
	// (turtlemonvh/blanket#23 phase 4). See docs/upgrade.md.
	//
	// These are `storage.*` and not `database.*`, which is what they
	// obviously should have been called, because viper stores defaults in
	// a nested map: setting `database.openTimeout` turns `database` into a
	// map and silently blanks the `database` *path* set above it. The
	// server then starts with an empty database path and dies with
	// `open : no such file or directory`. A scalar key and a subtree
	// cannot share a name, and `database` has been the path since blanket
	// was written. root_test.go pins this down so the trap can only be
	// walked into once.
	//
	// openTimeout is how long to wait for bolt's exclusive lock before
	// giving up. It was hardcoded at 1s, which is shorter than a normal
	// shutdown: under `Restart=always` a supervisor starts the
	// replacement immediately, and it has to outwait the old process's
	// drain-and-teardown or a routine restart becomes a crash loop.
	viper.SetDefault("storage.openTimeout", "5s")
	// Where backups go. Empty means <database dir>/backups -- beside the
	// database, which is where somebody restoring at 2am will look.
	viper.SetDefault("storage.backupDir", "")
	// How many backups to keep. Three, matching the three rollback slots
	// phase 6's `blanket rollback` keeps: the database half of a slot is
	// exactly one of these files. A count rather than a size cap, with a
	// free-space warning instead of a hard budget (brief decision row 9).
	viper.SetDefault("storage.backupRetention", 3)

	// Time multiplier can be used in tests to speed up tests
	viper.SetDefault("timeMultiplier", "1.0")
}

func InitializeConfig() {
	SetConfigDefaults()

	viper.SetConfigName("config")
	if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			viper.AddConfigPath(filepath.Join(localAppData, "blanket"))
		}
	} else {
		configHome := os.Getenv("XDG_CONFIG_HOME")
		if configHome == "" {
			if home, err := os.UserHomeDir(); err == nil {
				configHome = filepath.Join(home, ".config")
			}
		}
		if configHome != "" {
			viper.AddConfigPath(filepath.Join(configHome, "blanket"))
		}
	}
	viper.AddConfigPath("/etc/blanket/")
	viper.AddConfigPath("$HOME/.blanket")
	viper.AddConfigPath(".")
	viper.SetConfigFile(CfgFile)
	err := viper.ReadInConfig()
	if err != nil {
		log.WithFields(log.Fields{
			"error": err.Error(),
		}).Fatal("Please either add a config file in one of the predefined locations or pass in a path explicitly.")
	}

	// https://github.com/spf13/viper#working-with-environment-variables
	viper.SetEnvPrefix("BLANKET_APP_")
	viper.AutomaticEnv()

	viper.BindPFlag("port", blanketCmdV.PersistentFlags().Lookup("port"))
	viper.BindPFlag("logLevel", blanketCmdV.PersistentFlags().Lookup("logLevel"))
}

func InitializeLogging() {
	var level log.Level
	var err error
	level, err = log.ParseLevel(viper.GetString("logLevel"))
	if err != nil {
		log.WithFields(log.Fields{
			"levelChoice": viper.GetString("logLevel"),
		}).Error("invalid choice for option 'level'. Ignoring and continuing.")
	} else {
		log.SetLevel(level)
		log.WithFields(log.Fields{
			"level": level,
		}).Info("setting loglevel from config")
	}
}
